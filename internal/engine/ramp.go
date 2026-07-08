package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/tidwall/gjson"
	"golang.org/x/time/rate"
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

// dataSource distributes data file rows to VUs.
type dataSource struct {
	rows    []map[string]string
	mode    string
	counter atomic.Int64
}

func newDataSource(rows []map[string]string, mode string) *dataSource {
	return &dataSource{rows: rows, mode: mode}
}

func (ds *dataSource) next() map[string]string {
	if len(ds.rows) == 0 {
		return nil
	}
	if ds.mode == "random" {
		// Use counter as a cheap pseudo-random index (avoids math/rand lock contention).
		idx := ds.counter.Add(1) - 1
		return ds.rows[int(idx*2654435761)%len(ds.rows)]
	}
	idx := ds.counter.Add(1) - 1
	return ds.rows[int(idx)%len(ds.rows)]
}

// failWindow implements a sliding-time-window failure counter for the circuit breaker.
// Unlike a bare atomic counter, it avoids false trips when many VUs fail simultaneously
// in a burst but then recover quickly.
type failWindow struct {
	mu        sync.Mutex
	times     []time.Time // ring buffer of recent failure timestamps
	head      int
	threshold int
	window    time.Duration
	tripped   bool
}

func newFailWindow(threshold int, windowDur time.Duration) *failWindow {
	return &failWindow{
		times:     make([]time.Time, threshold),
		threshold: threshold,
		window:    windowDur,
	}
}

// record adds a failure timestamp and returns whether the circuit should trip.
func (w *failWindow) record(t time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tripped {
		return true
	}
	w.times[w.head] = t
	w.head = (w.head + 1) % w.threshold
	cutoff := t.Add(-w.window)
	count := 0
	for _, ts := range w.times {
		if !ts.IsZero() && ts.After(cutoff) {
			count++
		}
	}
	if count >= w.threshold {
		w.tripped = true
	}
	return w.tripped
}

