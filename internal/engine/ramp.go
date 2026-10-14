package engine

import (
	"context"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

const rampTickInterval = 100 * time.Millisecond

// RampStep pairs a request template with optional assertions, auth, capture config, and think time.
type RampStep struct {
	Name       string // display name; used as per-step metric bucket key
	Request    protocols.Request
	Assertions *scenarios.Assertions
	Auth       *scenarios.Auth
	Capture    *scenarios.Capture
	// CompiledRegexes holds pre-compiled patterns from Capture.Values to avoid
	// repeated regexp.Compile calls in the hot VU loop. Key is the raw pattern string.
	CompiledRegexes map[string]*regexp.Regexp
	Retry           *scenarios.RetryConfig
	// Pause is the think time after this step completes (0 = no delay).
	Pause    time.Duration
	Group    string // optional group name for aggregate reporting
	Protocol string // "http" (default) or "websocket"
	// WSMode is the websocket connection strategy: "" / "per_request" opens a
	// fresh connection per exchange; "persistent" reuses one per VU lifetime.
	WSMode string
	// Stream marks the response as a measured stream: "sse" enables streaming
	// reads with TTFT recorded alongside total latency (HTTP steps only).
	Stream string
	// If is evaluated before executing; the step is skipped when false.
	If   string
	Loop int // 0/1 = once, N = repeat N times per VU iteration
}

// RampConfig drives a stage-based load test.
type RampConfig struct {
	Stages         []scenarios.Stage
	Steps          []RampStep
	SetupSteps     []RampStep          // run once before stages; captures shared with all VUs
	TeardownSteps  []RampStep          // run once after stages regardless of outcome
	Vars           map[string]string   // scenario-level vars available for template rendering
	DataRows       []map[string]string // rows from vars_from data file; nil = no data file
	DataMode       string              // "sequential" (default) or "random"
	Executor       protocols.Executor
	WSExecutor     protocols.Executor // used when step.Protocol == "websocket"
	CircuitBreaker *scenarios.CircuitBreakerConfig
	// SeedCaptures pre-populates the shared setup captures before any VU starts.
	// In distributed mode the coordinator runs setup once and broadcasts the
	// captured values here, so every worker's VUs inherit the same auth tokens
	// without each worker re-running setup.
	SeedCaptures map[string]string
}

// RampEngine runs multi-stage load with linear VU interpolation between stages.
type RampEngine struct {
	cfg            RampConfig
	collector      *metrics.Collector
	data           *dataSource
	activeVUs      atomic.Int32
	stageCurrent   atomic.Int32
	stageTotal     atomic.Int32
	stageStartedAt atomic.Value // stores time.Time
	stageDurNs     atomic.Int64
	// setupCaptures holds values captured during setup steps and is copied
	// into each new VU's VarContext so all VUs share the setup results.
	setupCaptures map[string]string
	cbWindow      *failWindow // nil when circuit breaker is disabled
	// ratePeakWorkers records the peak rate-mode worker count reached (grow-only),
	// for transparency and tests. Zero in VU mode.
	ratePeakWorkers atomic.Int32
}

func NewRamp(cfg RampConfig, collector *metrics.Collector) *RampEngine {
	e := &RampEngine{cfg: cfg, collector: collector}
	e.stageTotal.Store(int32(len(cfg.Stages)))
	if len(cfg.DataRows) > 0 {
		e.data = newDataSource(cfg.DataRows, cfg.DataMode)
	}
	if cb := cfg.CircuitBreaker; cb != nil && cb.ConsecutiveFailures > 0 {
		windowDur := time.Duration(cb.WindowSeconds) * time.Second
		if windowDur <= 0 {
			windowDur = time.Second
		}
		e.cbWindow = newFailWindow(cb.ConsecutiveFailures, windowDur)
	}
	return e
}

// ActiveVUs returns the number of VU goroutines currently executing.
func (e *RampEngine) ActiveVUs() int { return int(e.activeVUs.Load()) }

// StageInfo returns the current stage index (1-based), total stages, and fractional
// progress within the current stage (0–1).
func (e *RampEngine) StageInfo() (current, total int, pct float64) {
	current = int(e.stageCurrent.Load())
	total = int(e.stageTotal.Load())
	if current == 0 || total == 0 {
		return 0, 0, 0
	}
	durNs := e.stageDurNs.Load()
	if durNs == 0 {
		return current, total, 0
	}
	startedAt, ok := e.stageStartedAt.Load().(time.Time)
	if !ok {
		return current, total, 0
	}
	pct = float64(time.Since(startedAt)) / float64(durNs)
	if pct > 1 {
		pct = 1
	}
	return current, total, pct
}

// isRateMode returns true when stages use target_rps instead of target VUs.
func (e *RampEngine) isRateMode() bool {
	for _, s := range e.cfg.Stages {
		if s.TargetRPS > 0 {
			return true
		}
	}
	return false
}

func (e *RampEngine) Run(ctx context.Context) metrics.Summary {
	// Seed captures supplied by a coordinator take effect before any VU starts.
	if len(e.cfg.SeedCaptures) > 0 {
		seeded := make(map[string]string, len(e.cfg.SeedCaptures))
		for k, v := range e.cfg.SeedCaptures {
			seeded[k] = v
		}
		e.setupCaptures = seeded
	}

	// Setup: run once, single-goroutine; captures shared with all VUs.
	if len(e.cfg.SetupSteps) > 0 {
		setupCtx := e.newVarContext()
		for _, step := range e.cfg.SetupSteps {
			e.executeSingleStep(ctx, step, setupCtx, e.pickExecutor(step))
		}
		e.setupCaptures = setupCtx.Captures
	}

	var sum metrics.Summary
	if e.isRateMode() {
		sum = e.runRate(ctx)
	} else {
		sum = e.runVUs(ctx)
	}

	// Teardown: run once after all stages regardless of outcome.
	if len(e.cfg.TeardownSteps) > 0 {
		tdCtx := e.newVarContext()
		for _, step := range e.cfg.TeardownSteps {
			e.executeSingleStep(ctx, step, tdCtx, e.pickExecutor(step))
		}
	}
	return sum
}

// RunSetup executes only the configured setup steps once and returns the
// captured values. The coordinator uses this to obtain auth tokens centrally
// before broadcasting them to workers; no stages or metrics are involved.
func (e *RampEngine) RunSetup(ctx context.Context) map[string]string {
	if len(e.cfg.SetupSteps) == 0 {
		return map[string]string{}
	}
	setupCtx := e.newVarContext()
	for _, step := range e.cfg.SetupSteps {
		e.executeSingleStep(ctx, step, setupCtx, e.pickExecutor(step))
	}
	return setupCtx.Captures
}

// RunTeardown executes only the configured teardown steps once, seeding them
// with captures (e.g. an auth token needed to log out). Used by the
// coordinator after a distributed run completes.
func (e *RampEngine) RunTeardown(ctx context.Context, seed map[string]string) {
	if len(e.cfg.TeardownSteps) == 0 {
		return
	}
	if len(seed) > 0 {
		seeded := make(map[string]string, len(seed))
		for k, v := range seed {
			seeded[k] = v
		}
		e.setupCaptures = seeded
	}
	tdCtx := e.newVarContext()
	for _, step := range e.cfg.TeardownSteps {
		e.executeSingleStep(ctx, step, tdCtx, e.pickExecutor(step))
	}
}

// newVarContext creates a VarContext for a new VU, pre-populated with setup captures.
func (e *RampEngine) newVarContext() *scenarios.VarContext {
	captures := make(map[string]string, len(e.setupCaptures))
	for k, v := range e.setupCaptures {
		captures[k] = v
	}
	ctx := &scenarios.VarContext{
		Vars:     e.cfg.Vars,
		Captures: captures,
	}
	if e.data != nil {
		ctx.Data = e.data.next()
	}
	return ctx
}
