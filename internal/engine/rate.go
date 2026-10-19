package engine

// RampEngine 的 rate 模式(開放模型):單一 dispatcher 依 stage 目標 RPS 排定
// 每個請求的應送時刻,交給按需成長的 worker 池;排隊時間即 CO 修正的來源。

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

func (e *RampEngine) maxTargetRPS() int {
	max := 0
	for _, s := range e.cfg.Stages {
		if s.TargetRPS > max {
			max = s.TargetRPS
		}
	}
	return max
}

// Rate-mode worker pool bounds. maxWorkers is the *ceiling* the pool may grow to
// (maxRPS × headroom), not an eager pre-spawn count: workers are added on demand
// so a low-latency target uses only the few it actually needs.
const (
	rateMinWorkers     = 10
	rateHardCap        = 5000
	rateWorkerHeadroom = 5
)

// rateWorkerCeiling is the most rate-mode workers — and so the most requests
// in flight — a run peaking at maxRPS may use.
func rateWorkerCeiling(maxRPS int) int {
	return min(max(maxRPS*rateWorkerHeadroom, rateMinWorkers), rateHardCap)
}

// ratePool grows the rate-mode worker set on demand. The single dispatcher calls
// maybeGrow before each send: if no worker is idle and we are below the cap, it
// spawns one; at the cap it stops growing and lets the send block — that block is
// the genuine backpressure the coordinated-omission correction must capture, so
// growth never masks real overload.
//
// A worker counts as idle from the moment grow adds it (not from when its
// goroutine first runs), so a catch-up burst of sends sees the workers it has
// already asked for instead of spawning one per send up to the ceiling.
type ratePool struct {
	jobs   chan time.Time
	idle   atomic.Int32
	total  atomic.Int32
	max    int
	capHit atomic.Bool
	start  func() // launches one worker goroutine; bookkeeping is grow's
	// taken counts jobs a worker took up: the requests the generator actually
	// sent, as opposed to the samples that happened to be recorded.
	taken atomic.Int64
}

func (p *ratePool) maybeGrow() {
	// Jobs already queued will each take one of the idle workers, so only the
	// surplus is free for this send — in a catch-up burst the queue fills faster
	// than idle workers wake to drain it.
	if p.idle.Load() > int32(len(p.jobs)) {
		return // a worker is already waiting to take this job
	}
	if int(p.total.Load()) >= p.max {
		p.capHit.Store(true) // at the ceiling: the send will block (real backpressure)
		return
	}
	p.grow()
}

// grow adds one worker, counted as idle before its goroutine runs.
func (p *ratePool) grow() {
	p.total.Add(1)
	p.idle.Add(1)
	p.start()
}

// runRate drives the load test in rate mode (fixed RPS on a fixed cadence).
func (e *RampEngine) runRate(ctx context.Context) metrics.Summary {
	start := time.Now()

	maxWorkers := rateWorkerCeiling(e.maxTargetRPS())

	// workerCtx lets us stop workers cleanly after all stages finish.
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	// The dispatcher paces itself from this schedule; the stage loop below
	// updates it as the rate ramps.
	sched := newRateSchedule()

	// jobs carries scheduled dispatch timestamps from the single dispatcher to
	// the workers. Its buffer is the backlog depth: when workers fall behind, a
	// timestamp ages in the queue and that age is exactly the omission to correct.
	jobs := make(chan time.Time, maxWorkers)

	var wg sync.WaitGroup
	pool := &ratePool{jobs: jobs, max: maxWorkers}
	pool.start = func() {
		if n := pool.total.Load(); n > e.ratePeakWorkers.Load() {
			e.ratePeakWorkers.Store(n)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.runRateWorker(workerCtx, pool)
		}()
	}
	// Start with the minimum pool; the dispatcher grows it on demand. All spawns
	// happen here or inside the dispatcher, both strictly before wg.Wait below.
	for range rateMinWorkers {
		pool.grow()
	}

	// The dispatcher paces requests at the current rate independent of worker
	// availability, so requests the generator can't serve in time are measured
	// as late rather than silently delayed.
	dispCtx, dispCancel := context.WithCancel(ctx)
	dispDone := make(chan struct{})
	go func() {
		defer close(dispDone)
		e.dispatch(dispCtx, sched, pool)
	}()

	// planned integrates the rate the dispatcher was actually asked to run
	// (the 100ms staircase applied below), so the report can say how much of the
	// declared load really went out. Once the circuit breaker trips the
	// dispatcher stops on purpose, so the schedule stops counting too — from
	// the tick that sees the trip, dropping at most one tick of schedule: an
	// undercount, which can only hide a shortfall, never invent one.
	var planned float64
	appliedRPS, appliedAt := 0.0, time.Now()
	apply := func(rps float64, now time.Time) {
		if !e.isCircuitTripped() {
			planned += appliedRPS * now.Sub(appliedAt).Seconds()
		}
		appliedRPS, appliedAt = rps, now
		sched.set(rps)
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
				currentRPS := float64(prevTarget) + float64(stage.TargetRPS-prevTarget)*progress
				apply(currentRPS, t)
			}
		}

		stageCancel()
		apply(float64(stage.TargetRPS), time.Now())
		prevTarget = stage.TargetRPS
	}
	apply(0, time.Now())

	dispCancel()
	<-dispDone
	workerCancel()
	wg.Wait()

	sum := e.collector.Stop()
	sum.WallTime = time.Since(start)
	sum.GeneratorWorkerCapHit = pool.capHit.Load()
	sum.GeneratorPlannedRequests = int64(math.Round(planned))
	sum.GeneratorDispatchedRequests = pool.taken.Load()
	return sum
}

