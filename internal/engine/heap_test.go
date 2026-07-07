package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/machiko/ramplio/v2/internal/engine"
	"github.com/machiko/ramplio/v2/internal/metrics"
	"github.com/machiko/ramplio/v2/internal/protocols"
	"github.com/machiko/ramplio/v2/internal/scenarios"
)

// heapAlloc returns HeapAlloc bytes after running two GC cycles.
func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// runScenario200VUs runs a 3-stage ramp against srv for ~12 seconds total and
// returns the engine summary. It cleans up idle connections before returning.
func runScenario200VUs(srv *httptest.Server) {
	const vus = 200
	exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	col := metrics.NewCollector(vus)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{
			{Duration: 2 * time.Second, Target: vus},
			{Duration: 8 * time.Second, Target: vus},
			{Duration: 2 * time.Second, Target: 0},
		},
		Steps:    []engine.RampStep{{Request: protocols.Request{Method: http.MethodGet, URL: srv.URL}}},
		Executor: exec,
	}, col)
	eng.Run(context.Background())
	exec.CloseIdleConnections()
	time.Sleep(200 * time.Millisecond)
}

// TestHeapGrowth confirms that heap live bytes do not grow monotonically across
// repeated runs. It executes the same scenario twice and asserts that the second
// run's post-GC HeapAlloc is not significantly larger than the first, which
// would indicate a persistent (non-freed) allocation accumulating over time.
//
// For the M6 10,000 VU × 10-minute validation, run manually:
//
//	ramplio mock-server &
//	go test -v -run=NOMATCH -bench=BenchmarkRampEngine ./internal/engine/... -benchtime=10m
//	# then inspect with: go tool pprof http://localhost:6060/debug/pprof/heap
func TestHeapGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("heap growth test skipped in short mode")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Warm-up: establish connection pool and runtime caches.
	runScenario200VUs(srv)

	heap1 := heapAlloc()
	runScenario200VUs(srv)
	heap2 := heapAlloc()

	t.Logf("HeapAlloc — run1-post: %d KB  run2-post: %d KB", heap1/1024, heap2/1024)

	// If heap is growing monotonically (leak), run2 will be substantially larger
	// than run1. Allow a 2× budget for normal runtime variance and pool retention.
	if heap1 > 0 && heap2 > heap1*2 {
		t.Errorf("heap appears to be growing: run1=%d KB run2=%d KB (ratio=%.2f×); possible leak",
			heap1/1024, heap2/1024, float64(heap2)/float64(heap1))
	}
}

// TestRampHeapGrowth is the same check for RampEngine with a larger VU count.
func TestRampHeapGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("heap growth test skipped in short mode")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	run := func() {
		const vus = 500
		exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
		col := metrics.NewCollector(vus)
		eng := engine.NewRamp(engine.RampConfig{
			Stages: []scenarios.Stage{
				{Duration: 3 * time.Second, Target: vus},
				{Duration: 9 * time.Second, Target: vus},
				{Duration: 3 * time.Second, Target: 0},
			},
			Steps:    []engine.RampStep{{Request: protocols.Request{Method: http.MethodGet, URL: srv.URL}}},
			Executor: exec,
		}, col)
		eng.Run(context.Background())
		exec.CloseIdleConnections()
		time.Sleep(200 * time.Millisecond)
	}

	run() // warm-up

	heap1 := heapAlloc()
	run()
	heap2 := heapAlloc()

	t.Logf("RampHeapAlloc — run1-post: %d KB  run2-post: %d KB", heap1/1024, heap2/1024)

	if heap1 > 0 && heap2 > heap1*2 {
		t.Errorf("ramp heap growing: run1=%d KB run2=%d KB (ratio=%.2f×)",
			heap1/1024, heap2/1024, float64(heap2)/float64(heap1))
	}
}
