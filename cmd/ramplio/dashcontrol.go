package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/discover"
	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

// dashController implements dashboard.Controller, managing the load test lifecycle
// for tests triggered from the web UI or pre-started from CLI flags.
type dashController struct {
	mu                   sync.RWMutex
	state                dashboard.State
	result               *dashboard.RunResult
	cancel               context.CancelFunc
	snapCache            reporter.LiveSnapshot
	httpCfg              protocols.HTTPConfig
	scenarioMeta         *dashboard.ScenarioMeta
	pendingSteps         []engine.RampStep
	pendingSetupSteps    []engine.RampStep
	pendingTeardownSteps []engine.RampStep
	pendingStages        []scenarios.Stage
	pendingVars          map[string]string
	pendingDataRows      []map[string]string
	pendingDataMode      string
	lastProfile          *dashboard.GuidedProfile // non-nil while a guided test is running
	lastSummary          metrics.Summary
	lastSummarySet       bool

	discoverActive     bool
	discoverProbes     []dashboard.DiscoverProbeSnap
	discoverResult     *dashboard.DiscoverResultSnap
	discoverCurrentRPS int
	discoverProbeStart time.Time
	discoverProbeDur   time.Duration
	discoverProbeSeq   []int
}

func newDashController(httpCfg protocols.HTTPConfig) *dashController {
	return &dashController{
		state:   dashboard.StateIdle,
		httpCfg: httpCfg,
	}
}

// setScenario loads a YAML scenario into the controller so the browser can display
// its metadata and start it by sending POST /api/run with an empty body.
func (c *dashController) setScenario(
	meta *dashboard.ScenarioMeta,
	steps, setupSteps, teardownSteps []engine.RampStep,
	stages []scenarios.Stage,
	vars map[string]string,
	dataRows []map[string]string,
	dataMode string,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scenarioMeta = meta
	c.pendingSteps = steps
	c.pendingSetupSteps = setupSteps
	c.pendingTeardownSteps = teardownSteps
	c.pendingStages = stages
	c.pendingVars = vars
	c.pendingDataRows = dataRows
	c.pendingDataMode = dataMode
}

func (c *dashController) ActiveGuidedProfile() *dashboard.GuidedProfile {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastProfile
}

func (c *dashController) ScenarioInfo() *dashboard.ScenarioMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scenarioMeta
}

// LoadScenario parses raw YAML and replaces the active scenario. Rejected while
// a test is running so the browser always sees a consistent state.
// scenarioDir is used to resolve relative paths in vars_from; pass "" to use cwd.
func (c *dashController) LoadScenario(yaml []byte, scenarioDir string) error {
	c.mu.RLock()
	running := c.state == dashboard.StateRunning
	c.mu.RUnlock()
	if running {
		return fmt.Errorf("cannot load scenario while a test is running; stop it first")
	}

	sc, err := scenarios.Parse(bytes.NewReader(yaml))
	if err != nil {
		return fmt.Errorf("invalid scenario YAML: %w", err)
	}

	steps, stepNames := buildStepsFromScenario(sc)
	setupSteps, _ := buildStepsFromScenario(&scenarios.Scenario{Steps: sc.Setup})
	teardownSteps, _ := buildStepsFromScenario(&scenarios.Scenario{Steps: sc.Teardown})

	var dataRows []map[string]string
	var dataMode string
	if sc.VarsFrom != nil && sc.VarsFrom.File != "" {
		dataFile := sc.VarsFrom.File
		if !filepath.IsAbs(dataFile) {
			base := scenarioDir
			if base == "" {
				base, _ = os.Getwd()
			}
			dataFile = filepath.Join(base, dataFile)
		}
		rows, err := scenarios.LoadDataFile(dataFile)
		if err != nil {
			return fmt.Errorf("loading data file %q: %w", sc.VarsFrom.File, err)
		}
		dataRows = rows
		dataMode = sc.VarsFrom.Mode
	}

	maxVUs := maxTarget(sc.Stages)
	var totalSec float64
	for _, stg := range sc.Stages {
		totalSec += stg.Duration.Seconds()
	}
	meta := &dashboard.ScenarioMeta{
		Name:          sc.Name,
		StepNames:     stepNames,
		MaxVUs:        maxVUs,
		TotalSec:      totalSec,
		StageCount:    len(sc.Stages),
		SetupCount:    len(sc.Setup),
		TeardownCount: len(sc.Teardown),
	}
	c.setScenario(meta, steps, setupSteps, teardownSteps, sc.Stages, sc.Vars, dataRows, dataMode)
	return nil
}

