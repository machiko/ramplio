// Package dashboard 只負責傳輸——HTTP/WS 端點、JSON 契約與 GUI 快照型別
// (*_snap.go 與 guided.go 的轉換皆屬呈現層)。engine 編排(啟動/停止/
// 觀測/比較的執行)一律在 cmd 層的 dashController,透過 Controller 介面
// 注入;本套件不得 import engine(td-2 職責界線)。
package dashboard

import (
	"io"

	"github.com/machiko/ramplio/v3/internal/reporter"
)

// State is the lifecycle state of a dashboard-controlled load test.
type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateDone    State = "done"
)

// RunRequest describes a URL-based load test started from the web UI.
// Either VUs, RPS, or Profile must be set; the three modes are mutually exclusive.
// OverrideVUs and OverrideDuration are optional and apply only in scenario mode:
// they rebuild the stages while keeping the imported steps.
type RunRequest struct {
	URL              string         `json:"url"`
	Method           string         `json:"method"`
	VUs              int            `json:"vus"`
	RPS              int            `json:"rps"`
	Duration         string         `json:"duration"`
	Profile          *GuidedProfile `json:"profile,omitempty"`
	OverrideVUs      int            `json:"override_vus,omitempty"`
	OverrideDuration string         `json:"override_duration,omitempty"`
}

// RunResult holds aggregate metrics after a test completes.
type RunResult struct {
	Total    int64   `json:"total"`
	Errors   int64   `json:"errors"`
	P50Ms    int64   `json:"p50_ms"`
	P90Ms    int64   `json:"p90_ms"`
	P95Ms    int64   `json:"p95_ms"`
	P99Ms    int64   `json:"p99_ms"`
	ErrorPct float64 `json:"error_pct"`
	MeanMs   int64   `json:"mean_ms"`
	RPS      float64 `json:"rps"`
	WallSec  float64 `json:"wall_sec"`
	// CorrectedP50Ms/P99Ms 是 rate 模式的使用者實感延遲(CO 修正,含排隊
	// 等待);零值 = VU 模式不適用。前端據此把 headline 延遲標示為
	// 「伺服器處理」並另列實感值——與 TTFT 卡的「完整回應」同基準,
	// 否則同屏兩個 p99(原始 vs 實感)互相衝突。
	CorrectedP50Ms int64 `json:"corrected_p50_ms,omitempty"`
	CorrectedP99Ms int64 `json:"corrected_p99_ms,omitempty"`
	// Verdict is the shared plain-language reading (same source as the terminal,
	// JSON and HTML outputs) so the browser speaks identical wording to the CLI.
	Verdict       reporter.Interpretation `json:"verdict"`
	GuidedVerdict *GuidedVerdict          `json:"guided_verdict,omitempty"` // set when started via guided wizard
	// Observe 是 trace 瓶頸關聯結果(rate 模式 + 伺服器啟動時帶 --observe 才有)。
	Observe *ObserveSnap `json:"observe,omitempty"`
	// Compare 是與已上傳基準的容量回歸比較(有載入 baseline 才有)。
	Compare *CompareSnap `json:"compare,omitempty"`
	// TTFT 是串流回應量測(場景含 stream: sse 步驟才有)。
	TTFT *TTFTSnap `json:"ttft,omitempty"`
}

// ScenarioMeta holds display metadata for a YAML scenario loaded via --scenario flag.
type ScenarioMeta struct {
	Name          string   `json:"name"`
	StepNames     []string `json:"step_names"`
	MaxVUs        int      `json:"max_vus"`
	TotalSec      float64  `json:"total_sec"`
	StageCount    int      `json:"stage_count"`
	SetupCount    int      `json:"setup_count,omitempty"`
	TeardownCount int      `json:"teardown_count,omitempty"`
}

// DiscoverRequest describes a capacity discovery run started from the web UI.
type DiscoverRequest struct {
	URL           string `json:"url"`
	Tolerance     string `json:"tolerance"`      // e.g. "2s", "500ms"; defaults to "2s"
	MaxRPS        int    `json:"max_rps"`        // default 500
	ProbeDuration string `json:"probe_duration"` // e.g. "15s"; default "15s"
}

// DiscoverProbeSnap is a single probe result pushed over WebSocket.
type DiscoverProbeSnap struct {
	RPS      int     `json:"rps"`
	P99Ms    int64   `json:"p99_ms"`
	ErrorPct float64 `json:"error_pct"`
	Status   string  `json:"status"` // "pass", "warn", "fail"
}

// DiscoverResultSnap is the final capacity report pushed over WebSocket.
type DiscoverResultSnap struct {
	SafeLimit     int  `json:"safe_limit"`
	BreakingPoint int  `json:"breaking_point"`
	Exhausted     bool `json:"exhausted"`
}

// DiscoverCurrentSnap describes the probe currently in progress.
type DiscoverCurrentSnap struct {
	RPS             int   `json:"rps"`
	ElapsedMs       int64 `json:"elapsed_ms"`
	ProbeDurationMs int64 `json:"probe_duration_ms"`
}

// Controller extends LiveProvider with start/stop lifecycle control for the web dashboard.
type Controller interface {
	reporter.LiveProvider
	// Start launches a new load test in the background. Returns an error if a test
	// is already running or if the RunRequest is invalid.
	Start(req RunRequest) error
	// Stop cancels the currently running test, if any.
	Stop()
	// State returns the current lifecycle state.
	State() State
	// Result returns the final RunResult after a test completes, or nil if not done.
	Result() *RunResult
	// ScenarioInfo returns metadata about the loaded YAML scenario, or nil in URL mode.
	ScenarioInfo() *ScenarioMeta
	// LoadScenario parses raw YAML content and loads it as the active scenario.
	// scenarioDir is used to resolve relative paths (e.g. vars_from file); pass "" for cwd.
	// Returns an error if the YAML is invalid or a test is already running.
	LoadScenario(yaml []byte, scenarioDir string) error
	// LoadScenarioWithData loads a scenario whose companion data comes from an
	// in-memory CSV string rather than a disk file, so a generated scenario can be
	// run directly from the browser without the data file ever touching disk.
	// An empty dataCSV means the scenario has no data-driven parameters.
	// Returns an error if the YAML or CSV is invalid or a test is already running.
	LoadScenarioWithData(yaml []byte, dataCSV string) error
	// ActiveGuidedProfile returns the GuidedProfile for the currently running guided test,
	// or nil when the test was not started via the wizard.
	ActiveGuidedProfile() *GuidedProfile
	// WriteReport generates an HTML report for the last completed test and writes it to w.
	// Returns an error if no test has completed yet.
	WriteReport(w io.Writer) error
	// LoadBaseline parses uploaded baseline JSON and holds it for post-run comparison.
	// Returns a summary of the loaded baseline, or an error if the data is invalid.
	LoadBaseline(raw []byte) (BaselineInfo, error)
	// ClearBaseline removes the held baseline; subsequent runs are not compared.
	ClearBaseline()
	// BaselineMeta returns the held baseline's summary, or nil when none is loaded.
	BaselineMeta() *BaselineInfo
	// StartDiscover launches a capacity discovery probe in the background.
	// Returns an error if a test is already running or if req is invalid.
	StartDiscover(req DiscoverRequest) error
	// DiscoverProgress returns accumulated probe results, the final result (non-nil when done),
	// the currently running probe (non-nil while a probe is in progress), the planned probe
	// sequence, and whether the controller is currently in discover mode.
	DiscoverProgress() (probes []DiscoverProbeSnap, result *DiscoverResultSnap, current *DiscoverCurrentSnap, probeSeq []int, active bool)
}
