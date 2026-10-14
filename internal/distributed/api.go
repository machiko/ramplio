package distributed

import (
	"github.com/machiko/ramplio/v3/internal/metrics"
)

// CapabilityStageShares is reported on /health by workers that run the exact
// per-stage load share a coordinator sends. A coordinator refuses to start
// against a worker without it: an older worker ignores the shares and would
// run the full scenario load on its own.
const CapabilityStageShares = "stage_shares"

// AssignRequest represents the request to assign work to a worker.
type AssignRequest struct {
	ScenarioYAML []byte `json:"scenario_yaml"`
	// AssignedVUs is this worker's peak VU share. With StageShares it only sizes
	// the metrics collector; requests from older coordinators carry no shares,
	// and the worker then scales the scenario's VU targets by it instead.
	AssignedVUs   int               `json:"assigned_vus"`
	SetupCaptures map[string]string `json:"setup_captures"`
	// StageShares is this worker's exact share of every scenario stage,
	// computed by the coordinator so the cluster-wide sum equals the declared
	// load. Durations are not sent: they stay with the scenario YAML the worker
	// parses and validates itself.
	StageShares []StageShare `json:"stage_shares,omitempty"`
}

// StageShare is one worker's part of one scenario stage's load.
type StageShare struct {
	Target    int `json:"target"`
	TargetRPS int `json:"target_rps"`
}

// ResultResponse carries a worker's final, serialized histogram snapshot.
// The coordinator merges these via metrics.MergeExports to compute correct
// cluster-wide percentiles (averaging per-worker percentiles would be wrong).
type ResultResponse struct {
	WorkerID string                  `json:"worker_id"`
	Export   metrics.HistogramExport `json:"export"`
}

// LiveMetricsResponse represents live metrics from a worker.
type LiveMetricsResponse struct {
	WorkerID  string  `json:"worker_id"`
	Total     int64   `json:"total"`
	Errors    int64   `json:"errors"`
	RPS       float64 `json:"rps"`
	P99Ns     int64   `json:"p99_ns"`
	ActiveVUs int     `json:"active_vus"`
	Done      bool    `json:"done"`
}

// StatusResponse represents a simple status response.
type StatusResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	// Capabilities lists optional protocol features a worker supports
	// (reported on /health), e.g. CapabilityStageShares.
	Capabilities []string `json:"capabilities,omitempty"`
}
