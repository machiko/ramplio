package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/observe"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

// dashController implements dashboard.Controller, managing the load test lifecycle
// for tests triggered from the web UI or pre-started from CLI flags.
//
// 職責界線(td-2 收斂):internal/dashboard 只負責傳輸(HTTP/WS/JSON 契約)
// 與快照型別;engine 編排一律在 cmd 層的 dashController。本型別依職責拆檔:
//   - dashcontrol.go           run 生命週期(Start/Stop/runLoop/快照)
//   - dashcontrol_scenario.go  場景載入(轉換與 CLI 單一來源)
//   - dashcontrol_baseline.go  基準比較的保存與查詢
//   - dashcontrol_discover.go  容量探測生命週期
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

	// observeSrc 來自伺服器啟動時的 --observe(nil = 未啟用);
	// obsRampDur/obsHoldDur 是本次 run 的觀測窗口,holdDur=0 表示不適用(非 rate 模式)。
	observeSrc observe.TraceSource
	obsRampDur time.Duration
	obsHoldDur time.Duration

	// pendingBaseline 是 GUI 上傳的待比較基準(nil = 未載入);run 結束後
	// 保留,使用者可連跑多次與同一基準比較。runIdent 是本次 run 的場景識別,
	// 供 FromSummary 填 Scenario(與 CLI 規則對齊:場景名,否則 URL)。
	pendingBaseline *baseline.Baseline
	runIdent        string

	discoverActive     bool
	discoverProbes     []dashboard.DiscoverProbeSnap
	discoverResult     *dashboard.DiscoverResultSnap
	discoverCurrentRPS int
	discoverProbeStart time.Time
	discoverProbeDur   time.Duration
	discoverProbeSeq   []int
}

func newDashController(httpCfg protocols.HTTPConfig, observeSrc observe.TraceSource) *dashController {
	return &dashController{
		state:      dashboard.StateIdle,
		httpCfg:    httpCfg,
		observeSrc: observeSrc,
	}
}

func (c *dashController) ActiveGuidedProfile() *dashboard.GuidedProfile {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastProfile
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

	// 每次 run 重置觀測窗口;僅 rate 分支會重新設定(其他模式不適用)
	c.obsRampDur, c.obsHoldDur = 0, 0
	// 場景識別與 CLI 規則對齊:URL 模式用 URL;scenario 分支下面覆蓋為場景名
	c.runIdent = req.URL

	switch {
	case c.scenarioMeta != nil:
		c.runIdent = c.scenarioMeta.Name
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
	case req.RPS > 0:
		dur, _ := time.ParseDuration(req.Duration) // validated above
		method := strings.ToUpper(req.Method)
		if method == "" {
			method = http.MethodGet
		}
		// 觀測窗口只在 rate 模式有意義(負載輪廓提供基準/臨界窗)
		c.obsRampDur, c.obsHoldDur = rateProfile(dur)
		// 與 CLI 共用 rateStages:此處曾自行實作窗口數學且缺負值鉗制,
		// 短 duration 會把負時長 stage 送進 engine。
		stgs := rateStages(req.RPS, dur)
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
	default:
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

	// guided 一律不觀測:窗口若殘留上一輪 rate run 的值,
	// 會對本輪誤觸發觀測且窗口與 guided 的 stage 配置毫無對應。
	c.obsRampDur, c.obsHoldDur = 0, 0
	c.runIdent = p.URL

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

			// 觀測(網路 I/O,秒級)必須在取鎖前完成,不可鎖內拉 trace。
			// ctx 已取消 = 使用者主動 Stop:窗口是照原計畫時長推導的,
			// 提前中止後與實際負載不符,觀測數字不可信,直接跳過——
			// 也避免 Stop 後還卡最長 30 秒(兩窗各 15s 逾時)才進 done。
			var obsSnap *dashboard.ObserveSnap
			c.mu.RLock()
			obsSrc, obsRamp, obsHold := c.observeSrc, c.obsRampDur, c.obsHoldDur
			c.mu.RUnlock()
			if obsSrc != nil && obsHold > 0 && ctx.Err() == nil {
				if analysis, truncated, obsErr := fetchAndAnalyze(obsSrc, startedAt, obsRamp, obsHold); obsErr == nil {
					snap := dashboard.ObserveSnapFrom(analysis, truncated)
					obsSnap = &snap
				} else {
					fmt.Fprintf(os.Stderr, "warning: %v,結果頁略過觀測卡片\n", obsErr)
				}
			}

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
			result.Observe = obsSnap
			// 與已上傳基準比較(純計算,鎖內安全)。失敗只警告不掛卡片——
			// 比較是結果的補充,不可污染主流程(比照 observe/sink 慣例)。
			if c.pendingBaseline != nil {
				after := baseline.FromSummary(sum, c.runIdent)
				if cmp, cmpErr := baseline.Compare(*c.pendingBaseline, after, baseline.DefaultTolerance()); cmpErr == nil {
					snap := dashboard.CompareSnapFrom(cmp, *c.pendingBaseline)
					result.Compare = &snap
				} else {
					fmt.Fprintf(os.Stderr, "warning: 基準比較失敗:%v,結果頁略過比較卡片\n", cmpErr)
				}
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
