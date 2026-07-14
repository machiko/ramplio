package main

// dashController 的容量探測面:GUI 觸發的 discover 生命週期與進度回報。

import (
	"context"
	"fmt"
	"time"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/discover"
)

func (c *dashController) StartDiscover(req dashboard.DiscoverRequest) error {
	if req.URL == "" {
		return fmt.Errorf("url is required")
	}
	tol := 2 * time.Second
	if req.Tolerance != "" {
		var err error
		tol, err = time.ParseDuration(req.Tolerance)
		if err != nil {
			return fmt.Errorf("invalid tolerance %q", req.Tolerance)
		}
	}
	pd := 15 * time.Second
	if req.ProbeDuration != "" {
		var err error
		pd, err = time.ParseDuration(req.ProbeDuration)
		if err != nil {
			return fmt.Errorf("invalid probe_duration %q", req.ProbeDuration)
		}
	}
	maxRPS := req.MaxRPS
	if maxRPS <= 0 {
		maxRPS = 500
	}

	c.mu.Lock()
	if c.state == dashboard.StateRunning {
		c.mu.Unlock()
		return fmt.Errorf("a test is already running; stop it first")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.state = dashboard.StateRunning
	c.result = nil
	c.discoverActive = true
	c.discoverProbes = nil
	c.discoverResult = nil
	c.mu.Unlock()

	go c.runDiscover(ctx, req.URL, tol, maxRPS, pd)
	return nil
}

func (c *dashController) runDiscover(ctx context.Context, url string, tol time.Duration, maxRPS int, pd time.Duration) {
	cfg := discover.Config{
		URL:           url,
		Tolerance:     tol,
		MaxRPS:        maxRPS,
		ProbeDuration: pd,
		HTTPConfig:    c.httpCfg,
	}

	probeSeq := discover.ProbeSequence(maxRPS)
	c.mu.Lock()
	c.discoverProbeSeq = probeSeq
	c.discoverProbeDur = pd
	c.mu.Unlock()

	prober := discover.New(cfg)
	result := prober.Run(ctx,
		func(rps int) {
			c.mu.Lock()
			c.discoverCurrentRPS = rps
			c.discoverProbeStart = time.Now()
			c.mu.Unlock()
		},
		func(pr discover.ProbeResult) {
			status := "pass"
			switch pr.Status {
			case discover.ProbeWarn:
				status = "warn"
			case discover.ProbeFail:
				status = "fail"
			}
			snap := dashboard.DiscoverProbeSnap{
				RPS:      pr.RPS,
				P99Ms:    pr.P99.Milliseconds(),
				ErrorPct: pr.ErrorRate,
				Status:   status,
			}
			c.mu.Lock()
			c.discoverProbes = append(c.discoverProbes, snap)
			c.discoverCurrentRPS = 0
			c.mu.Unlock()
		},
	)

	discResult := &dashboard.DiscoverResultSnap{
		SafeLimit:     result.SafeLimit,
		BreakingPoint: result.BreakingPoint,
		Exhausted:     result.Exhausted,
	}
	c.mu.Lock()
	c.state = dashboard.StateDone
	c.discoverResult = discResult
	c.mu.Unlock()
}

func (c *dashController) DiscoverProgress() ([]dashboard.DiscoverProbeSnap, *dashboard.DiscoverResultSnap, *dashboard.DiscoverCurrentSnap, []int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var cur *dashboard.DiscoverCurrentSnap
	if c.discoverCurrentRPS > 0 {
		cur = &dashboard.DiscoverCurrentSnap{
			RPS:             c.discoverCurrentRPS,
			ElapsedMs:       time.Since(c.discoverProbeStart).Milliseconds(),
			ProbeDurationMs: c.discoverProbeDur.Milliseconds(),
		}
	}
	return c.discoverProbes, c.discoverResult, cur, c.discoverProbeSeq, c.discoverActive
}