// rateSchedule is the dispatcher's current pace: the interval between two
// intended send times, or 0 while the rate is 0. set wakes a dispatcher idling
// at rate 0 so a stage's first requests go out when the rate is applied, not up
// to a whole poll tick later (that tick used to cost ~10% of a short run).
type rateSchedule struct {
	intervalNs atomic.Int64
	wake       chan struct{}
}

func newRateSchedule() *rateSchedule { return &rateSchedule{wake: make(chan struct{}, 1)} }

func (s *rateSchedule) set(rps float64) {
	if rps <= 0 {
		s.intervalNs.Store(0)
		return
	}
	s.intervalNs.Store(int64(float64(time.Second) / rps))
	select {
	case s.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

func (s *rateSchedule) interval() time.Duration { return time.Duration(s.intervalNs.Load()) }

// dispatch is the single producer for the jobs channel. It runs a fixed
// cadence: the n-th request is due at start + n·interval, and that intended
// time is what the worker gets as the sample's ScheduledAt. When the
// dispatcher wakes late (timer overshoot is tens of µs per sleep) or the send
// blocked (all workers busy, the backlog full), it dispatches immediately and
// the schedule does not slip — so the declared rate is delivered, and the
// lateness lands in the stamp where coordinated-omission correction attributes
// it, rather than quietly becoming a lower rate. A token bucket with burst=1
// could not catch up: every overshoot was a lost slice of the rate, 10–25%
// at a few thousand RPS.
//
// The schedule is re-read at least every rampTickInterval so ramps take
// effect, and restarts from "now" when woken after a stretch at rate 0. A send
// planned under a slower rate is pulled in to one new interval from now, so a
// sudden rate increase is not held back by the old, longer gap.
//
// Catch-up is unbounded, as in wrk2: after the generator stalls (a GC pause, a
// blocked send) the overdue requests go out back to back, so the instantaneous
// rate briefly exceeds the declared one while the average stays on target.
func (e *RampEngine) dispatch(ctx context.Context, sched *rateSchedule, pool *ratePool) {
	const reeval = rampTickInterval
	var next time.Time // zero until the first positive rate
	for {
		if ctx.Err() != nil {
			return
		}
		if e.isCircuitTripped() {
			return
		}
		interval := sched.interval()
		if interval <= 0 {
			// Rate 0 (ramping from/to zero): nothing is due; wait to be woken.
			next = time.Time{}
			select {
			case <-sched.wake:
			case <-ctx.Done():
				return
			}
			continue
		}
		now := time.Now()
		if next.IsZero() {
			next = now
		} else if next.After(now.Add(interval)) {
			next = now.Add(interval)
		}
		if wait := next.Sub(now); wait > 0 {
			if wait > reeval {
				// Low rate: sleep one tick at a time so a rate change is noticed.
				if !sleepCtx(ctx, reeval) {
					return
				}
				continue
			}
			if !sleepCtx(ctx, wait) {
				return
			}
		}

		// Grow the pool to meet demand before handing off; at the cap this is a
		// no-op and the send blocks, producing the backpressure CO must measure.
		pool.maybeGrow()
		select {
		case pool.jobs <- next:
		case <-ctx.Done():
			return
		}
		next = next.Add(interval)
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

func (e *RampEngine) runRateWorker(ctx context.Context, pool *ratePool) {
	varCtx := e.newVarContext()
	stepIdx := 0
	// grow already counted this worker as idle; it stays idle while it waits
	// for a job, so the dispatcher knows whether it needs to grow the pool.
	for {
		var scheduledAt time.Time
		select {
		case <-ctx.Done():
			pool.idle.Add(-1)
			return
		case scheduledAt = <-pool.jobs:
		}
		pool.idle.Add(-1)
		if e.isCircuitTripped() {
			return
		}
		pool.taken.Add(1)
		if !e.runRateJob(ctx, varCtx, &stepIdx, scheduledAt) {
			return
		}
		pool.idle.Add(1)
	}
}

// runRateJob runs the next step for one dispatched slot. It returns false when
// the run is over and the worker should exit.
func (e *RampEngine) runRateJob(ctx context.Context, varCtx *scenarios.VarContext, stepIdx *int, scheduledAt time.Time) bool {
	step := e.cfg.Steps[*stepIdx%len(e.cfg.Steps)]
	*stepIdx++

	if step.If != "" && !scenarios.EvalCondition(step.If, varCtx) {
		return true
	}

	repeat := step.Loop
	if repeat < 1 {
		repeat = 1
	}
	exec := e.pickExecutor(step)

	e.activeVUs.Add(1)
	for r := range repeat {
		if r > 0 && e.isCircuitTripped() {
			// A trip inside a loop stops the remaining repeats; the worker exits
			// rather than sleep out the step's pause.
			e.activeVUs.Add(-1)
			return false
		}
		req, err := renderRequest(step, varCtx)
		if err != nil {
			e.collector.Add(metrics.Sample{Error: err, At: time.Now(), ScheduledAt: scheduledAt, StepName: step.Name, Group: step.Group})
			e.incFails()
			continue
		}

		result := exec.Execute(ctx, req)
		if isRunCancelled(ctx, result.Error) {
			e.activeVUs.Add(-1)
			return false
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
			TTFT:        result.TTFT,
		})

		if result.Error != nil {
			e.incFails()
		} else {
			e.recordSuccess()
		}
	}
	e.activeVUs.Add(-1)

	if step.Pause > 0 {
		select {
		case <-time.After(step.Pause):
		case <-ctx.Done():
			return false
		}
	}
	return true
}
