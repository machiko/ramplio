package engine

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCollector returns a collector whose aggregator goroutine is stopped when
// the test ends, so tests that never call Run do not leak it.
func testCollector(t *testing.T) *metrics.Collector {
	t.Helper()
	c := metrics.NewCollector(1)
	t.Cleanup(func() { c.Stop() })
	return c
}

// --- circuit breaker sliding window -----------------------------------------

func TestFailWindow_TripsAtThresholdWithinWindow(t *testing.T) {
	w := newFailWindow(3, time.Second)
	t0 := time.Unix(1000, 0)

	assert.False(t, w.record(t0))
	assert.False(t, w.record(t0.Add(100*time.Millisecond)))
	assert.True(t, w.record(t0.Add(200*time.Millisecond)), "third failure inside 1s must trip")
}

func TestFailWindow_ThresholdOneTripsOnFirstFailure(t *testing.T) {
	w := newFailWindow(1, time.Second)
	assert.True(t, w.record(time.Unix(1000, 0)))
}

// Failures spread wider than the window must not trip — this is the whole point
// of the sliding window over a bare counter (burst-then-recover is not an outage).
func TestFailWindow_SpreadFailuresDoNotTripUntilDense(t *testing.T) {
	w := newFailWindow(3, time.Second)
	t0 := time.Unix(1000, 0)

	assert.False(t, w.record(t0))
	assert.False(t, w.record(t0.Add(600*time.Millisecond)))
	// cutoff = t0+200ms: t0 has aged out, only 2 failures remain in the window.
	assert.False(t, w.record(t0.Add(1200*time.Millisecond)))
	// The ring buffer overwrites t0's slot; t0+600/1200/1300 all sit inside the window.
	assert.True(t, w.record(t0.Add(1300*time.Millisecond)))
}

// A failure exactly one window old sits on the cutoff and is not counted
// (ts.After(cutoff) is strict).
func TestFailWindow_FailureOnCutoffIsExcluded(t *testing.T) {
	w := newFailWindow(2, time.Second)
	t0 := time.Unix(1000, 0)

	assert.False(t, w.record(t0))
	assert.False(t, w.record(t0.Add(time.Second)), "t0 equals the cutoff and must not count")
	assert.True(t, w.record(t0.Add(time.Second+time.Millisecond)))
}

func TestFailWindow_ResetClearsCountedFailures(t *testing.T) {
	w := newFailWindow(3, time.Second)
	t0 := time.Unix(1000, 0)

	w.record(t0)
	w.record(t0.Add(time.Millisecond))
	w.reset()

	assert.False(t, w.record(t0.Add(2*time.Millisecond)), "a success in between must wipe earlier failures")
	assert.False(t, w.record(t0.Add(3*time.Millisecond)))
	assert.True(t, w.record(t0.Add(4*time.Millisecond)))
}

// Once tripped the breaker latches: a later success or a quiet period does not
// re-arm it, because the run is being stopped.
func TestFailWindow_TripLatches(t *testing.T) {
	w := newFailWindow(1, time.Second)
	t0 := time.Unix(1000, 0)

	require.True(t, w.record(t0))
	w.reset()
	assert.True(t, w.record(t0.Add(time.Hour)))
}

// N concurrent failures against an N+1 threshold: none may trip, and the next one
// must — so every concurrent record was counted exactly once (no loss, no double).
func TestFailWindow_ConcurrentRecordsCountExactlyOnce(t *testing.T) {
	const goroutines, perG = 16, 50
	w := newFailWindow(goroutines*perG+1, time.Minute)
	now := time.Now()

	var trippedEarly atomic.Bool
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perG {
				if w.record(now) {
					trippedEarly.Store(true)
				}
			}
		}()
	}
	wg.Wait()

	assert.False(t, trippedEarly.Load(), "N failures must not trip an N+1 threshold")
	assert.True(t, w.record(now), "the N+1th failure must trip")
}

