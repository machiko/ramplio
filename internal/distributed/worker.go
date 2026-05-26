package distributed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ramplio/ramplio/internal/engine"
	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/scenarios"
)

// WorkerState represents the current state of a worker.
type WorkerState string

const (
	WorkerStateIdle    WorkerState = "idle"
	WorkerStateRunning WorkerState = "running"
	WorkerStateDone    WorkerState = "done"
)

// Worker represents a local load-generation worker that accepts scenarios via HTTP
// and reports metrics back to a coordinator.
type Worker struct {
	id        string
	state     WorkerState
	mu        sync.RWMutex
	cancel    context.CancelFunc
	engine    *engine.RampEngine
	collector *metrics.Collector
	result    *metrics.Summary
	startedAt time.Time
}

// NewWorker creates a new worker with the given ID.
func NewWorker(id string) *Worker {
	return &Worker{
		id:    id,
		state: WorkerStateIdle,
	}
}

// Assign assigns a scenario to the worker and starts running it.
// Returns 409 Conflict if the worker is already running.
func (w *Worker) Assign(ctx context.Context, req *AssignRequest) error {
	w.mu.Lock()

	// If already running, return conflict
	if w.state == WorkerStateRunning {
		w.mu.Unlock()
		return fmt.Errorf("worker already running")
	}

	// Parse scenario
	sc, err := scenarios.Parse(bytes.NewReader(req.ScenarioYAML))
	if err != nil {
		w.mu.Unlock()
		return fmt.Errorf("failed to parse scenario: %w", err)
	}

	// Scale VU counts based on assigned VUs
	scaleScenario(sc, req.AssignedVUs)

	// Convert scenario steps to engine steps
	steps := scenarioStepsToEngineSteps(sc.Steps)
	setupSteps := scenarioStepsToEngineSteps(sc.Setup)
	teardownSteps := scenarioStepsToEngineSteps(sc.Teardown)

	// Build engine config from scenario
	cfg := engine.RampConfig{
		Stages:         sc.Stages,
		Steps:          steps,
		SetupSteps:     setupSteps,
		TeardownSteps:  teardownSteps,
		Vars:           sc.Vars,
		CircuitBreaker: sc.CircuitBreaker,
		Executor:       protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
		WSExecutor:     protocols.NewWSExecutor(),
	}

	// Create collector
	collector := metrics.NewCollector(req.AssignedVUs)
	w.collector = collector

	// Create engine
	eng := engine.NewRamp(cfg, collector)
	w.engine = eng

	// Start the engine in a background goroutine
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.startedAt = time.Now()
	w.state = WorkerStateRunning
	w.mu.Unlock() // Release lock before starting goroutine

	go func() {
		result := w.engine.Run(runCtx)
		w.collector.Stop()

		w.mu.Lock()
		w.result = &result
		w.state = WorkerStateDone
		w.mu.Unlock()
	}()

	return nil
}

// Stop gracefully stops the running worker.
func (w *Worker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cancel != nil {
		w.cancel()
	}
}

// GetState returns the current worker state.
func (w *Worker) GetState() WorkerState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// GetLiveMetrics returns the current live metrics.
func (w *Worker) GetLiveMetrics() *LiveMetricsResponse {
	w.mu.RLock()
	defer w.mu.RUnlock()

	activeVUs := 0
	if w.engine != nil {
		activeVUs = w.engine.ActiveVUs()
	}

	rps := 0.0
	p99 := int64(0)
	total := int64(0)
	errors := int64(0)

	if w.collector != nil {
		snap := w.collector.LiveSummary()
		total = snap.Total
		errors = snap.Errors
		if w.startedAt != (time.Time{}) {
			elapsed := time.Since(w.startedAt).Seconds()
			if elapsed > 0 {
				rps = float64(total) / elapsed
			}
		}
		// Get live percentiles
		_, _, _, p99Dur := w.collector.LivePercentiles()
		p99 = p99Dur.Nanoseconds()
	}

	return &LiveMetricsResponse{
		WorkerID:  w.id,
		Total:     total,
		Errors:    errors,
		RPS:       rps,
		P99Ns:     p99,
		ActiveVUs: activeVUs,
		Done:      w.state == WorkerStateDone,
	}
}

// GetResult returns the final result summary.
func (w *Worker) GetResult() *PartialSummary {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if w.result == nil {
		return nil
	}

	summary := &PartialSummary{
		WorkerID:       w.id,
		Total:          w.result.Total,
		Errors:         w.result.Errors,
		MinNs:          w.result.MinLatency.Nanoseconds(),
		MaxNs:          w.result.MaxLatency.Nanoseconds(),
		P50Ns:          w.result.P50.Nanoseconds(),
		P90Ns:          w.result.P90.Nanoseconds(),
		P95Ns:          w.result.P95.Nanoseconds(),
		P99Ns:          w.result.P99.Nanoseconds(),
		BytesIn:        w.result.BytesIn,
		WallNs:         w.result.WallTime.Nanoseconds(),
		DroppedSamples: w.result.DroppedSamples,
	}

	// Convert step summaries
	if len(w.result.Steps) > 0 {
		summary.Steps = make([]PartialStepSummary, len(w.result.Steps))
		for i, step := range w.result.Steps {
			summary.Steps[i] = PartialStepSummary{
				Name:    step.Name,
				Total:   step.Total,
				Errors:  step.Errors,
				P50Ns:   step.P50.Nanoseconds(),
				P90Ns:   step.P90.Nanoseconds(),
				P95Ns:   step.P95.Nanoseconds(),
				P99Ns:   step.P99.Nanoseconds(),
			}
		}
	}

	return summary
}