func buildStepsFromScenario(sc *scenarios.Scenario) ([]engine.RampStep, []string) {
	steps := make([]engine.RampStep, len(sc.Steps))
	names := make([]string, len(sc.Steps))
	for i, s := range sc.Steps {
		name := s.Name
		if name == "" {
			name = strings.ToUpper(s.Method) + " " + s.URL
		}
		steps[i] = engine.RampStep{
			Name: name,
			Request: protocols.Request{
				Method:  strings.ToUpper(s.Method),
				URL:     s.URL,
				Headers: s.Headers,
				Body:    []byte(s.Body),
			},
			Assertions: s.Assertions,
			Auth:       s.Auth,
			Capture:    s.Capture,
			Pause:      s.Pause,
			Retry:      s.Retry,
			Group:      s.Group,
			Protocol:   s.Protocol,
			If:         s.If,
			Loop:       s.Loop,
		}
		if s.Capture != nil {
			compiled := make(map[string]*regexp.Regexp)
			for _, expr := range s.Capture.Values {
				if strings.HasPrefix(expr, "regex:") {
					pattern := strings.TrimPrefix(expr, "regex:")
					if re, err := regexp.Compile(pattern); err == nil {
						compiled[pattern] = re
					}
				}
			}
			if len(compiled) > 0 {
				steps[i].CompiledRegexes = compiled
			}
		}
		names[i] = name
	}
	return steps, names
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
// If a scenario was pre-loaded via setScenario(), it runs in scenario mode (the
// request body from the browser is ignored). Otherwise it uses URL mode.
func (c *dashController) Start(req dashboard.RunRequest) error {
	// Guided wizard mode takes priority when a profile is attached.
	if req.Profile != nil {
		return c.startGuided(req.Profile)
	}

	// Read scenario mode flag without holding write lock so validateRunRequest
	// (which is pure computation) can run outside the critical section.
	c.mu.RLock()
	isScenario := c.scenarioMeta != nil
	c.mu.RUnlock()

	if !isScenario {
		if err := validateRunRequest(req); err != nil {
			return err
		}
	}

	c.mu.Lock()
	if c.state == dashboard.StateRunning {
		c.mu.Unlock()
		return fmt.Errorf("a test is already running; stop it first")
	}

	var (
		col  *metrics.Collector
		ramp *engine.RampEngine
	)

	if c.scenarioMeta != nil {
		stages := c.pendingStages
		maxVUs := c.scenarioMeta.MaxVUs

		if req.OverrideVUs > 0 || req.OverrideDuration != "" {
			vus := req.OverrideVUs
			if vus <= 0 {
				vus = maxVUs
			}
			if vus <= 0 {
				vus = 1
			}
			var totalDur time.Duration
			if req.OverrideDuration != "" {
				totalDur, _ = time.ParseDuration(req.OverrideDuration)
			}
			if totalDur <= 0 {
				for _, s := range c.pendingStages {
					totalDur += s.Duration
				}
			}
			stages = buildOverrideStages(vus, totalDur)
			maxVUs = vus
		}

		if maxVUs <= 0 {
			maxVUs = 1
		}
		col = metrics.NewCollector(maxVUs)
		ramp = engine.NewRamp(engine.RampConfig{
			Stages:        stages,
			Steps:         c.pendingSteps,
			SetupSteps:    c.pendingSetupSteps,
			TeardownSteps: c.pendingTeardownSteps,
			Vars:          c.pendingVars,
			DataRows:      c.pendingDataRows,
			DataMode:      c.pendingDataMode,
			Executor:      protocols.NewHTTPExecutor(c.httpCfg),
			WSExecutor:    protocols.NewWSExecutor(),
		}, col)
	} else if req.RPS > 0 {
		dur, _ := time.ParseDuration(req.Duration) // validated above
		method := strings.ToUpper(req.Method)
		if method == "" {
			method = http.MethodGet
		}
		rampDur := dur / 4
		if rampDur < time.Second {
			rampDur = time.Second
		}
		holdDur := dur - 2*rampDur
		stgs := []scenarios.Stage{
			{Duration: rampDur, TargetRPS: req.RPS},
			{Duration: holdDur, TargetRPS: req.RPS},
			{Duration: rampDur, TargetRPS: 0},
		}
		steps := []engine.RampStep{{
			Request: protocols.Request{Method: method, URL: req.URL},
		}}
		workerCount := req.RPS * 5
		if workerCount < 10 {
			workerCount = 10
		}
		if workerCount > 5000 {
			workerCount = 5000
		}
		col = metrics.NewCollector(workerCount)
		ramp = engine.NewRamp(engine.RampConfig{
			Stages:   stgs,
			Steps:    steps,
			Executor: protocols.NewHTTPExecutor(c.httpCfg),
		}, col)
	} else {
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
		col = metrics.NewCollector(vus)
		ramp = engine.NewRamp(engine.RampConfig{
			Stages:   stgs,
			Steps:    steps,
			Executor: protocols.NewHTTPExecutor(c.httpCfg),
		}, col)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.state = dashboard.StateRunning
	c.result = nil
	c.snapCache = reporter.LiveSnapshot{}
	c.discoverActive = false
	c.discoverProbes = nil
	c.discoverResult = nil
	startedAt := time.Now()
	c.mu.Unlock()

	go c.runLoop(ctx, col, ramp, startedAt)
	return nil
}

// startGuided launches a test translated from a PM-facing GuidedProfile.
func (c *dashController) startGuided(p *dashboard.GuidedProfile) error {
	if p.URL == "" {
		return fmt.Errorf("url is required")
	}

	plan := dashboard.TranslateProfile(*p)
	method := strings.ToUpper(p.Method)
	if method == "" {
		method = dashboard.GuidedMethod(p.ScenarioKind)
	}
	steps := []engine.RampStep{{
		Request: protocols.Request{Method: method, URL: p.URL},
	}}

	c.mu.Lock()
	if c.state == dashboard.StateRunning {
		c.mu.Unlock()
		return fmt.Errorf("a test is already running; stop it first")
	}

	col := metrics.NewCollector(plan.MaxVUs)
	ramp := engine.NewRamp(engine.RampConfig{
		Stages:   plan.Stages,
		Steps:    steps,
		Executor: protocols.NewHTTPExecutor(c.httpCfg),
	}, col)

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.state = dashboard.StateRunning
	c.result = nil
	c.snapCache = reporter.LiveSnapshot{}
	c.lastProfile = p
	c.discoverActive = false
	c.discoverProbes = nil
	c.discoverResult = nil
	startedAt := time.Now()
	c.mu.Unlock()

	go c.runLoop(ctx, col, ramp, startedAt)
	return nil
}

func validateRunRequest(req dashboard.RunRequest) error {
	if req.URL == "" {
		return fmt.Errorf("url is required")
	}
	if req.RPS > 0 && req.VUs > 1 {
		return fmt.Errorf("vus and rps are mutually exclusive")
	}
	if req.RPS < 0 || req.RPS > 100000 {
		return fmt.Errorf("rps must be between 1 and 100000")
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
			result := &dashboard.RunResult{
				Total:    sum.Total,
				Errors:   sum.Errors,
				P50Ms:    sum.P50.Milliseconds(),
				P90Ms:    sum.P90.Milliseconds(),
				P95Ms:    sum.P95.Milliseconds(),
				P99Ms:    sum.P99.Milliseconds(),
				ErrorPct: errPct,
				MeanMs:   sum.MeanLatency().Milliseconds(),
				RPS:      sum.RPS(),
				WallSec:  sum.WallTime.Seconds(),
				Verdict:  reporter.Interpret(sum),
			}
			if c.lastProfile != nil {
				verdict := dashboard.InterpretResult(*c.lastProfile, *result)
				result.GuidedVerdict = &verdict
				c.lastProfile = nil
			}
			c.result = result
			c.lastSummary = sum
			c.lastSummarySet = true
			c.mu.Unlock()
			return
		}
	}
}

// buildOverrideStages creates a simple 3-stage ramp-hold-ramp profile for
// scenario mode when the user overrides VUs or duration from the dashboard.
func buildOverrideStages(vus int, total time.Duration) []scenarios.Stage {
	ramp := total / 4
	if ramp < time.Second {
		ramp = time.Second
	}
	hold := total - 2*ramp
	if hold < time.Second {
		hold = time.Second
	}
	return []scenarios.Stage{
		{Duration: ramp, Target: vus},
		{Duration: hold, Target: vus},
		{Duration: ramp, Target: 0},
	}
}

func (c *dashController) WriteReport(w io.Writer) error {
	c.mu.RLock()
	sum := c.lastSummary
	set := c.lastSummarySet
	c.mu.RUnlock()
	if !set {
		return fmt.Errorf("no completed test run")
	}
	return reporter.WriteHTML(w, sum)
}

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
		StepMetrics:  col.LiveStepMetrics(),
		GroupMetrics: col.LiveGroupMetrics(),
	}
	c.mu.Lock()
	c.snapCache = snap
	c.mu.Unlock()
}