// Every VU (and the rate dispatcher) polls isCircuitTripped while other
// goroutines record failures; under -race this pins down that the flag is
// published safely, and every reader must eventually observe the trip.
func TestCircuitBreaker_TripVisibleToConcurrentReaders(t *testing.T) {
	const writers, perWriter, readers = 8, 25, 8
	e := NewRamp(RampConfig{CircuitBreaker: &scenarios.CircuitBreakerConfig{
		ConsecutiveFailures: writers * perWriter, WindowSeconds: 60,
	}}, testCollector(t))

	stop := make(chan struct{})
	var sawTrip atomic.Int32
	var rg sync.WaitGroup
	for range readers {
		rg.Add(1)
		go func() {
			defer rg.Done()
			for {
				if e.isCircuitTripped() {
					sawTrip.Add(1)
					return
				}
				select {
				case <-stop:
					return
				default:
					runtime.Gosched() // keep spinning readers from starving the writers
				}
			}
		}()
	}

	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				e.incFails()
			}
		}()
	}
	wg.Wait()
	require.True(t, e.isCircuitTripped(), "the last failure reaches the threshold")

	readersDone := make(chan struct{})
	go func() { rg.Wait(); close(readersDone) }()
	select {
	case <-readersDone:
	case <-time.After(5 * time.Second):
		close(stop)
		<-readersDone
	}
	assert.Equal(t, int32(readers), sawTrip.Load(), "every polling goroutine must observe the trip")
}

func TestNewRamp_CircuitBreakerConfig(t *testing.T) {
	tests := []struct {
		name          string
		cb            *scenarios.CircuitBreakerConfig
		wantEnabled   bool
		wantThreshold int
		wantWindow    time.Duration
	}{
		{name: "nil config disables", cb: nil},
		{name: "zero failures disables", cb: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: 0, WindowSeconds: 5}},
		{name: "negative failures disables", cb: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: -1}},
		{name: "window defaults to 1s", cb: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: 4},
			wantEnabled: true, wantThreshold: 4, wantWindow: time.Second},
		{name: "negative window defaults to 1s", cb: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: 2, WindowSeconds: -3},
			wantEnabled: true, wantThreshold: 2, wantWindow: time.Second},
		{name: "explicit window", cb: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: 10, WindowSeconds: 5},
			wantEnabled: true, wantThreshold: 10, wantWindow: 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewRamp(RampConfig{CircuitBreaker: tt.cb}, testCollector(t))
			if !tt.wantEnabled {
				assert.Nil(t, e.cbWindow)
				assert.False(t, e.isCircuitTripped())
				return
			}
			require.NotNil(t, e.cbWindow)
			assert.Equal(t, tt.wantThreshold, e.cbWindow.threshold)
			assert.Len(t, e.cbWindow.times, tt.wantThreshold)
			assert.Equal(t, tt.wantWindow, e.cbWindow.window)
		})
	}
}

// --- teardown context -------------------------------------------------------

func TestTeardownContext(t *testing.T) {
	const budget = 200 * time.Millisecond

	t.Run("a run that is never cancelled is unbounded", func(t *testing.T) {
		got, release := teardownContextWithBudget(context.Background(), 10*time.Millisecond)
		defer release()
		_, bounded := got.Deadline()
		assert.False(t, bounded, "a normal teardown must not be cut short")
		time.Sleep(100 * time.Millisecond) // well past the budget
		assert.NoError(t, got.Err(), "the budget only starts on a cancel")
	})
	t.Run("a cancel before teardown starts the budget", func(t *testing.T) {
		run, cancelRun := context.WithCancel(context.Background())
		cancelRun()
		got, release := teardownContextWithBudget(run, budget)
		defer release()
		assert.NoError(t, got.Err(), "teardown must still be able to send after a cancel")
		assert.Eventually(t, func() bool { return got.Err() != nil }, 5*time.Second, 5*time.Millisecond,
			"teardown must stop once the budget is spent")
	})
	t.Run("a cancel during teardown does not abort it, only starts the budget", func(t *testing.T) {
		run, cancelRun := context.WithCancel(context.Background())
		got, release := teardownContextWithBudget(run, budget)
		defer release()
		cancelRun() // e.g. Ctrl-C while a normal teardown is still running
		assert.NoError(t, got.Err(), "an in-progress teardown must not be aborted by the cancel")
		assert.Eventually(t, func() bool { return got.Err() != nil }, 5*time.Second, 5*time.Millisecond)
	})
	t.Run("release ends it", func(t *testing.T) {
		run, cancelRun := context.WithCancel(context.Background())
		defer cancelRun()
		got, release := teardownContextWithBudget(run, budget)
		release()
		assert.Error(t, got.Err())
	})
	t.Run("production budget", func(t *testing.T) {
		assert.Equal(t, 30*time.Second, CancelledTeardownTimeout)
	})
}