// StartHTTPServer starts the HTTP server for the worker.
func (w *Worker) StartHTTPServer(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /assign", w.handleAssign)
	mux.HandleFunc("POST /stop", w.handleStop)
	mux.HandleFunc("GET /live", w.handleLive)
	mux.HandleFunc("GET /result", w.handleResult)
	mux.HandleFunc("GET /health", w.handleHealth)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return server.ListenAndServe()
}

// HTTP Handlers

func (w *Worker) handleAssign(rw http.ResponseWriter, r *http.Request) {
	var req AssignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, fmt.Sprintf("failed to decode request: %v", err), http.StatusBadRequest)
		return
	}

	if err := w.Assign(r.Context(), &req); err != nil {
		if err.Error() == "worker already running" {
			http.Error(rw, "worker already running", http.StatusConflict)
			return
		}
		http.Error(rw, fmt.Sprintf("assignment failed: %v", err), http.StatusBadRequest)
		return
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(&StatusResponse{OK: true})
}

func (w *Worker) handleStop(rw http.ResponseWriter, r *http.Request) {
	w.Stop()
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(&StatusResponse{OK: true})
}

func (w *Worker) handleLive(rw http.ResponseWriter, r *http.Request) {
	metrics := w.GetLiveMetrics()
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(metrics)
}

func (w *Worker) handleResult(rw http.ResponseWriter, r *http.Request) {
	result := w.GetResult()
	if result == nil {
		http.Error(rw, "no result available yet", http.StatusNotFound)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(result)
}

func (w *Worker) handleHealth(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(&StatusResponse{OK: true, Message: "healthy"})
}

// scaleScenario scales the scenario's VU counts based on the assigned VUs.
// If the original scenario has max target VU T, and we're assigning A VUs,
// then we scale all stage targets by ratio A/T.
func scaleScenario(sc *scenarios.Scenario, assignedVUs int) {
	if assignedVUs == 0 || len(sc.Stages) == 0 {
		return
	}

	// Find the maximum target VU count in the original scenario
	maxTarget := 0
	for _, stage := range sc.Stages {
		if stage.Target > maxTarget {
			maxTarget = stage.Target
		}
	}

	if maxTarget == 0 {
		return
	}

	// Calculate scaling ratio
	ratio := float64(assignedVUs) / float64(maxTarget)

	// Scale all stage targets
	for i := range sc.Stages {
		if sc.Stages[i].Target > 0 {
			sc.Stages[i].Target = int(math.Round(float64(sc.Stages[i].Target) * ratio))
		}
	}
}

// scenarioStepsToEngineSteps converts scenario.Step to engine.RampStep
func scenarioStepsToEngineSteps(steps []scenarios.Step) []engine.RampStep {
	out := make([]engine.RampStep, len(steps))
	for i, s := range steps {
		name := s.Name
		if name == "" {
			name = strings.ToUpper(s.Method) + " " + s.URL
		}
		hdrs := s.Headers
		if hdrs == nil {
			hdrs = make(map[string]string)
		}
		// Inject WS expect as a synthetic header so WSExecutor can check it.
		if strings.EqualFold(s.Protocol, "websocket") && s.WSExpect != "" {
			hdrs["X-WS-Expect"] = s.WSExpect
		}
		body := []byte(s.Body)
		if strings.EqualFold(s.Protocol, "websocket") && s.WSMessage != "" && s.Body == "" {
			body = []byte(s.WSMessage)
		}
		out[i] = engine.RampStep{
			Name: name,
			Request: protocols.Request{
				Method:  strings.ToUpper(s.Method),
				URL:     s.URL,
				Headers: hdrs,
				Body:    body,
			},
			Assertions: s.Assertions,
			Auth:       s.Auth,
			Capture:    s.Capture,
			Retry:      s.Retry,
			Pause:      s.Pause,
			Group:      s.Group,
			Protocol:   s.Protocol,
			If:         s.If,
			Loop:       s.Loop,
		}
		if s.Capture != nil {
			if compiled := precompileRegexes(s.Capture.Values); len(compiled) > 0 {
				out[i].CompiledRegexes = compiled
			}
		}
	}
	return out
}

// precompileRegexes builds a map of pattern → *regexp.Regexp for all regex: capture expressions.
func precompileRegexes(values map[string]string) map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp)
	for _, expr := range values {
		if strings.HasPrefix(expr, "regex:") {
			pattern := strings.TrimPrefix(expr, "regex:")
			if re, err := regexp.Compile(pattern); err == nil {
				out[expr] = re
			}
		}
	}
	return out
}
