package engine

// RampEngine 的 VU 模式(封閉模型):每個 VU 一個 goroutine 循環執行步驟,
// stage 迴圈以線性插值增減 VU 數。

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

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

func (e *RampEngine) runVU(ctx context.Context) {
	// Each VU gets its own cookie jar so session cookies persist across steps.
	sessionExec := protocols.Executor(e.cfg.Executor)
	if h, ok := e.cfg.Executor.(*protocols.HTTPExecutor); ok {
		sessionExec = h.NewSession()
	}

	// ws_mode: persistent 的 per-VU 連線持有者(比照 HTTP cookie jar 的
	// session 先例)。注入的假執行器沒有連線概念,wsSession 維持 nil,
	// stepExecutor 自然退回逐請求呼叫。VU 結束時關閉全部連線。
	var wsSession *protocols.WSSession
	if w, ok := e.wsBase().(*protocols.WSExecutor); ok {
		wsSession = w.NewSession()
		defer func() { _ = wsSession.Close() }()
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

			exec := e.stepExecutor(step, sessionExec, wsSession)

			for r := 0; r < repeat; r++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if r > 0 && e.isCircuitTripped() {
					return // a trip inside a loop stops the remaining repeats
				}

				req, err := renderRequest(step, varCtx)
				if err != nil {
					e.collector.Add(metrics.Sample{Error: err, At: time.Now(), StepName: step.Name, Group: step.Group})
					e.incFails()
					continue
				}

				result := exec.Execute(ctx, req)
				if isRunCancelled(ctx, result.Error) {
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
					TTFT:       result.TTFT,
				})

				if result.Error != nil {
					e.incFails()
				} else {
					e.recordSuccess()
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