// --- collector sizing ---------------------------------------------------------

// CollectorCapacity sizes the sample buffer to the most requests that can
// complete at once: peak VUs in VU mode, the worker-pool ceiling in rate mode.
// Rate-mode scenarios used to get the VU rule (no VUs → 1 → a 10-sample buffer).
func TestCollectorCapacity(t *testing.T) {
	tests := []struct {
		name   string
		stages []scenarios.Stage
		want   int
	}{
		{"vu mode takes the peak target", []scenarios.Stage{{Target: 3}, {Target: 7}, {Target: 0}}, 7},
		{"no load still gets a buffer", nil, 1},
		{"rate mode uses the worker ceiling", []scenarios.Stage{{TargetRPS: 40}, {TargetRPS: 100}}, 100 * rateWorkerHeadroom},
		{"rate mode floor", []scenarios.Stage{{TargetRPS: 1}}, rateMinWorkers},
		{"rate mode cap", []scenarios.Stage{{TargetRPS: 5000}}, rateHardCap},
		// The parser rejects mixing; if it ever slips through, follow the engine
		// (isRateMode: any TargetRPS > 0 runs in rate mode).
		{"mixed stages follow rate mode", []scenarios.Stage{{Target: 50}, {TargetRPS: 4}}, 4 * rateWorkerHeadroom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CollectorCapacity(tt.stages))
		})
	}
}

// The collector must be sized for exactly as many concurrent completions as
// the rate-mode worker pool allows.
func TestCollectorCapacityMatchesRateWorkerCeiling(t *testing.T) {
	for _, rps := range []int{1, 2, 3, 50, 999, 1000, 1001, 20000} {
		assert.Equal(t, rateWorkerCeiling(rps), CollectorCapacity([]scenarios.Stage{{TargetRPS: rps}}), "rps=%d", rps)
	}
}

// --- data file rows ---------------------------------------------------------

func rowsN(n int) []map[string]string {
	rows := make([]map[string]string, n)
	for i := range rows {
		rows[i] = map[string]string{"id": string(rune('a' + i))}
	}
	return rows
}

func TestDataSource_EmptyRowsReturnNil(t *testing.T) {
	for _, mode := range []string{"", "sequential", "random"} {
		assert.Nil(t, newDataSource(nil, mode).next(), "mode %q", mode)
	}
}

func TestDataSource_SequentialCyclesInOrder(t *testing.T) {
	ds := newDataSource(rowsN(3), "sequential")
	var got []string
	for range 7 {
		got = append(got, ds.next()["id"])
	}
	assert.Equal(t, []string{"a", "b", "c", "a", "b", "c", "a"}, got)
}

func TestDataSource_UnknownModeFallsBackToSequential(t *testing.T) {
	ds := newDataSource(rowsN(2), "")
	assert.Equal(t, "a", ds.next()["id"])
	assert.Equal(t, "b", ds.next()["id"])
}

// Implementation-agnostic: with plenty of draws no row may be starved.
func TestDataSource_RandomReachesEveryRow(t *testing.T) {
	for _, n := range []int{1, 3, 10} {
		ds := newDataSource(rowsN(n), "random")
		seen := make(map[string]bool, n)
		for range 100 * n {
			seen[ds.next()["id"]] = true
		}
		assert.Len(t, seen, n, "n=%d", n)
	}
}

// Known gap, deliberately not locked in here: the current stride is
// 2654435761 mod len(rows), which is 1 for every row count dividing 2654435760
// (2, 3, 4, 5, 6, 8, 10, 12, 15, 16, 17, 20, …) — for those sizes "random" draws
// in exactly sequential order. n=7 is a size where the order does differ.
func TestDataSource_RandomOrderDiffersFromSequential(t *testing.T) {
	seq := newDataSource(rowsN(7), "sequential")
	rnd := newDataSource(rowsN(7), "random")
	var seqOrder, rndOrder []string
	for range 7 {
		seqOrder = append(seqOrder, seq.next()["id"])
		rndOrder = append(rndOrder, rnd.next()["id"])
	}
	assert.NotEqual(t, seqOrder, rndOrder)
}

