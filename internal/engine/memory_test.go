package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/engine"
	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/scenarios"
)

// TestMemoryStability verifies that running many VUs for an extended period does
// not produce unbounded goroutine growth. Idle HTTP connections are explicitly
// closed after the run so their goroutines exit before we take the measurement.
func TestMemoryStability(t *testing.T) {
	if testing.Short() {
		t.Skip("memory stability test skipped in short mode")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const vus = 50

	exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	col := metrics.NewCollector(vus)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{{Duration: 5 * time.Second, Target: vus}},
		Steps: []engine.RampStep{{
			Name:    "GET " + srv.URL,
			Request: protocols.Request{Method: "GET", URL: srv.URL},
		}},
		Executor: exec,
	}, col)

	runtime.GC()
	base := runtime.NumGoroutine()

	eng.Run(context.Background())

	// Close idle keep-alive connections so their readLoop/writeLoop goroutines exit.
	exec.CloseIdleConnections()
	time.Sleep(300 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	// Tolerance of 10 covers any background runtime/GC goroutines.
	if after-base > 10 {
		t.Errorf("goroutine leak: started with %d, ended with %d (leaked %d)",
			base, after, after-base)
	}
}

// TestRampMemoryStability runs a multi-stage scenario and checks for goroutine leaks.
func TestRampMemoryStability(t *testing.T) {
	if testing.Short() {
		t.Skip("memory stability test skipped in short mode")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stages := []scenarios.Stage{
		{DurationRaw: "1s", Target: 10, Duration: time.Second},
		{DurationRaw: "2s", Target: 20, Duration: 2 * time.Second},
		{DurationRaw: "1s", Target: 0, Duration: time.Second},
	}
	steps := []engine.RampStep{
		{Request: protocols.Request{Method: "GET", URL: srv.URL}},
	}

	exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	col := metrics.NewCollector(20)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   stages,
		Steps:    steps,
		Executor: exec,
	}, col)

	runtime.GC()
	base := runtime.NumGoroutine()

	eng.Run(context.Background())

	exec.CloseIdleConnections()
	time.Sleep(300 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	if after-base > 10 {
		t.Errorf("ramp goroutine leak: started with %d, ended with %d (leaked %d)",
			base, after, after-base)
	}
}
