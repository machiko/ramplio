package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/scenarios"
)

// TestRatePool_MaybeGrowRespectsCapAndIdle verifies the grow-on-demand logic in
// isolation: it grows only when no worker is idle, stops at the cap, and flags
// capHit once the ceiling is reached (where the send blocks = real backpressure).
func TestRatePool_MaybeGrowRespectsCapAndIdle(t *testing.T) {
	spawned := 0
	p := &ratePool{max: 3}
	p.spawn = func() { spawned++; p.total.Add(1) }

	// An idle worker is waiting → no need to grow.
	p.idle.Store(1)
	p.maybeGrow()
	if spawned != 0 {
		t.Fatalf("should not grow while a worker is idle, spawned=%d", spawned)
	}

	// No idle worker, below cap → grow on each call up to the cap.
	p.idle.Store(0)
	p.maybeGrow() // total 1
	p.maybeGrow() // total 2
	p.maybeGrow() // total 3 == cap
	if spawned != 3 {
		t.Fatalf("expected growth up to cap (3), spawned=%d", spawned)
	}
	if p.capHit.Load() {
		t.Fatalf("capHit must not be set until growth is attempted past the cap")
	}

	// At the cap → no more growth, capHit set so the verdict can flag it.
	p.maybeGrow()
	if spawned != 3 {
		t.Fatalf("must not grow past the cap, spawned=%d", spawned)
	}
	if !p.capHit.Load() {
		t.Fatalf("capHit should be set once at the cap")
	}
}

// TestRatePool_LowLatencyStaysSmall proves grow-on-demand saves resources: a fast
// target needs only a few workers, so the pool must stay far below the old eager
// maxRPS×5 pre-spawn (250 at 50 RPS) and never hit the cap.
func TestRatePool_LowLatencyStaysSmall(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-dependent rate test skipped in -short mode")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	col := metrics.NewCollector(200)
	eng := NewRamp(RampConfig{
		Stages:   []scenarios.Stage{{Duration: 1500 * time.Millisecond, TargetRPS: 50}},
		Steps:    []RampStep{{Request: protocols.Request{Method: http.MethodGet, URL: s.URL}}},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	if sum.GeneratorWorkerCapHit {
		t.Error("a low-latency target should not hit the worker cap")
	}
	peak := eng.ratePeakWorkers.Load()
	if peak > 50 {
		t.Errorf("peak workers = %d, expected grow-on-demand to stay well below the old eager 250", peak)
	}
	if peak < rateMinWorkers {
		t.Errorf("peak workers = %d, expected at least the minimum pool (%d)", peak, rateMinWorkers)
	}
}