// VUs pull rows concurrently; the atomic counter must hand out rows evenly with
// no duplicates or skips.
func TestDataSource_ConcurrentSequentialDistributesEvenly(t *testing.T) {
	const goroutines, perG, nRows = 20, 50, 4
	ds := newDataSource(rowsN(nRows), "sequential")

	var mu sync.Mutex
	counts := make(map[string]int)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perG {
				id := ds.next()["id"]
				mu.Lock()
				counts[id]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for _, id := range []string{"a", "b", "c", "d"} {
		assert.Equal(t, goroutines*perG/nRows, counts[id], "row %s", id)
	}
}

func TestNewRamp_DataSourceOnlyWithRows(t *testing.T) {
	assert.Nil(t, NewRamp(RampConfig{}, testCollector(t)).data)

	e := NewRamp(RampConfig{DataRows: rowsN(2), DataMode: "random"}, testCollector(t))
	require.NotNil(t, e.data)
	assert.Equal(t, "random", e.data.mode)
	assert.Contains(t, []string{"a", "b"}, e.newVarContext().Data["id"], "each new VU context draws a row")
}

// --- live stage readout -----------------------------------------------------

func TestStageInfo_EdgeCases(t *testing.T) {
	t.Run("before run", func(t *testing.T) {
		e := NewRamp(RampConfig{Stages: []scenarios.Stage{{Duration: time.Second}}}, testCollector(t))
		cur, total, pct := e.StageInfo()
		assert.Zero(t, cur)
		assert.Zero(t, total, "total stays hidden until a stage starts")
		assert.Zero(t, pct)
		assert.Zero(t, e.ActiveVUs())
	})
	t.Run("zero-duration stage reports no progress", func(t *testing.T) {
		e := NewRamp(RampConfig{Stages: []scenarios.Stage{{}}}, testCollector(t))
		e.stageCurrent.Store(1)
		cur, total, pct := e.StageInfo()
		assert.Equal(t, 1, cur)
		assert.Equal(t, 1, total)
		assert.Zero(t, pct)
	})
	t.Run("start time not yet published", func(t *testing.T) {
		e := NewRamp(RampConfig{Stages: []scenarios.Stage{{Duration: time.Second}}}, testCollector(t))
		e.stageCurrent.Store(1)
		e.stageDurNs.Store(int64(time.Second))
		_, _, pct := e.StageInfo()
		assert.Zero(t, pct)
	})
	t.Run("overrun clamps to 1", func(t *testing.T) {
		e := NewRamp(RampConfig{Stages: []scenarios.Stage{{Duration: time.Second}}}, testCollector(t))
		e.stageCurrent.Store(1)
		e.stageDurNs.Store(int64(time.Second))
		e.stageStartedAt.Store(time.Now().Add(-time.Hour))
		_, _, pct := e.StageInfo()
		assert.Equal(t, 1.0, pct)
	})
	t.Run("midway reports fraction", func(t *testing.T) {
		e := NewRamp(RampConfig{Stages: []scenarios.Stage{{Duration: time.Hour}}}, testCollector(t))
		e.stageCurrent.Store(1)
		e.stageDurNs.Store(int64(time.Hour))
		e.stageStartedAt.Store(time.Now().Add(-30 * time.Minute))
		_, _, pct := e.StageInfo()
		assert.InDelta(t, 0.5, pct, 0.01)
	})
}

// --- request rendering ------------------------------------------------------

func strPtr(s string) *string { return &s }

func TestRenderRequest(t *testing.T) {
	vc := &scenarios.VarContext{
		Vars:     map[string]string{"host": "api.test", "user": "alice", "pass": "s3cret"},
		Captures: map[string]string{"token": "tok-123", "trace": "abc"},
		Data:     map[string]string{"sku": "SKU-9"},
	}

	tests := []struct {
		name string
		step RampStep
		want protocols.Request
	}{
		{
			name: "plain request has no headers and no body",
			step: RampStep{Request: protocols.Request{Method: "GET", URL: "http://api.test/health"}},
			want: protocols.Request{Method: "GET", URL: "http://api.test/health"},
		},
		{
			// Executors only read headers, so without auth nothing is allocated —
			// and the step's own (shared) empty map is never handed out either.
			name: "empty template without auth yields no header map",
			step: RampStep{Request: protocols.Request{Method: "GET", URL: "http://x", Headers: map[string]string{}}},
			want: protocols.Request{Method: "GET", URL: "http://x"},
		},
		{
			name: "templates resolve in url, headers and body",
			step: RampStep{Request: protocols.Request{
				Method:  "POST",
				URL:     "http://{{vars.host}}/items/{{data.sku}}",
				Headers: map[string]string{"X-Trace": "{{capture.trace}}"},
				Body:    []byte(`{"sku":"{{data.sku}}"}`),
			}},
			want: protocols.Request{
				Method:  "POST",
				URL:     "http://api.test/items/SKU-9",
				Headers: map[string]string{"X-Trace": "abc"},
				Body:    []byte(`{"sku":"SKU-9"}`),
			},
		},
		{
			name: "sse stream flag",
			step: RampStep{Request: protocols.Request{Method: "GET", URL: "http://x"}, Stream: "sse"},
			want: protocols.Request{Method: "GET", URL: "http://x", Stream: true},
		},
		{
			name: "unknown stream kind is not streamed",
			step: RampStep{Request: protocols.Request{Method: "GET", URL: "http://x"}, Stream: "ndjson"},
			want: protocols.Request{Method: "GET", URL: "http://x"},
		},
		{
			name: "bearer auth from capture",
			step: RampStep{
				Request: protocols.Request{Method: "GET", URL: "http://x"},
				Auth:    &scenarios.Auth{Bearer: strPtr("{{capture.token}}")},
			},
			want: protocols.Request{Method: "GET", URL: "http://x", Headers: map[string]string{"Authorization": "Bearer tok-123"}},
		},
		{
			name: "basic auth from vars",
			step: RampStep{
				Request: protocols.Request{Method: "GET", URL: "http://x"},
				Auth:    &scenarios.Auth{Basic: &scenarios.BasicAuth{Username: "{{vars.user}}", Password: "{{vars.pass}}"}},
			},
			want: protocols.Request{Method: "GET", URL: "http://x", Headers: map[string]string{"Authorization": "Basic YWxpY2U6czNjcmV0"}},
		},
		{
			name: "auth block with neither kind adds nothing",
			step: RampStep{Request: protocols.Request{Method: "GET", URL: "http://x"}, Auth: &scenarios.Auth{}},
			want: protocols.Request{Method: "GET", URL: "http://x"},
		},
		{
			// Current behaviour: the parser does not reject a step declaring both.
			name: "bearer wins when both auth kinds are set",
			step: RampStep{
				Request: protocols.Request{Method: "GET", URL: "http://x"},
				Auth: &scenarios.Auth{
					Bearer: strPtr("t"),
					Basic:  &scenarios.BasicAuth{Username: "u", Password: "p"},
				},
			},
			want: protocols.Request{Method: "GET", URL: "http://x", Headers: map[string]string{"Authorization": "Bearer t"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderRequest(tt.step, vc)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The step template is shared by every VU, so rendering must never write auth
// into the template's own header map — whatever its shape. The empty non-nil
// shape is what scenario conversion used to produce for steps without headers.
func TestRenderRequest_AuthNeverWritesIntoTemplateHeaders(t *testing.T) {
	templates := map[string]func() map[string]string{
		"nil":       func() map[string]string { return nil },
		"empty":     func() map[string]string { return map[string]string{} },
		"non-empty": func() map[string]string { return map[string]string{"Accept": "application/json"} },
	}
	auths := map[string]struct {
		auth *scenarios.Auth
		want string
	}{
		"bearer": {&scenarios.Auth{Bearer: strPtr("t")}, "Bearer t"},
		"basic":  {&scenarios.Auth{Basic: &scenarios.BasicAuth{Username: "u", Password: "p"}}, "Basic dTpw"},
	}
	for tmplName, newTmpl := range templates {
		for authName, a := range auths {
			t.Run(tmplName+"/"+authName, func(t *testing.T) {
				tmpl := newTmpl()
				before := maps.Clone(tmpl)
				step := RampStep{
					Request: protocols.Request{Method: "GET", URL: "http://x", Headers: tmpl},
					Auth:    a.auth,
				}

				got, err := renderRequest(step, &scenarios.VarContext{})
				require.NoError(t, err)

				assert.Equal(t, a.want, got.Headers["Authorization"])
				assert.Equal(t, before, tmpl, "the shared template must be left untouched")
			})
		}
	}
}

func TestRenderRequest_Errors(t *testing.T) {
	missing := "{{vars.missing}}"
	tests := []struct {
		name string
		step RampStep
	}{
		{"url", RampStep{Request: protocols.Request{URL: missing}}},
		{"header", RampStep{Request: protocols.Request{URL: "http://x", Headers: map[string]string{"X": missing}}}},
		{"body", RampStep{Request: protocols.Request{URL: "http://x", Body: []byte(missing)}}},
		{"bearer", RampStep{Request: protocols.Request{URL: "http://x"}, Auth: &scenarios.Auth{Bearer: strPtr(missing)}}},
		{"basic username", RampStep{Request: protocols.Request{URL: "http://x"},
			Auth: &scenarios.Auth{Basic: &scenarios.BasicAuth{Username: missing, Password: "p"}}}},
		{"basic password", RampStep{Request: protocols.Request{URL: "http://x"},
			Auth: &scenarios.Auth{Basic: &scenarios.BasicAuth{Username: "u", Password: missing}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderRequest(tt.step, &scenarios.VarContext{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "vars.missing")
			assert.Equal(t, protocols.Request{}, got, "a failed render must not return a partial request")
		})
	}
}

func TestBasicAuthHeader(t *testing.T) {
	tests := []struct{ user, pass, want string }{
		{"Aladdin", "open sesame", "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ=="}, // RFC 7617 §2 example
		{"", "", "Basic Og=="},
		{"a", "b:c", "Basic YTpiOmM="}, // only the first colon separates user from password
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, basicAuthHeader(tt.user, tt.pass))
	}
}

// --- response captures ------------------------------------------------------

func TestApplyCaptures(t *testing.T) {
	result := protocols.Result{
		Body:            []byte(`{"data":{"token":"jwt-1"},"items":[{"id":"i0"},{"id":"i1"}],"html":"x"} order=4711 ref=AB-99`),
		ResponseHeaders: map[string]string{"X-Request-Id": "req-7"},
		RawSetCookies: []string{
			"theme=dark; Path=/",
			"=broken",
			"session=sess-42; HttpOnly; Secure",
		},
	}

	tests := []struct {
		name     string
		values   map[string]string
		compiled map[string]*regexp.Regexp
		want     map[string]string
	}{
		{"json nested", map[string]string{"tok": "$.data.token"}, nil, map[string]string{"tok": "jwt-1"}},
		{"json array index", map[string]string{"id": "$.items[1].id"}, nil, map[string]string{"id": "i1"}},
		{"header lookup is canonicalised", map[string]string{"rid": "header:x-request-id"}, nil, map[string]string{"rid": "req-7"}},
		{"cookie skips malformed entries", map[string]string{"s": "cookie:session"}, nil, map[string]string{"s": "sess-42"}},
		{"cookie absent leaves key unset", map[string]string{"s": "cookie:missing"}, nil, map[string]string{}},
		{"regex compiled on the fly", map[string]string{"o": `regex:order=(\d+)`}, nil, map[string]string{"o": "4711"}},
		{"regex no match leaves key unset", map[string]string{"o": `regex:nope=(\d+)`}, nil, map[string]string{}},
		{"regex without group leaves key unset", map[string]string{"o": `regex:order=\d+`}, nil, map[string]string{}},
		// Current behaviour: invalid capture patterns are not rejected at parse time
		// (precompileRegexes drops them silently), so the capture just never fires.
		{"invalid regex is ignored", map[string]string{"o": `regex:([`}, nil, map[string]string{}},
		{
			// The hot loop must use the precompiled pattern rather than recompiling:
			// the cache entry deliberately differs from the raw pattern to prove it.
			name:     "precompiled regex takes precedence",
			values:   map[string]string{"r": `regex:ref=([A-Z]+-\d+)`},
			compiled: map[string]*regexp.Regexp{`ref=([A-Z]+-\d+)`: regexp.MustCompile(`ref=([A-Z]+)`)},
			want:     map[string]string{"r": "AB"},
		},
		{
			name:   "several captures in one step",
			values: map[string]string{"tok": "$.data.token", "rid": "header:X-Request-Id", "s": "cookie:theme"},
			want:   map[string]string{"tok": "jwt-1", "rid": "req-7", "s": "dark"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vc := &scenarios.VarContext{Captures: map[string]string{}}
			applyCaptures(&scenarios.Capture{Values: tt.values}, tt.compiled, result, vc)
			assert.Equal(t, tt.want, vc.Captures)
		})
	}
}

func TestApplyCaptures_NilCaptureIsNoop(t *testing.T) {
	vc := &scenarios.VarContext{Captures: map[string]string{"keep": "v"}}
	applyCaptures(nil, nil, protocols.Result{Body: []byte(`{"a":1}`)}, vc)
	assert.Equal(t, map[string]string{"keep": "v"}, vc.Captures)
}

// --- setup/teardown single-step execution -----------------------------------

type scriptedExec struct {
	calls  atomic.Int32
	result protocols.Result
}

func (s *scriptedExec) Execute(_ context.Context, _ protocols.Request) protocols.Result {
	s.calls.Add(1)
	return s.result
}

func TestExecuteSingleStep(t *testing.T) {
	capture := &scenarios.Capture{Values: map[string]string{"tok": "$.token"}}
	body := []byte(`{"token":"t-1"}`)

	t.Run("render error skips the request", func(t *testing.T) {
		exec := &scriptedExec{}
		vc := &scenarios.VarContext{Captures: map[string]string{}}
		e := NewRamp(RampConfig{}, testCollector(t))

		e.executeSingleStep(context.Background(),
			RampStep{Request: protocols.Request{URL: "{{vars.missing}}"}, Capture: capture}, vc, exec)

		assert.Zero(t, exec.calls.Load())
		assert.Empty(t, vc.Captures)
	})
	t.Run("success applies captures", func(t *testing.T) {
		exec := &scriptedExec{result: protocols.Result{StatusCode: 200, Body: body}}
		vc := &scenarios.VarContext{Captures: map[string]string{}}
		e := NewRamp(RampConfig{}, testCollector(t))

		e.executeSingleStep(context.Background(),
			RampStep{Request: protocols.Request{URL: "http://x"}, Capture: capture}, vc, exec)

		assert.Equal(t, int32(1), exec.calls.Load())
		assert.Equal(t, map[string]string{"tok": "t-1"}, vc.Captures)
	})
	t.Run("transport error skips captures", func(t *testing.T) {
		exec := &scriptedExec{result: protocols.Result{Error: errors.New("connection refused"), Body: body}}
		vc := &scenarios.VarContext{Captures: map[string]string{}}
		e := NewRamp(RampConfig{}, testCollector(t))

		e.executeSingleStep(context.Background(),
			RampStep{Request: protocols.Request{URL: "http://x"}, Capture: capture}, vc, exec)

		assert.Equal(t, int32(1), exec.calls.Load())
		assert.Empty(t, vc.Captures)
	})
}

// BenchmarkRenderRequest covers the header shapes renderRequest sees on the hot
// path; "empty" is what scenario files produce for steps without headers.
func BenchmarkRenderRequest(b *testing.B) {
	bearer := &scenarios.Auth{Bearer: strPtr("{{capture.token}}")}
	vc := &scenarios.VarContext{Captures: map[string]string{"token": "tok"}}
	cases := []struct {
		name    string
		headers map[string]string
		auth    *scenarios.Auth
	}{
		{"nil", nil, nil},
		{"empty", map[string]string{}, nil},
		{"empty+bearer", map[string]string{}, bearer},
		{"one+bearer", map[string]string{"Accept": "application/json"}, bearer},
	}
	for _, c := range cases {
		step := RampStep{
			Request: protocols.Request{Method: "GET", URL: "http://x/items", Headers: c.headers},
			Auth:    c.auth,
		}
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := renderRequest(step, vc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkIsCircuitTripped is the per-request breaker check every VU (and the
// rate dispatcher) makes; RunParallel mirrors many VUs polling at once.
func BenchmarkIsCircuitTripped(b *testing.B) {
	col := metrics.NewCollector(1)
	b.Cleanup(func() { col.Stop() })
	e := NewRamp(RampConfig{CircuitBreaker: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: 1 << 10}}, col)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if e.isCircuitTripped() {
				b.Error("breaker tripped without failures")
				return
			}
		}
	})
}
