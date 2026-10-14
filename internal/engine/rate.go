package engine

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"golang.org/x/time/rate"
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
				TTFT:        result.TTFT,
			})

			if result.Error != nil {
				e.incFails()
			} else if e.cbWindow != nil {
				e.cbWindow.reset()
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
