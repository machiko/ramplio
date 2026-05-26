package distributed

// AssignRequest represents the request to assign work to a worker.
type AssignRequest struct {
	ScenarioYAML   []byte            `json:"scenario_yaml"`
	AssignedVUs    int               `json:"assigned_vus"`
	SetupCaptures  map[string]string `json:"setup_captures"`
}

// PartialStepSummary represents metrics for a single step from a worker.
type PartialStepSummary struct {
	Name    string `json:"name"`
	Total   int64  `json:"total"`
	Errors  int64  `json:"errors"`
	MinNs   int64  `json:"min_ns"`
	MaxNs   int64  `json:"max_ns"`
	P50Ns   int64  `json:"p50_ns"`
	P90Ns   int64  `json:"p90_ns"`
	P95Ns   int64  `json:"p95_ns"`
	P99Ns   int64  `json:"p99_ns"`
	BytesIn int64  `json:"bytes_in"`
}

// PartialSummary represents metrics summary from a single worker.
type PartialSummary struct {
	WorkerID       string                  `json:"worker_id"`
	Total          int64                   `json:"total"`
	Errors         int64                   `json:"errors"`
	MinNs          int64                   `json:"min_ns"`
	MaxNs          int64                   `json:"max_ns"`
	P50Ns          int64                   `json:"p50_ns"`
	P90Ns          int64                   `json:"p90_ns"`
	P95Ns          int64                   `json:"p95_ns"`
	P99Ns          int64                   `json:"p99_ns"`
	BytesIn        int64                   `json:"bytes_in"`
	WallNs         int64                   `json:"wall_ns"`
	DroppedSamples int64                   `json:"dropped_samples"`
	Steps          []PartialStepSummary    `json:"steps,omitempty"`
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
