package engine

import (
	"context"
	"regexp"
	"sync"
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
//
// 依職責拆檔(wr-6):
//   - ramp.go        型別、建構、即時讀數、Run/setup/teardown 生命週期
//   - vu.go          VU 模式(封閉模型)
//   - rate.go        rate 模式(開放模型:dispatcher + worker 池)
//   - step.go        步驟執行、請求渲染與 capture
//   - breaker.go     熔斷器滑動窗口
//   - datasource.go  資料檔列分配
//   - convert.go     場景步驟 → RampStep 轉換(各入口唯一來源)
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
	e.cbWindow = newBreakerWindow(cfg.CircuitBreaker)
	return e
}

// CollectorCapacity is the size to pass metrics.NewCollector for a run over
// stages, so samples are not dropped while the aggregator catches up: the peak
// VU count in VU mode and, in rate mode, the worker-pool ceiling (that many
// requests can complete at once). Scenario and rate-mode entry points (CLI,
// dashboard, distributed worker, discover) size their collector here.
func CollectorCapacity(stages []scenarios.Stage) int {
	maxVUs, maxRPS := 0, 0
	for _, s := range stages {
		maxVUs = max(maxVUs, s.Target)
		maxRPS = max(maxRPS, s.TargetRPS)
	}
	if maxRPS > 0 {
		return rateWorkerCeiling(maxRPS)
	}
	return max(maxVUs, 1)
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

	// Teardown: run once after all stages regardless of outcome — a cancel
	// (Ctrl-C, dashboard stop) included, see teardownContext.
	e.runTeardownSteps(ctx)
	return sum
}

// CancelledTeardownTimeout is how long teardown may keep running once the run
// has been cancelled (Ctrl-C, dashboard stop).
const CancelledTeardownTimeout = 30 * time.Second

// teardownContext returns the context teardown steps run under. Teardown cleans
// up what setup created, so it never inherits the run's cancel: a cancel only
// starts a CancelledTeardownTimeout budget for whatever teardown is left —
// whether it arrived before teardown began or in the middle of it. A run that is
// never cancelled gets an unbounded teardown (each request keeps its own
// timeout).
func teardownContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return teardownContextWithBudget(ctx, CancelledTeardownTimeout)
}

func teardownContextWithBudget(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	tctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var (
		mu    sync.Mutex
		timer *time.Timer
	)
	stopWatch := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		timer = time.AfterFunc(budget, cancel)
	})
	return tctx, func() {
		stopWatch()
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		cancel()
	}
}

// runTeardownSteps runs the configured teardown steps once under
// teardownContext; every teardown path goes through here.
func (e *RampEngine) runTeardownSteps(ctx context.Context) {
	if len(e.cfg.TeardownSteps) == 0 {
		return
	}
	stepCtx, cancel := teardownContext(ctx)
	defer cancel()
	tdCtx := e.newVarContext()
	for _, step := range e.cfg.TeardownSteps {
		e.executeSingleStep(stepCtx, step, tdCtx, e.pickExecutor(step))
	}
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
// with captures (e.g. an auth token needed to log out). The distributed
// coordinator calls it after a run, also after Ctrl-C: like Run's own teardown
// it never inherits ctx's cancel, and a cancel leaves it at most
// CancelledTeardownTimeout (see teardownContext).
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
	e.runTeardownSteps(ctx)
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
