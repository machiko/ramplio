package engine_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/machiko/ramplio/internal/dashboard"
	"github.com/machiko/ramplio/internal/engine"
	"github.com/machiko/ramplio/internal/metrics"
	"github.com/machiko/ramplio/internal/protocols"
	"github.com/machiko/ramplio/internal/reporter"
	"github.com/machiko/ramplio/internal/scenarios"
)

// benchDuration is the wall-clock time each benchmark "operation" runs the engine.
// With -benchtime=30s, the framework will execute ~6 iterations and average the metrics.
const benchDuration = 5 * time.Second

// okFastServer returns an httptest.Server that replies 200 OK immediately.
func okFastServer(b *testing.B) *httptest.Server {
	b.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	b.Cleanup(s.Close)
	return s
}

// BenchmarkEngine measures baseline throughput with fixed VUs and no dashboard.
// Run with:
//
//	go test ./internal/engine/... -bench=BenchmarkEngine -benchtime=30s -v
func BenchmarkEngine(b *testing.B) {
	srv := okFastServer(b)
	const vus = 100

	var sumRPS, sumP99 float64
	for i := 0; i < b.N; i++ {
		exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
		col := metrics.NewCollector(vus)
		eng := engine.NewRamp(engine.RampConfig{
			Stages: []scenarios.Stage{{Duration: benchDuration, Target: vus}},
			Steps: []engine.RampStep{{
				Name:    "bench",
				Request: protocols.Request{Method: http.MethodGet, URL: srv.URL},
			}},
			Executor: exec,
		}, col)

		b.ResetTimer()
		sum := eng.Run(context.Background())
		b.StopTimer()

		exec.CloseIdleConnections()
		sumRPS += sum.RPS()
		sumP99 += float64(sum.P99.Milliseconds())
	}

	if b.N > 0 {
		b.ReportMetric(sumRPS/float64(b.N), "rps/run")
		b.ReportMetric(sumP99/float64(b.N), "p99ms/run")
	}
}

// BenchmarkRampEngine measures throughput with a 3-stage ramp profile and no dashboard.
func BenchmarkRampEngine(b *testing.B) {
	srv := okFastServer(b)
	const vus = 100

	var sumRPS, sumP99 float64
	for i := 0; i < b.N; i++ {
		exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
		col := metrics.NewCollector(vus)
		eng := engine.NewRamp(engine.RampConfig{
			Stages: []scenarios.Stage{
				{Duration: benchDuration / 3, Target: vus},
				{Duration: benchDuration / 3, Target: vus},
				{Duration: benchDuration / 3, Target: 0},
			},
			Steps:    []engine.RampStep{{Request: protocols.Request{Method: http.MethodGet, URL: srv.URL}}},
			Executor: exec,
		}, col)

		b.ResetTimer()
		sum := eng.Run(context.Background())
		b.StopTimer()

		exec.CloseIdleConnections()
		sumRPS += sum.RPS()
		sumP99 += float64(sum.P99.Milliseconds())
	}

	if b.N > 0 {
		b.ReportMetric(sumRPS/float64(b.N), "rps/run")
		b.ReportMetric(sumP99/float64(b.N), "p99ms/run")
	}
}

// benchController adapts a running engine's metrics for the dashboard.Controller interface.
// Start/Stop/State/Result are no-ops because the engine is managed externally.
type benchController struct {
	col       *metrics.Collector
	ramp      *engine.RampEngine
	startedAt time.Time
}

func (c *benchController) Snapshot() reporter.LiveSnapshot {
	livSum := c.col.LiveSummary()
	p50, p90, p95, p99 := c.col.LivePercentiles()
	cur, total, pct := c.ramp.StageInfo()
	return reporter.LiveSnapshot{
		Total:        livSum.Total,
		Errors:       livSum.Errors,
		RPS:          livSum.RPS(),
		MeanLatency:  livSum.MeanLatency(),
		P50:          p50,
		P90:          p90,
		P95:          p95,
		P99:          p99,
		ActiveVUs:    c.ramp.ActiveVUs(),
		StageCurrent: cur,
		StageTotal:   total,
		StagePct:     pct,
		Elapsed:      time.Since(c.startedAt),
	}
}
func (c *benchController) Start(_ dashboard.RunRequest) error              { return nil }
func (c *benchController) Stop()                                           {}
func (c *benchController) State() dashboard.State                          { return dashboard.StateRunning }
func (c *benchController) Result() *dashboard.RunResult                    { return nil }
func (c *benchController) ScenarioInfo() *dashboard.ScenarioMeta           { return nil }
func (c *benchController) LoadScenario(_ []byte, _ string) error           { return nil }
func (c *benchController) ActiveGuidedProfile() *dashboard.GuidedProfile   { return nil }
func (c *benchController) WriteReport(_ io.Writer) error                   { return fmt.Errorf("no report") }
func (c *benchController) StartDiscover(_ dashboard.DiscoverRequest) error { return nil }
func (c *benchController) DiscoverProgress() ([]dashboard.DiscoverProbeSnap, *dashboard.DiscoverResultSnap, *dashboard.DiscoverCurrentSnap, []int, bool) {
	return nil, nil, nil, nil, false
}

// BenchmarkRampEngine_WithDashboard runs the same ramp profile as BenchmarkRampEngine
// but with a live dashboard server and one active WebSocket consumer attached.
// Compare p99ms/run against BenchmarkRampEngine: the difference should be < 1ms.
//
// Run both back-to-back with:
//
//	go test ./internal/engine/... -bench='BenchmarkRampEngine$|BenchmarkRampEngine_WithDashboard' -benchtime=30s
func BenchmarkRampEngine_WithDashboard(b *testing.B) {
	srv := okFastServer(b)
	const vus = 100

	var sumRPS, sumP99 float64
	for i := 0; i < b.N; i++ {
		exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
		col := metrics.NewCollector(vus)
		ramp := engine.NewRamp(engine.RampConfig{
			Stages: []scenarios.Stage{
				{Duration: benchDuration / 3, Target: vus},
				{Duration: benchDuration / 3, Target: vus},
				{Duration: benchDuration / 3, Target: 0},
			},
			Steps:    []engine.RampStep{{Request: protocols.Request{Method: http.MethodGet, URL: srv.URL}}},
			Executor: exec,
		}, col)

		ctrl := &benchController{col: col, ramp: ramp, startedAt: time.Now()}
		dashSrv := dashboard.New(ctrl, 0, "")

		ctx, cancel := context.WithCancel(context.Background())
		if err := dashSrv.Start(ctx); err != nil {
			cancel()
			b.Fatalf("dashboard start: %v", err)
		}

		// Simulate one active browser client draining WebSocket frames.
		wsConn, _, err := websocket.DefaultDialer.Dial(
			"ws://"+dashSrv.Addr()+"/ws", nil,
		)
		if err != nil {
			cancel()
			b.Fatalf("ws dial: %v", err)
		}
		wsConsumerDone := make(chan struct{})
		go func() {
			defer close(wsConsumerDone)
			for {
				if _, _, err := wsConn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		b.ResetTimer()
		sum := ramp.Run(ctx)
		b.StopTimer()

		cancel()
		wsConn.Close()
		<-wsConsumerDone
		exec.CloseIdleConnections()

		sumRPS += sum.RPS()
		sumP99 += float64(sum.P99.Milliseconds())
	}

	if b.N > 0 {
		b.ReportMetric(sumRPS/float64(b.N), "rps/run")
		b.ReportMetric(sumP99/float64(b.N), "p99ms/run")
	}
}
