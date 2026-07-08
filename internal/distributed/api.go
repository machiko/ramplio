package distributed

import "github.com/machiko/ramplio/v3/internal/metrics"

// AssignRequest represents the request to assign work to a worker.
type AssignRequest struct {
	ScenarioYAML  []byte            `json:"scenario_yaml"`
	AssignedVUs   int               `json:"assigned_vus"`
	SetupCaptures map[string]string `json:"setup_captures"`
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
}
