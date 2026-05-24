package dashboard

import "github.com/ramplio/ramplio/internal/reporter"

// State is the lifecycle state of a dashboard-controlled load test.
type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateDone    State = "done"
)

// RunRequest describes a URL-based load test started from the web UI.
type RunRequest struct {
	URL      string `json:"url"`
	Method   string `json:"method"`
	VUs      int    `json:"vus"`
	Duration string `json:"duration"`
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
}

// ScenarioMeta holds display metadata for a YAML scenario loaded via --scenario flag.
type ScenarioMeta struct {
	Name       string   `json:"name"`
	StepNames  []string `json:"step_names"`
	MaxVUs     int      `json:"max_vus"`
	TotalSec   float64  `json:"total_sec"`
	StageCount int      `json:"stage_count"`
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
	// Returns an error if the YAML is invalid or a test is already running.
	LoadScenario(yaml []byte) error
}
