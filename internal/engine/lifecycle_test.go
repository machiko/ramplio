package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authServer is a tiny login/api/logout target that records every request path
// in arrival order, and how many /api calls carried the wrong bearer token.
type authServer struct {
	*httptest.Server
	mu      sync.Mutex
	paths   []string
	badAuth atomic.Int64
}

func newAuthServer(t *testing.T, token string) *authServer {
	t.Helper()
	as := &authServer{}
	as.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		as.mu.Lock()
		as.paths = append(as.paths, r.URL.Path)
		as.mu.Unlock()
		switch {
		case r.URL.Path == "/login":
			w.Header().Set("X-User-Id", "42")
			_, _ = w.Write([]byte(`{"token":"` + token + `"}`))
		case strings.HasPrefix(r.URL.Path, "/api"), r.URL.Path == "/logout":
			if r.Header.Get("Authorization") != "Bearer "+token {
				as.badAuth.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(as.Close)
	return as
}

func (as *authServer) log() []string {
	as.mu.Lock()
	defer as.mu.Unlock()
	return append([]string(nil), as.paths...)
}

func (as *authServer) count(path string) int {
	n := 0
	for _, p := range as.log() {
		if p == path {
			n++
		}
	}
	return n
}

func loginStep(url string) engine.RampStep {
	return engine.RampStep{
		Name:    "login",
		Request: protocols.Request{Method: http.MethodPost, URL: url + "/login"},
		Capture: &scenarios.Capture{Values: map[string]string{"token": "$.token", "uid": "header:x-user-id"}},
	}
}

func bearerStep(name, url string) engine.RampStep {
	tok := "{{capture.token}}"
	return engine.RampStep{
		Name:    name,
		Request: protocols.Request{Method: http.MethodGet, URL: url},
		Auth:    &scenarios.Auth{Bearer: &tok},
	}
}

func httpExec() protocols.Executor {
	return protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
}

// testCollector returns a collector whose aggregator goroutine is stopped when
// the test ends (Stop is idempotent, so tests that call Run are fine too).
func testCollector(t *testing.T, maxVUs int) *metrics.Collector {
	t.Helper()
	c := metrics.NewCollector(maxVUs)
	t.Cleanup(func() { c.Stop() })
	return c
}

// holdStages runs target VUs for hold. The 10ms lead-in matters: a single
// linear stage only starts a VU once round(target*progress) >= 1 — for target 1
// that is mid-stage — so most of a short single stage would carry no traffic.
func holdStages(target int, hold time.Duration) []scenarios.Stage {
	return []scenarios.Stage{{Duration: 10 * time.Millisecond, Target: target}, {Duration: hold, Target: target}}
}

// --- RunSetup / RunTeardown (the coordinator's distributed lifecycle hooks) ---

func TestRunSetup_NoStepsReturnsEmptyMap(t *testing.T) {
	exec := &countingExec{}
	eng := engine.NewRamp(engine.RampConfig{Executor: exec}, testCollector(t, 1))

	got := eng.RunSetup(context.Background())

	assert.NotNil(t, got, "callers range over and broadcast the result; it must not be nil")
	assert.Empty(t, got)
	assert.Zero(t, exec.calls.Load())
}

func TestRunSetup_ChainsCapturesAcrossSteps(t *testing.T) {
	srv := newAuthServer(t, "abc")
	col := testCollector(t, 1)
	eng := engine.NewRamp(engine.RampConfig{
		SetupSteps: []engine.RampStep{
			loginStep(srv.URL),
			bearerStep("whoami", srv.URL+"/api/whoami"), // needs the token captured by the step before
		},
		Executor: httpExec(),
	}, col)

	got := eng.RunSetup(context.Background())

	assert.Equal(t, map[string]string{"token": "abc", "uid": "42"}, got)
	assert.Equal(t, []string{"/login", "/api/whoami"}, srv.log())
	assert.Zero(t, srv.badAuth.Load(), "the second setup step must see the first step's capture")
	assert.Zero(t, col.Stop().Total, "setup traffic is not part of the measured load")
}

// Current behaviour, not a design endorsement: RunSetup has no error channel, so
// a setup step that cannot render is skipped silently and the rest still run
// (any later step needing its capture then fails at render time instead).
func TestRunSetup_UnrenderableStepIsSkipped(t *testing.T) {
	srv := newAuthServer(t, "abc")
	eng := engine.NewRamp(engine.RampConfig{
		SetupSteps: []engine.RampStep{
			{Request: protocols.Request{Method: http.MethodGet, URL: srv.URL + "/{{vars.missing}}"}},
			loginStep(srv.URL),
		},
		Executor: httpExec(),
	}, testCollector(t, 1))

	got := eng.RunSetup(context.Background())

	assert.Equal(t, "abc", got["token"])
	assert.Equal(t, []string{"/login"}, srv.log())
}

func TestRunTeardown_NoStepsDoesNothing(t *testing.T) {
	exec := &countingExec{}
	eng := engine.NewRamp(engine.RampConfig{Executor: exec}, testCollector(t, 1))

	eng.RunTeardown(context.Background(), map[string]string{"token": "x"})

	assert.Zero(t, exec.calls.Load())
}

func TestRunTeardown_UsesSeedCaptures(t *testing.T) {
	srv := newAuthServer(t, "seeded")
	eng := engine.NewRamp(engine.RampConfig{
		TeardownSteps: []engine.RampStep{bearerStep("logout", srv.URL+"/logout")},
		Executor:      httpExec(),
	}, testCollector(t, 1))

	eng.RunTeardown(context.Background(), map[string]string{"token": "seeded"})

	assert.Equal(t, []string{"/logout"}, srv.log())
	assert.Zero(t, srv.badAuth.Load(), "teardown must authenticate with the seeded token")
}

// The distributed coordinator calls RunTeardown with the run's context, which
// is already cancelled after Ctrl-C; cleanup must still go out.
func TestRunTeardown_AfterCancelStillSends(t *testing.T) {
	srv := newAuthServer(t, "seeded")
	eng := engine.NewRamp(engine.RampConfig{
		TeardownSteps: []engine.RampStep{bearerStep("logout", srv.URL+"/logout")},
		Executor:      httpExec(),
	}, testCollector(t, 1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	eng.RunTeardown(ctx, map[string]string{"token": "seeded"})

	assert.Equal(t, 1, srv.count("/logout"))
	assert.Zero(t, srv.badAuth.Load())
}

func TestRunTeardown_NilSeed(t *testing.T) {
	exec := &countingExec{}
	eng := engine.NewRamp(engine.RampConfig{
		TeardownSteps: []engine.RampStep{rampStep("http://unused.test/logout")},
		Executor:      exec,
	}, testCollector(t, 1))

	eng.RunTeardown(context.Background(), nil)

	assert.Equal(t, int32(1), exec.calls.Load())
}

// --- Run lifecycle ------------------------------------------------------------

func TestRun_SetupBeforeLoadAndTeardownAfter(t *testing.T) {
	const vus = 3
	srv := newAuthServer(t, "abc")
	eng := engine.NewRamp(engine.RampConfig{
		Stages:        holdStages(vus, 300*time.Millisecond),
		SetupSteps:    []engine.RampStep{loginStep(srv.URL)},
		Steps:         []engine.RampStep{bearerStep("api", srv.URL+"/api/items")},
		TeardownSteps: []engine.RampStep{bearerStep("logout", srv.URL+"/logout")},
		Executor:      httpExec(),
	}, testCollector(t, vus))

	sum := eng.Run(context.Background())

	log := srv.log()
	require.NotEmpty(t, log)
	assert.Equal(t, "/login", log[0], "setup must complete before any VU traffic")
	assert.Equal(t, 1, srv.count("/login"))
	require.Equal(t, 1, srv.count("/logout"))
	// A VU request cancelled at shutdown may still reach the handler after
	// teardown's /logout (client-side cancel vs server-side arrival) — at most
	// one per VU. Anything else after /logout means teardown ran too early.
	tail := log[slices.Index(log, "/logout")+1:]
	assert.LessOrEqual(t, len(tail), vus)
	for _, p := range tail {
		assert.Equal(t, "/api/items", p)
	}
	assert.Zero(t, srv.badAuth.Load(), "every VU must inherit the setup token")

	assert.Positive(t, sum.Total)
	assert.Zero(t, sum.Errors)
	require.Len(t, sum.Steps, 1, "setup/teardown must not appear in step metrics")
	assert.Equal(t, "api", sum.Steps[0].Name)
}

// Ctrl-C in the CLI and Stop in the dashboard both cancel the run context.
// Teardown cleans up what setup created (log out, delete test data), so it must
// still send its requests after the cancel — seeded with the setup captures.
func TestRun_TeardownRunsAfterCancel(t *testing.T) {
	srv := newAuthServer(t, "abc")
	eng := engine.NewRamp(engine.RampConfig{
		Stages:        holdStages(2, 30*time.Second), // cut short by the cancel
		SetupSteps:    []engine.RampStep{loginStep(srv.URL)},
		Steps:         []engine.RampStep{bearerStep("api", srv.URL+"/api/items")},
		TeardownSteps: []engine.RampStep{bearerStep("logout", srv.URL+"/logout")},
		Executor:      httpExec(),
	}, testCollector(t, 2))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { // cancel mid-test, once load is flowing
		deadline := time.Now().Add(5 * time.Second)
		for srv.count("/api/items") == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	sum := eng.Run(ctx)

	assert.Less(t, time.Since(start), 10*time.Second, "cancel must end the run promptly")
	assert.Positive(t, sum.Total)
	assert.Zero(t, sum.Errors, "requests cut off by the cancel itself are not samples, let alone errors")
	assert.Equal(t, 1, srv.count("/logout"), "teardown must still run after a cancel")
	assert.Zero(t, srv.badAuth.Load(), "teardown must carry the setup token")
}

// In distributed mode the coordinator broadcasts setup results as SeedCaptures;
// a worker that also has its own setup steps must merge rather than replace them.
func TestRun_SeedCapturesMergeWithSetupAndReachVUs(t *testing.T) {
	srv := newAuthServer(t, "abc")
	seed := map[string]string{"tenant": "t1"}
	eng := engine.NewRamp(engine.RampConfig{
		Stages:       holdStages(2, 300*time.Millisecond),
		SetupSteps:   []engine.RampStep{loginStep(srv.URL)},
		Steps:        []engine.RampStep{bearerStep("api", srv.URL+"/api/{{capture.tenant}}")},
		SeedCaptures: seed,
		Executor:     httpExec(),
	}, testCollector(t, 2))

	sum := eng.Run(context.Background())

	assert.Positive(t, srv.count("/api/t1"), "seeded capture must reach VU templates")
	assert.Zero(t, srv.badAuth.Load(), "setup capture must reach VUs alongside the seed")
	assert.Zero(t, sum.Errors)
	assert.Equal(t, map[string]string{"tenant": "t1"}, seed, "the caller's seed map must not be mutated")
}

func TestRun_SeedCapturesWithoutSetup(t *testing.T) {
	srv := newAuthServer(t, "from-coordinator")
	eng := engine.NewRamp(engine.RampConfig{
		Stages:       holdStages(1, 300*time.Millisecond),
		Steps:        []engine.RampStep{bearerStep("api", srv.URL+"/api/x")},
		SeedCaptures: map[string]string{"token": "from-coordinator"},
		Executor:     httpExec(),
	}, testCollector(t, 1))

	sum := eng.Run(context.Background())

	assert.Positive(t, sum.Total)
	assert.Zero(t, srv.badAuth.Load())
}

// Regression for a shipped bug: the scenario conversion used to give every step
// without headers an *empty non-nil* map, shared by all VUs, and auth used
// to be written into it — a concurrent map write (-race reports it; the runtime
// may abort with "concurrent map writes"). The sharedHeaders assertion catches a
// regression deterministically. The mismatch check guards per-VU token
// isolation; under the old bug a cross-VU token only leaked in the brief window
// before the template's first write, so it rarely fired on its own.
func TestRun_PerVUBearerTokensWithSharedEmptyHeaders(t *testing.T) {
	const vus = 8
	var (
		mu         sync.Mutex
		nextVU     int
		mismatches atomic.Int64
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var vu string
		c, err := r.Cookie("vu")
		if err == nil {
			vu = c.Value
		} else {
			mu.Lock()
			nextVU++
			vu = strconv.Itoa(nextVU)
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "vu", Value: vu, Path: "/"})
		}
		if r.URL.Path == "/login" {
			_, _ = w.Write([]byte(`{"token":"tok-` + vu + `"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok-"+vu {
			mismatches.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sharedHeaders := map[string]string{} // the shape scenario conversion used to produce
	api := bearerStep("api", srv.URL+"/api")
	api.Request.Headers = sharedHeaders
	login := loginStep(srv.URL)
	login.Request.Headers = map[string]string{}

	eng := engine.NewRamp(engine.RampConfig{
		Stages:   holdStages(vus, 300*time.Millisecond),
		Steps:    []engine.RampStep{login, api},
		Executor: httpExec(),
	}, testCollector(t, vus))

	sum := eng.Run(context.Background())

	assert.Empty(t, sharedHeaders, "auth must never be written into the shared step template")
	assert.Zero(t, mismatches.Load(), "a VU sent another VU's token")
	assert.Positive(t, sum.Total)
	assert.Zero(t, sum.Errors)
}

// Each VU draws one data row when it starts and keeps it for its lifetime. The
// server tells VUs apart by a cookie (every VU has its own cookie jar), so it can
// check both halves: each VU sticks to one row, and the rows are all distinct.
func TestRun_DataRowsAssignedPerVU(t *testing.T) {
	var (
		mu       sync.Mutex
		nextVU   int
		rowsByVU = map[string]map[string]bool{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var vu string
		c, err := r.Cookie("vu")
		mu.Lock()
		if err == nil {
			vu = c.Value
		} else {
			nextVU++
			vu = strconv.Itoa(nextVU)
		}
		if rowsByVU[vu] == nil {
			rowsByVU[vu] = map[string]bool{}
		}
		rowsByVU[vu][strings.TrimPrefix(r.URL.Path, "/user/")] = true
		mu.Unlock()
		if err != nil {
			http.SetCookie(w, &http.Cookie{Name: "vu", Value: vu, Path: "/"})
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	eng := engine.NewRamp(engine.RampConfig{
		Stages:   holdStages(3, 300*time.Millisecond),
		Steps:    []engine.RampStep{rampStep(srv.URL + "/user/{{data.id}}")},
		DataRows: []map[string]string{{"id": "1"}, {"id": "2"}, {"id": "3"}},
		Executor: httpExec(),
	}, testCollector(t, 3))

	sum := eng.Run(context.Background())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, rowsByVU, 3, "expected one cookie identity per VU")
	used := map[string]bool{}
	for vu, rows := range rowsByVU {
		require.Len(t, rows, 1, "VU %s must keep a single row, got %v", vu, rows)
		for row := range rows {
			used[row] = true
		}
	}
	assert.Equal(t, map[string]bool{"1": true, "2": true, "3": true}, used)
	assert.Zero(t, sum.Errors)
}

// ActiveVUs/StageInfo are polled concurrently by the TUI and dashboard while the
// engine runs; this exercises that exact access pattern (meaningful under -race).
func TestRun_LiveReadoutsDuringRun(t *testing.T) {
	srv := okServer(t)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{
			{Duration: time.Second, Target: 3},
			{Duration: 30 * time.Second, Target: 3}, // cut short by cancel below
		},
		Steps:    []engine.RampStep{rampStep(srv.URL)},
		Executor: httpExec(),
	}, testCollector(t, 3))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.Run(ctx)
	}()

	assert.Eventually(t, func() bool {
		cur, total, pct := eng.StageInfo()
		return cur == 1 && total == 2 && pct >= 0 && pct <= 1
	}, 3*time.Second, 5*time.Millisecond, "stage 1 of 2 must be visible while it runs")
	assert.Eventually(t, func() bool {
		cur, _, _ := eng.StageInfo()
		return cur == 2 && eng.ActiveVUs() == 3
	}, 5*time.Second, 5*time.Millisecond, "the hold stage must report all target VUs active")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
	assert.Zero(t, eng.ActiveVUs(), "every VU goroutine must have exited")
}

func TestRun_ReadoutsAfterCompletion(t *testing.T) {
	srv := okServer(t)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   holdStages(2, 50*time.Millisecond),
		Steps:    []engine.RampStep{rampStep(srv.URL)},
		Executor: httpExec(),
	}, testCollector(t, 2))

	eng.Run(context.Background())

	cur, total, pct := eng.StageInfo()
	assert.Equal(t, 2, cur)
	assert.Equal(t, 2, total)
	assert.Equal(t, 1.0, pct, "a finished stage reports full progress")
	assert.Zero(t, eng.ActiveVUs())
}

// --- request timeouts -----------------------------------------------------------

// http.Client.Timeout errors satisfy errors.Is(err, context.DeadlineExceeded),
// the same test the engine uses to detect a cancelled run. A slow target must
// be *reported* as timeouts — the most important signal a load test has — and
// the VUs must keep going, not silently exit one by one.
func TestRun_RequestTimeoutIsReportedAndVUsSurvive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	cfg := protocols.DefaultHTTPConfig()
	cfg.RequestTimeout = 50 * time.Millisecond

	eng := engine.NewRamp(engine.RampConfig{
		Stages:   holdStages(2, 600*time.Millisecond),
		Steps:    []engine.RampStep{rampStep(srv.URL)},
		Executor: protocols.NewHTTPExecutor(cfg),
	}, testCollector(t, 2))

	done := make(chan metrics.Summary, 1)
	go func() { done <- eng.Run(context.Background()) }()
	time.Sleep(400 * time.Millisecond) // several timeouts in
	assert.Equal(t, 2, eng.ActiveVUs(), "VUs must survive request timeouts")
	sum := <-done

	assert.GreaterOrEqual(t, sum.Total, int64(8), "each timeout is a recorded sample (2 VUs × ~12 per 600ms)")
	assert.Equal(t, sum.Total, sum.Errors)
	assert.Equal(t, sum.Errors, sum.ErrorBreakdown[metrics.ErrKindTimeout], "timeouts must be classified as timeouts")
}

// --- circuit breaker ----------------------------------------------------------
//
// The first two use a single VU on purpose: with one goroutine the trip point is
// exact, so the assertions are deterministic rather than timing-dependent. The
// multi-VU test that follows bounds the overshoot instead (the rate-mode breaker
// test lives in ramp_pool_test.go, where it can read the worker pool size).

func failingServer(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustBe200(url string) engine.RampStep {
	return engine.RampStep{
		Request:    protocols.Request{Method: http.MethodGet, URL: url},
		Assertions: &scenarios.Assertions{Status: scenarios.StatusExact(200)},
	}
}

func TestRun_CircuitBreakerStopsTraffic(t *testing.T) {
	const threshold = 5
	var hits atomic.Int64
	srv := failingServer(t, &hits)

	eng := engine.NewRamp(engine.RampConfig{
		Stages:         holdStages(1, 30*time.Second), // cut short by cancel below
		Steps:          []engine.RampStep{mustBe200(srv.URL)},
		CircuitBreaker: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: threshold, WindowSeconds: 10},
		Executor:       httpExec(),
	}, testCollector(t, 1))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan metrics.Summary, 1)
	go func() { done <- eng.Run(ctx) }()

	// The VU stops right after the threshold-th failure, while the stage still runs.
	assert.Eventually(t, func() bool {
		return hits.Load() == threshold && eng.ActiveVUs() == 0
	}, 3*time.Second, 5*time.Millisecond, "the tripped VU must exit (the README's active-VU cliff)")

	cancel()
	var sum metrics.Summary
	select {
	case sum = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
	assert.Equal(t, int64(threshold), hits.Load(), "no requests may be sent after the breaker trips")
	assert.Equal(t, int64(threshold), sum.Total)
	assert.Equal(t, int64(threshold), sum.Errors)
}

func TestRun_CircuitBreakerSuccessResetsWindow(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	eng := engine.NewRamp(engine.RampConfig{
		Stages: holdStages(1, 300*time.Millisecond),
		Steps: []engine.RampStep{{
			Request:    protocols.Request{Method: http.MethodGet, URL: srv.URL},
			Assertions: &scenarios.Assertions{Status: scenarios.StatusExact(200)},
		}},
		// Two failures in 10s would trip — but every failure is followed by a success.
		CircuitBreaker: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: 2, WindowSeconds: 10},
		Executor:       httpExec(),
	}, testCollector(t, 1))

	sum := eng.Run(context.Background())

	// Without the reset the 2nd failure (request 3) would trip and stop the VU.
	assert.Greater(t, n.Load(), int64(6), "alternating failures must not trip the breaker")
	assert.Positive(t, sum.Errors)
	assert.Less(t, sum.Errors, sum.Total)
}

// Many VUs record failures and poll the breaker concurrently (meaningful under
// -race). Each VU checks the breaker before every step, so once it trips at most
// one in-flight request per VU can still land. That bound assumes a single-shot
// step: the breaker is not re-checked between Loop iterations.
func TestRun_CircuitBreakerManyVUs(t *testing.T) {
	const vus, threshold = 8, 20
	var hits atomic.Int64
	srv := failingServer(t, &hits)

	eng := engine.NewRamp(engine.RampConfig{
		Stages:         holdStages(vus, 30*time.Second), // cut short by cancel below
		Steps:          []engine.RampStep{mustBe200(srv.URL)},
		CircuitBreaker: &scenarios.CircuitBreakerConfig{ConsecutiveFailures: threshold, WindowSeconds: 10},
		Executor:       httpExec(),
	}, testCollector(t, vus))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan metrics.Summary, 1)
	go func() { done <- eng.Run(ctx) }()

	assert.Eventually(t, func() bool {
		return hits.Load() >= threshold && eng.ActiveVUs() == 0
	}, 3*time.Second, 5*time.Millisecond, "every VU must exit once the breaker trips")

	cancel()
	var sum metrics.Summary
	select {
	case sum = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
	assert.LessOrEqual(t, hits.Load(), int64(threshold+vus))
	assert.GreaterOrEqual(t, sum.Errors, int64(threshold))
}

// --- rate mode worker branches -----------------------------------------------

func rateCfg(step engine.RampStep) engine.RampConfig {
	return engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 300 * time.Millisecond, TargetRPS: 50}},
		Steps:    []engine.RampStep{step},
		Vars:     map[string]string{"flag": "off"},
		Executor: httpExec(),
	}
}

func TestRateMode_PassingAssertionIsNotError(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	ok := `"ok"`
	cfg := rateCfg(engine.RampStep{
		Request:    protocols.Request{Method: http.MethodGet, URL: srv.URL},
		Pause:      time.Millisecond,
		Assertions: &scenarios.Assertions{BodyContains: &ok},
	})
	sum := engine.NewRamp(cfg, testCollector(t, 50)).Run(context.Background())

	assert.Positive(t, sum.Total)
	assert.Zero(t, sum.Errors)
	assert.GreaterOrEqual(t, hits.Load(), sum.Total)
}

// One dispatched job runs the step Loop times. To attribute requests to jobs
// deterministically, each worker's first request claims a unique id (captured
// into its VarContext) and the long Pause parks the worker after its single job.
// So every id must be seen exactly Loop-1 more times.
func TestRateMode_LoopRunsWholeIterationsPerJob(t *testing.T) {
	const loop = 3
	exec := &idClaimExec{}
	cfg := rateCfg(engine.RampStep{
		Request: protocols.Request{Method: http.MethodGet, URL: "{{capture.wid}}"},
		Loop:    loop,
		Pause:   time.Minute, // one job per worker; shutdown cancels the pause
		Capture: &scenarios.Capture{Values: map[string]string{"wid": "$.wid"}},
	})
	cfg.SeedCaptures = map[string]string{"wid": "new"}
	cfg.Executor = exec
	sum := engine.NewRamp(cfg, testCollector(t, 50)).Run(context.Background())

	perJob := exec.reuseCounts()
	require.NotEmpty(t, perJob, "expected at least one dispatched job")
	for id, n := range perJob {
		assert.Equal(t, loop-1, n, "job %s", id)
	}
	assert.Equal(t, int64(loop*len(perJob)), sum.Total)
}

// With one VU the request order is fully deterministic: the looped step repeats
// back-to-back before the next step runs.
func TestRun_LoopRepeatsStepBeforeNextStep(t *testing.T) {
	exec := &recordingExec{}
	eng := engine.NewRamp(engine.RampConfig{
		Stages: holdStages(1, 100*time.Millisecond),
		Steps: []engine.RampStep{
			{Request: protocols.Request{Method: http.MethodGet, URL: "a"}, Loop: 3},
			{Request: protocols.Request{Method: http.MethodGet, URL: "b"}, Loop: 0}, // 0 means once
		},
		Executor: exec,
	}, testCollector(t, 1))

	eng.Run(context.Background())

	runs := strings.Split(strings.Join(exec.urls(), ""), "b")
	require.Greater(t, len(runs), 2, "expected several full iterations")
	for i, run := range runs[:len(runs)-1] { // the last run may be cut by shutdown
		assert.Equal(t, "aaa", run, "iteration %d", i)
	}
}

func TestRateMode_AssertionFailureIsError(t *testing.T) {
	srv := okServer(t)
	want := "never-present"
	cfg := rateCfg(engine.RampStep{
		Request:    protocols.Request{Method: http.MethodGet, URL: srv.URL},
		Assertions: &scenarios.Assertions{BodyContains: &want},
	})
	sum := engine.NewRamp(cfg, testCollector(t, 50)).Run(context.Background())

	assert.Positive(t, sum.Total)
	assert.Equal(t, sum.Total, sum.Errors)
}

// The true-condition case is the control: it proves jobs were dispatched and the
// condition is actually evaluated, so the zero in the false case means "skipped".
func TestRateMode_ConditionGatesStep(t *testing.T) {
	tests := []struct {
		name     string
		cond     string // Vars has flag=off
		wantSent bool
	}{
		{"false condition skips", `vars.flag == "on"`, false},
		{"true condition sends", `vars.flag == "off"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &countingExec{}
			cfg := rateCfg(engine.RampStep{
				Request: protocols.Request{Method: http.MethodGet, URL: "http://unused.test"},
				If:      tt.cond,
			})
			cfg.Executor = exec
			sum := engine.NewRamp(cfg, testCollector(t, 50)).Run(context.Background())

			if tt.wantSent {
				assert.Positive(t, exec.calls.Load())
				assert.Equal(t, int64(exec.calls.Load()), sum.Total)
				return
			}
			assert.Zero(t, exec.calls.Load())
			assert.Zero(t, sum.Total, "a skipped step is neither sent nor recorded")
		})
	}
}

func TestRateMode_RenderErrorRecordedWithoutSending(t *testing.T) {
	exec := &countingExec{}
	cfg := rateCfg(engine.RampStep{
		Name:    "broken",
		Request: protocols.Request{Method: http.MethodGet, URL: "http://unused.test/{{vars.missing}}"},
	})
	cfg.Executor = exec
	sum := engine.NewRamp(cfg, testCollector(t, 50)).Run(context.Background())

	assert.Zero(t, exec.calls.Load())
	assert.Positive(t, sum.Total, "render failures must be visible in the error rate, not dropped")
	assert.Equal(t, sum.Total, sum.Errors)
}

// countingExec is a no-network executor that counts calls and always succeeds.
type countingExec struct{ calls atomic.Int32 }

func (c *countingExec) Execute(_ context.Context, _ protocols.Request) protocols.Result {
	c.calls.Add(1)
	return protocols.Result{StatusCode: http.StatusOK}
}

// recordingExec is a no-network executor that records request URLs in call order.
type recordingExec struct {
	mu   sync.Mutex
	seen []string
}

func (r *recordingExec) Execute(_ context.Context, req protocols.Request) protocols.Result {
	r.mu.Lock()
	r.seen = append(r.seen, req.URL)
	r.mu.Unlock()
	return protocols.Result{StatusCode: http.StatusOK}
}

func (r *recordingExec) urls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// idClaimExec hands out a fresh id when the request URL is "new" and echoes the
// id otherwise, counting how often each claimed id is reused. It ignores ctx so
// in-flight loops are never cut short at shutdown.
type idClaimExec struct {
	mu     sync.Mutex
	next   int
	reused map[string]int
}

func (x *idClaimExec) Execute(_ context.Context, req protocols.Request) protocols.Result {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.reused == nil {
		x.reused = map[string]int{}
	}
	id := req.URL
	if id == "new" {
		x.next++
		id = "w" + strconv.Itoa(x.next)
		x.reused[id] = 0
	} else {
		x.reused[id]++
	}
	return protocols.Result{StatusCode: http.StatusOK, Body: []byte(`{"wid":"` + id + `"}`)}
}

func (x *idClaimExec) reuseCounts() map[string]int {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make(map[string]int, len(x.reused))
	for k, v := range x.reused {
		out[k] = v
	}
	return out
}
