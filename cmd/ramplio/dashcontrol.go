package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ramplio/ramplio/internal/dashboard"
	"github.com/ramplio/ramplio/internal/engine"
	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/reporter"
	"github.com/ramplio/ramplio/internal/scenarios"
)

// dashController implements dashboard.Controller, managing the load test lifecycle
// for tests triggered from the web UI or pre-started from CLI flags.
type dashController struct {
	mu        sync.RWMutex
	state     dashboard.State
	result    *dashboard.RunResult
	cancel    context.CancelFunc
	snapCache reporter.LiveSnapshot
}

func newDashController() *dashController {
	return &dashController{state: dashboard.StateIdle}
}

func (c *dashController) Snapshot() reporter.LiveSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapCache
}

func (c *dashController) State() dashboard.State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *dashController) Result() *dashboard.RunResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.result
}

func (c *dashController) Stop() {
	c.mu.RLock()
	cancel := c.cancel
	c.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// Start launches a new load test in the background. Allowed from idle or done state.
func (c *dashController) Start(req dashboard.RunRequest) error {
	if err := validateRunRequest(req); err != nil {
		return err
	}

	c.mu.Lock()
	if c.state == dashboard.StateRunning {
		c.mu.Unlock()
		return fmt.Errorf("a test is already running; stop it first")
	}

	vus := req.VUs
	if vus <= 0 {
		vus = 1
	}
	dur, _ := time.ParseDuration(req.Duration) // validated above
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}

	// Three equal stages: ramp-up → hold → ramp-down
	stageDur := dur / 3
	if stageDur < time.Second {
		stageDur = time.Second
	}
	stgs := []scenarios.Stage{
		{Duration: stageDur, Target: vus},
		{Duration: stageDur, Target: vus},
		{Duration: stageDur, Target: 0},
	}
	steps := []engine.RampStep{{
		Request: protocols.Request{Method: method, URL: req.URL},
	}}

	col := metrics.NewCollector(vus)
	ramp := engine.NewRamp(engine.RampConfig{
		Stages:   stgs,
		Steps:    steps,
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.state = dashboard.StateRunning
	c.result = nil
	c.snapCache = reporter.LiveSnapshot{}
	startedAt := time.Now()
	c.mu.Unlock()

	go c.runLoop(ctx, col, ramp, startedAt)
	return nil
}

func validateRunRequest(req dashboard.RunRequest) error {
	if req.URL == "" {
		return fmt.Errorf("url is required")
	}
	if req.VUs < 0 || req.VUs > 5000 {
		return fmt.Errorf("vus must be between 1 and 5000")
	}
	dur, err := time.ParseDuration(req.Duration)
	if err != nil || dur <= 0 {
		return fmt.Errorf("invalid duration %q: use a positive value like 30s or 2m", req.Duration)
	}
	return nil
}

func (c *dashController) runLoop(
	ctx context.Context,
	col *metrics.Collector,
	ramp *engine.RampEngine,
	startedAt time.Time,
) {
	sumCh := make(chan metrics.Summary, 1)
	go func() { sumCh <- ramp.Run(ctx) }()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.refreshSnap(col, ramp, startedAt)
		case sum := <-sumCh:
			c.refreshSnap(col, ramp, startedAt)
			c.mu.Lock()
			c.state = dashboard.StateDone
			errPct := 0.0
			if sum.Total > 0 {
				errPct = float64(sum.Errors) / float64(sum.Total) * 100
			}
			c.result = &dashboard.RunResult{
				Total:    sum.Total,
				Errors:   sum.Errors,
				P50Ms:    sum.P50.Milliseconds(),
				P99Ms:    sum.P99.Milliseconds(),
				ErrorPct: errPct,
				MeanMs:   sum.MeanLatency().Milliseconds(),
				RPS:      sum.RPS(),
				WallSec:  sum.WallTime.Seconds(),
			}
			c.mu.Unlock()
			return
		}
	}
}

func (c *dashController) refreshSnap(col *metrics.Collector, ramp *engine.RampEngine, startedAt time.Time) {
	livSum := col.LiveSummary()
	p50, p90, p95, p99 := col.LivePercentiles()
	cur, total, pct := ramp.StageInfo()
	snap := reporter.LiveSnapshot{
		Total:        livSum.Total,
		Errors:       livSum.Errors,
		RPS:          livSum.RPS(),
		MeanLatency:  livSum.MeanLatency(),
		P50:          p50,
		P90:          p90,
		P95:          p95,
		P99:          p99,
		ActiveVUs:    ramp.ActiveVUs(),
		StageCurrent: cur,
		StageTotal:   total,
		StagePct:     pct,
		Elapsed:      time.Since(startedAt),
	}
	c.mu.Lock()
	c.snapCache = snap
	c.mu.Unlock()
}
