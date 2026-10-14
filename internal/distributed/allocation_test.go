package distributed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cluster as a whole must generate the load the scenario declares — not
// that load once per worker. These run a real coordinator against real worker
// HTTP servers and count what the target actually receives.

// runCluster starts n workers, runs yaml through a coordinator and returns the
// merged summary.
func runCluster(t *testing.T, yaml string, n int) metrics.Summary {
	t.Helper()
	coord := newTestCoordinator(t, startWorkers(t, n), yaml)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sum, err := coord.Run(ctx)
	require.NoError(t, err)
	return sum
}

func newTestCoordinator(t *testing.T, addrs []string, yaml string) *Coordinator {
	t.Helper()
	sc, err := scenarios.Parse(bytes.NewReader([]byte(yaml)))
	require.NoError(t, err)
	coord := NewCoordinator(addrs, []byte(yaml), sc, protocols.DefaultHTTPConfig())
	coord.SetTiming(50*time.Millisecond, 0)
	return coord
}

// startWorkers starts n real worker HTTP servers and returns their addresses.
// httptest picks the ports itself (no free-port probing to race against other
// packages' listeners) and closes the servers when the test ends.
func startWorkers(t *testing.T, n int) []string {
	t.Helper()
	addrs := make([]string, n)
	for i := range n {
		srv := httptest.NewServer(NewWorker(fmt.Sprintf("alloc-%d", i)).Handler())
		t.Cleanup(srv.Close)
		addrs[i] = strings.TrimPrefix(srv.URL, "http://")
	}
	return addrs
}

func TestDistributed_RateModeSplitsTargetRPSAcrossWorkers(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	sum := runCluster(t, fmt.Sprintf(`name: rate split
stages:
  - duration: 10ms
    target_rps: 40
  - duration: 1s
    target_rps: 40
steps:
  - name: ping
    method: GET
    url: %s
`, target.URL), 2)

	// 40 RPS for ~1s across the cluster ≈ 40 requests (plus a start-up token
	// per worker). Sending the full rate from every worker would double it.
	t.Logf("cluster sent %d requests", hits.Load())
	// Wide lower bound: a starved limiter never replays missed tokens. The upper
	// bound is what catches a regression (every worker at full rate sent 76).
	assert.GreaterOrEqual(t, hits.Load(), int64(20))
	assert.LessOrEqual(t, hits.Load(), int64(58), "cluster must send the declared RPS once, not once per worker")
	assert.Zero(t, sum.Errors)
}

func TestDistributed_MoreWorkersThanVUsNeverExceedsTarget(t *testing.T) {
	var inFlight, peak atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cur := inFlight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	sum := runCluster(t, fmt.Sprintf(`name: one vu many workers
stages:
  - duration: 10ms
    target: 1
  - duration: 500ms
    target: 1
steps:
  - name: ping
    method: GET
    url: %s
`, target.URL), 3)

	require.Positive(t, sum.Total)
	assert.Equal(t, int32(1), peak.Load(), "a 1-VU scenario must never run more than 1 VU across the cluster")
}

// A worker given an all-zero share idles for the stage durations: the share
// from the coordinator replaces the scenario's own stages.
func TestWorker_UsesAssignedStageShares(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	w := NewWorker("share")
	t.Cleanup(w.Stop)
	require.NoError(t, w.Assign(context.Background(), &AssignRequest{
		ScenarioYAML: []byte(fmt.Sprintf(`name: share
stages:
  - duration: 10ms
    target: 1
  - duration: 200ms
    target: 1
steps:
  - name: ping
    method: GET
    url: %s
`, target.URL)),
		StageShares: []StageShare{{}, {}},
	}))
	require.Eventually(t, func() bool { return w.GetState() == WorkerStateDone },
		5*time.Second, 20*time.Millisecond)

	assert.Zero(t, hits.Load(), "a zero share must idle, not run the scenario's own load")
}

// Shares the worker cannot apply are refused up front, leaving it idle: a
// count mismatch means coordinator and worker disagree on the scenario, and a
// negative VU target would spin the engine's VU loop forever.
func TestWorker_RejectsInvalidStageShares(t *testing.T) {
	yaml := []byte(`name: two stages
stages:
  - duration: 1s
    target: 2
  - duration: 1s
    target: 2
steps:
  - name: ping
    method: GET
    url: http://127.0.0.1:1/
`)
	tests := []struct {
		name    string
		shares  []StageShare
		wantErr string
	}{
		{"count mismatch", []StageShare{{Target: 1}}, "assigned 1 stage shares but the scenario has 2 stages"},
		{"negative target", []StageShare{{Target: 1}, {Target: -1}}, "stage 2 share is negative"},
		{"negative rps", []StageShare{{TargetRPS: -5}, {}}, "stage 1 share is negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWorker("invalid-" + tt.name)
			err := w.Assign(context.Background(), &AssignRequest{ScenarioYAML: yaml, StageShares: tt.shares})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Equal(t, WorkerStateIdle, w.GetState(), "a rejected assignment must leave the worker idle")
		})
	}
}

// A worker that does not advertise stage shares (an older version) would run
// the full scenario load by itself; the coordinator must refuse before
// assigning anything.
func TestCoordinator_RefusesWorkerWithoutStageShares(t *testing.T) {
	var assigns atomic.Int32
	oldWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"ok":true,"message":"healthy"}`))
		case "/assign":
			assigns.Add(1)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(oldWorker.Close)

	yaml := "name: t\nstages:\n  - duration: 1s\n    target: 2\nsteps:\n  - name: p\n    method: GET\n    url: http://127.0.0.1:1/\n"
	sc, err := scenarios.Parse(bytes.NewReader([]byte(yaml)))
	require.NoError(t, err)
	coord := NewCoordinator([]string{strings.TrimPrefix(oldWorker.URL, "http://")}, []byte(yaml), sc, protocols.DefaultHTTPConfig())

	_, err = coord.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support per-worker stage shares")
	assert.Zero(t, assigns.Load(), "no work may be assigned before the capability check passes")
}

func TestWorker_HealthAdvertisesStageShares(t *testing.T) {
	rec := httptest.NewRecorder()
	NewWorker("caps").handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var status StatusResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	assert.Contains(t, status.Capabilities, CapabilityStageShares)
}