// reset clears the window (called on successful request).
func (w *failWindow) reset() {
	w.mu.Lock()
	for i := range w.times {
		w.times[i] = time.Time{}
	}
	w.mu.Unlock()
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

func (e *RampEngine) maxTargetRPS() int {
	max := 0
	for _, s := range e.cfg.Stages {
		if s.TargetRPS > max {
			max = s.TargetRPS
		}
	}
	return max
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

// runVUs is the former Run() body for VU mode; extracted so Run() can wrap it.
func (e *RampEngine) runVUs(ctx context.Context) metrics.Summary {
	start := time.Now()

	var (
		wg      sync.WaitGroup
		cancels []context.CancelFunc
		active  int
	)

	addVU := func() {
		vuCtx, cancel := context.WithCancel(ctx)
		cancels = append(cancels, cancel)
		active++
		e.activeVUs.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer e.activeVUs.Add(-1)
			e.runVU(vuCtx)
		}()
	}

	removeVU := func() {
		if active == 0 {
			return
		}
		last := active - 1
		cancels[last]()
		cancels[last] = nil
		cancels = cancels[:last]
		active--
	}

	setVUs := func(target int) {
		for active < target {
			addVU()
		}
		for active > target {
			removeVU()
		}
	}

	prevTarget := 0
	for stageIdx, stage := range e.cfg.Stages {
		if ctx.Err() != nil {
			break
		}

		e.stageCurrent.Store(int32(stageIdx + 1))
		e.stageStartedAt.Store(time.Now())
		e.stageDurNs.Store(int64(stage.Duration))

		stageCtx, stageCancel := context.WithTimeout(ctx, stage.Duration)
		stageStart := time.Now()
		ticker := time.NewTicker(rampTickInterval)

	stageLoop:
		for {
			select {
			case <-stageCtx.Done():
				ticker.Stop()
				break stageLoop
			case t := <-ticker.C:
				elapsed := t.Sub(stageStart).Seconds()
				progress := math.Min(elapsed/stage.Duration.Seconds(), 1.0)
				target := prevTarget + int(math.Round(float64(stage.Target-prevTarget)*progress))
				setVUs(target)
			}
		}

		stageCancel()
		setVUs(stage.Target) // enforce exact VU count at stage boundary
		prevTarget = stage.Target
	}

	setVUs(0)
	wg.Wait()

	sum := e.collector.Stop()
	sum.WallTime = time.Since(start)
	return sum
}

// Rate-mode worker pool bounds. maxWorkers is the *ceiling* the pool may grow to
// (maxRPS × headroom), not an eager pre-spawn count: workers are added on demand
// so a low-latency target uses only the few it actually needs.
const (
	rateMinWorkers     = 10
	rateHardCap        = 5000
	rateWorkerHeadroom = 5
)

// ratePool grows the rate-mode worker set on demand. The single dispatcher calls
// maybeGrow before each send: if no worker is idle and we are below the cap, it
// spawns one; at the cap it stops growing and lets the send block — that block is
// the genuine backpressure the coordinated-omission correction must capture, so
// growth never masks real overload.
type ratePool struct {
	jobs   chan time.Time
	idle   atomic.Int32
	total  atomic.Int32
	max    int
	capHit atomic.Bool
	spawn  func()
}

func (p *ratePool) maybeGrow() {
	if p.idle.Load() > 0 {
		return // a worker is already waiting to take this job
	}
	if int(p.total.Load()) >= p.max {
		p.capHit.Store(true) // at the ceiling: the send will block (real backpressure)
		return
	}
	p.spawn()
}

// runRate drives the load test in rate mode (fixed RPS via token bucket).
func (e *RampEngine) runRate(ctx context.Context) metrics.Summary {
	start := time.Now()

	maxRPS := e.maxTargetRPS()
	maxWorkers := maxRPS * rateWorkerHeadroom
	if maxWorkers < rateMinWorkers {
		maxWorkers = rateMinWorkers
	}
	if maxWorkers > rateHardCap {
		maxWorkers = rateHardCap
	}

	// workerCtx lets us stop workers cleanly after all stages finish.
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	// burst=1: the limiter releases tokens at the exact configured cadence. A
	// larger burst would let tokens pile up while workers are busy and then fire
	// back-to-back, masking the queueing delay coordinated-omission correction
	// must capture.
	lim := rate.NewLimiter(0, 1)

	// jobs carries scheduled dispatch timestamps from the single dispatcher to
	// the workers. Its buffer is the backlog depth: when workers fall behind, a
	// timestamp ages in the queue and that age is exactly the omission to correct.
	jobs := make(chan time.Time, maxWorkers)

	var wg sync.WaitGroup
	pool := &ratePool{jobs: jobs, max: maxWorkers}
	pool.spawn = func() {
		n := pool.total.Add(1)
		if n > e.ratePeakWorkers.Load() {
			e.ratePeakWorkers.Store(n)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.runRateWorker(workerCtx, jobs, &pool.idle)
		}()
	}
	// Start with the minimum pool; the dispatcher grows it on demand. All spawns
	// happen here or inside the dispatcher, both strictly before wg.Wait below.
	for range rateMinWorkers {
		pool.spawn()
	}

	// The dispatcher paces requests at the current rate independent of worker
	// availability, so requests the generator can't serve in time are measured
	// as late rather than silently delayed.
	dispCtx, dispCancel := context.WithCancel(ctx)
	dispDone := make(chan struct{})
	go func() {
		defer close(dispDone)
		e.dispatch(dispCtx, lim, pool)
	}()

	prevTarget := 0
	for stageIdx, stage := range e.cfg.Stages {
		if ctx.Err() != nil {
			break
		}

		e.stageCurrent.Store(int32(stageIdx + 1))
		e.stageStartedAt.Store(time.Now())
		e.stageDurNs.Store(int64(stage.Duration))

		stageCtx, stageCancel := context.WithTimeout(ctx, stage.Duration)
		stageStart := time.Now()
		ticker := time.NewTicker(rampTickInterval)

	stageLoop:
		for {
			select {
			case <-stageCtx.Done():
				ticker.Stop()
				break stageLoop
			case t := <-ticker.C:
				elapsed := t.Sub(stageStart).Seconds()
				progress := math.Min(elapsed/stage.Duration.Seconds(), 1.0)
				currentRPS := float64(prevTarget) + float64(stage.TargetRPS-prevTarget)*progress
				lim.SetLimit(rate.Limit(currentRPS))
			}
		}

		stageCancel()
		lim.SetLimit(rate.Limit(stage.TargetRPS))
		prevTarget = stage.TargetRPS
	}

	dispCancel()
	<-dispDone
	workerCancel()
	wg.Wait()

	sum := e.collector.Stop()
	sum.WallTime = time.Since(start)
	sum.GeneratorWorkerCapHit = pool.capHit.Load()
	return sum
}

// dispatch is the single producer for the jobs channel. It paces requests at the
// limiter's current rate and stamps each with the time it was due to be sent.
// When all workers are busy and the backlog is full the send blocks — natural
// open-model backpressure — and the time a timestamp waits before a worker picks
// it up becomes the coordinated-omission correction applied in the collector.
//
// It uses Reserve + a capped sleep rather than lim.Wait so it re-reads the rate
// at least every dispatchReeval: a blocked Wait holds a reservation computed
// against the old rate and would not observe the ramp's SetLimit changes (and at
// rate 0 would block forever). When the current rate is ~0 (ramping from/to 0)
// the reservation delay is effectively infinite, so we cancel and poll instead
// of dispatching.
func (e *RampEngine) dispatch(ctx context.Context, lim *rate.Limiter, pool *ratePool) {
	const dispatchReeval = rampTickInterval
	for {
		if ctx.Err() != nil {
			return
		}
		if e.isCircuitTripped() {
			return
		}

		now := time.Now()
		r := lim.ReserveN(now, 1)
		delay := r.DelayFrom(now)
		if !r.OK() || delay > dispatchReeval {
			// Rate too low to dispatch right now; don't hold a long reservation,
			// re-evaluate after one tick so rate changes are picked up.
			r.Cancel()
			if !sleepCtx(ctx, dispatchReeval) {
				return
			}
			continue
		}
		if delay > 0 && !sleepCtx(ctx, delay) {
			r.Cancel()
			return
		}

		scheduledAt := now.Add(delay)
		// Grow the pool to meet demand before handing off; at the cap this is a
		// no-op and the send blocks, producing the backpressure CO must measure.
		pool.maybeGrow()
		select {
		case pool.jobs <- scheduledAt:
		case <-ctx.Done():
			return
		}
	}
}

// sleepCtx sleeps for d or until ctx is done. Returns false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *RampEngine) runRateWorker(ctx context.Context, jobs <-chan time.Time, idle *atomic.Int32) {
	varCtx := e.newVarContext()
	stepIdx := 0
	for {
		var scheduledAt time.Time
		// Count this worker as idle while it waits for a job, so the dispatcher
		// knows whether it needs to grow the pool.
		idle.Add(1)
		select {
		case <-ctx.Done():
			idle.Add(-1)
			return
		case scheduledAt = <-jobs:
		}
		idle.Add(-1)
		if e.isCircuitTripped() {
			return
		}

		step := e.cfg.Steps[stepIdx%len(e.cfg.Steps)]
		stepIdx++

		if step.If != "" && !scenarios.EvalCondition(step.If, varCtx) {
			continue
		}

		repeat := step.Loop
		if repeat < 1 {
			repeat = 1
		}
		exec := e.pickExecutor(step)

		e.activeVUs.Add(1)
		for r := 0; r < repeat; r++ {
			req, err := renderRequest(step, varCtx)
			if err != nil {
				e.collector.Add(metrics.Sample{Error: err, At: time.Now(), ScheduledAt: scheduledAt, StepName: step.Name, Group: step.Group})
				e.incFails()
				continue
			}

			result := exec.Execute(ctx, req)
			if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, context.DeadlineExceeded) {
				e.activeVUs.Add(-1)
				return
			}

			if result.Error == nil {
				applyCaptures(step.Capture, step.CompiledRegexes, result, varCtx)
				if step.Assertions != nil {
					if assertErr := scenarios.EvalAssertions(step.Assertions, result); assertErr != nil {
						result.Error = assertErr
					}
				}
			}

			e.collector.Add(metrics.Sample{
				Latency:     result.Latency,
				StatusCode:  result.StatusCode,
				BytesRead:   result.BytesRead,
				Error:       result.Error,
				At:          time.Now(),
				ScheduledAt: scheduledAt,
				StepName:    step.Name,
				Group:       step.Group,
			})

			if result.Error != nil {
				e.incFails()
			} else {
				if e.cbWindow != nil {
					e.cbWindow.reset()
				}
			}
		}
		e.activeVUs.Add(-1)

		if step.Pause > 0 {
			select {
			case <-time.After(step.Pause):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (e *RampEngine) runVU(ctx context.Context) {
	// Each VU gets its own cookie jar so session cookies persist across steps.
	sessionExec := protocols.Executor(e.cfg.Executor)
	if h, ok := e.cfg.Executor.(*protocols.HTTPExecutor); ok {
		sessionExec = h.NewSession()
	}

	varCtx := e.newVarContext()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		for i := range e.cfg.Steps {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if e.isCircuitTripped() {
				return
			}

			step := e.cfg.Steps[i]

			// M8: conditional skip
			if step.If != "" && !scenarios.EvalCondition(step.If, varCtx) {
				continue
			}

			// M8: loop
			repeat := step.Loop
			if repeat < 1 {
				repeat = 1
			}

			exec := e.stepExecutor(step, sessionExec)

			for r := 0; r < repeat; r++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				req, err := renderRequest(step, varCtx)
				if err != nil {
					e.collector.Add(metrics.Sample{Error: err, At: time.Now(), StepName: step.Name, Group: step.Group})
					e.incFails()
					continue
				}

				result := exec.Execute(ctx, req)
				if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, context.DeadlineExceeded) {
					return
				}

				if result.Error == nil {
					applyCaptures(step.Capture, step.CompiledRegexes, result, varCtx)
					if step.Assertions != nil {
						if assertErr := scenarios.EvalAssertions(step.Assertions, result); assertErr != nil {
							result.Error = assertErr
						}
					}
				}

				e.collector.Add(metrics.Sample{
					Latency:    result.Latency,
					StatusCode: result.StatusCode,
					BytesRead:  result.BytesRead,
					Error:      result.Error,
					At:         time.Now(),
					StepName:   step.Name,
					Group:      step.Group,
				})

				if result.Error != nil {
					e.incFails()
				} else {
					if e.cbWindow != nil {
						e.cbWindow.reset()
					}
				}

				if step.Pause > 0 {
					select {
					case <-time.After(step.Pause):
					case <-ctx.Done():
						return
					}
				}
			}
		}
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

// stepExecutor returns the correct executor for a step (protocol + retry wrapper).
// sessionExec is the HTTP session executor for the current VU; pass e.cfg.Executor for rate mode.
func (e *RampEngine) stepExecutor(step RampStep, sessionExec protocols.Executor) protocols.Executor {
	var base protocols.Executor
	if step.Protocol == "websocket" {
		if e.cfg.WSExecutor != nil {
			base = e.cfg.WSExecutor
		} else {
			base = protocols.NewWSExecutor()
		}
	} else {
		base = sessionExec
	}
	if step.Retry != nil && step.Retry.Count > 0 {
		return protocols.NewRetryingExecutor(base, step.Retry.Count, step.Retry.On, step.Retry.BackoffMs)
	}
	return base
}

// pickExecutor selects the executor for a step without a session (setup/teardown/rate mode).
func (e *RampEngine) pickExecutor(step RampStep) protocols.Executor {
	return e.stepExecutor(step, e.cfg.Executor)
}

// executeSingleStep runs a step once; used by setup and teardown.
func (e *RampEngine) executeSingleStep(ctx context.Context, step RampStep, varCtx *scenarios.VarContext, exec protocols.Executor) {
	req, err := renderRequest(step, varCtx)
	if err != nil {
		return
	}
	result := exec.Execute(ctx, req)
	if result.Error == nil {
		applyCaptures(step.Capture, step.CompiledRegexes, result, varCtx)
	}
}

// isCircuitTripped returns true when the circuit breaker has fired.
func (e *RampEngine) isCircuitTripped() bool {
	return e.cbWindow != nil && e.cbWindow.tripped
}

func (e *RampEngine) incFails() {
	if e.cbWindow != nil {
		e.cbWindow.record(time.Now())
	}
}

// renderRequest resolves template tokens in the step's URL, headers, and body,
// then applies any auth helper.
func renderRequest(step RampStep, ctx *scenarios.VarContext) (protocols.Request, error) {
	url, err := scenarios.RenderString(step.Request.URL, ctx)
	if err != nil {
		return protocols.Request{}, err
	}

	headers, err := scenarios.RenderHeaders(step.Request.Headers, ctx)
	if err != nil {
		return protocols.Request{}, err
	}
	if headers == nil {
		headers = make(map[string]string)
	}

	var body []byte
	if len(step.Request.Body) > 0 {
		rendered, err := scenarios.RenderString(string(step.Request.Body), ctx)
		if err != nil {
			return protocols.Request{}, err
		}
		body = []byte(rendered)
	}

	if step.Auth != nil {
		if step.Auth.Bearer != nil {
			token, err := scenarios.RenderString(*step.Auth.Bearer, ctx)
			if err != nil {
				return protocols.Request{}, err
			}
			headers["Authorization"] = "Bearer " + token
		} else if step.Auth.Basic != nil {
			username, err := scenarios.RenderString(step.Auth.Basic.Username, ctx)
			if err != nil {
				return protocols.Request{}, err
			}
			password, err := scenarios.RenderString(step.Auth.Basic.Password, ctx)
			if err != nil {
				return protocols.Request{}, err
			}
			headers["Authorization"] = basicAuthHeader(username, password)
		}
	}

	return protocols.Request{
		Method:  step.Request.Method,
		URL:     url,
		Headers: headers,
		Body:    body,
	}, nil
}

// applyCaptures extracts values from the result and stores them in varCtx.Captures.
// compiled is a pre-built map of regex patterns to avoid repeated Compile calls in the hot loop.
func applyCaptures(cap *scenarios.Capture, compiled map[string]*regexp.Regexp, result protocols.Result, ctx *scenarios.VarContext) {
	if cap == nil {
		return
	}
	for key, expr := range cap.Values {
		switch {
		case strings.HasPrefix(expr, "cookie:"):
			cookieName := strings.TrimPrefix(expr, "cookie:")
			for _, raw := range result.RawSetCookies {
				if c, err := http.ParseSetCookie(raw); err == nil && c.Name == cookieName {
					ctx.Captures[key] = c.Value
					break
				}
			}
		case strings.HasPrefix(expr, "header:"):
			ctx.Captures[key] = result.ResponseHeaders[http.CanonicalHeaderKey(strings.TrimPrefix(expr, "header:"))]
		case strings.HasPrefix(expr, "regex:"):
			pattern := strings.TrimPrefix(expr, "regex:")
			re := compiled[pattern]
			if re == nil {
				re, _ = regexp.Compile(pattern)
			}
			if re != nil {
				if m := re.FindSubmatch(result.Body); len(m) > 1 {
					ctx.Captures[key] = string(m[1])
				}
			}
		default:
			ctx.Captures[key] = gjson.GetBytes(result.Body, scenarios.JSONPathToGJSON(expr)).String()
		}
	}
}

func basicAuthHeader(username, password string) string {
	creds := username + ":" + password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}
