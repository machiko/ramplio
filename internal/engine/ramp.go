package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/scenarios"
	"github.com/tidwall/gjson"
)

const rampTickInterval = 100 * time.Millisecond

// RampStep pairs a request template with optional assertions, auth, and capture config.
type RampStep struct {
	Request    protocols.Request
	Assertions *scenarios.Assertions
	Auth       *scenarios.Auth
	Capture    *scenarios.Capture
}

// RampConfig drives a stage-based load test.
type RampConfig struct {
	Stages   []scenarios.Stage
	Steps    []RampStep
	Vars     map[string]string // scenario-level vars available for template rendering
	Executor protocols.Executor
}

// RampEngine runs multi-stage load with linear VU interpolation between stages.
type RampEngine struct {
	cfg            RampConfig
	collector      *metrics.Collector
	activeVUs      atomic.Int32
	stageCurrent   atomic.Int32
	stageTotal     atomic.Int32
	stageStartedAt atomic.Value // stores time.Time
	stageDurNs     atomic.Int64
}

func NewRamp(cfg RampConfig, collector *metrics.Collector) *RampEngine {
	e := &RampEngine{cfg: cfg, collector: collector}
	e.stageTotal.Store(int32(len(cfg.Stages)))
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

func (e *RampEngine) Run(ctx context.Context) metrics.Summary {
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
	varCtx := &scenarios.VarContext{
		Vars:     e.cfg.Vars,
		Captures: make(map[string]string),
	}
	stepIdx := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
			step := e.cfg.Steps[stepIdx%len(e.cfg.Steps)]
			stepIdx++

			req, err := renderRequest(step, varCtx)
			if err != nil {
				e.collector.Add(metrics.Sample{Error: err, At: time.Now()})
				continue
			}

			result := e.cfg.Executor.Execute(ctx, req)
			if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, context.DeadlineExceeded) {
				return
			}

			if result.Error == nil {
				applyCaptures(step.Capture, result, varCtx)
				if step.Assertions != nil {
					if err := scenarios.EvalAssertions(step.Assertions, result); err != nil {
						result.Error = err
					}
				}
			}

			e.collector.Add(metrics.Sample{
				Latency:    result.Latency,
				StatusCode: result.StatusCode,
				BytesRead:  result.BytesRead,
				Error:      result.Error,
				At:         time.Now(),
			})
		}
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
func applyCaptures(cap *scenarios.Capture, result protocols.Result, ctx *scenarios.VarContext) {
	if cap == nil {
		return
	}
	for key, expr := range cap.Values {
		if after, ok := strings.CutPrefix(expr, "header:"); ok {
			ctx.Captures[key] = result.ResponseHeaders[http.CanonicalHeaderKey(after)]
		} else {
			ctx.Captures[key] = gjson.GetBytes(result.Body, scenarios.JSONPathToGJSON(expr)).String()
		}
	}
}

func basicAuthHeader(username, password string) string {
	creds := username + ":" + password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}

