package distributed

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramplio/ramplio/internal/scenarios"
	"github.com/stretchr/testify/assert"
)

// TestWorkerStateMachine tests the worker state transitions
func TestWorkerStateMachine(t *testing.T) {
	w := NewWorker("test-worker")
	assert.Equal(t, WorkerStateIdle, w.GetState())

	// Attempting to get result in idle state should return nil
	result := w.GetResult()
	assert.Nil(t, result)

	// Get live metrics should return empty
	metrics := w.GetLiveMetrics()
	assert.NotNil(t, metrics)
	assert.Equal(t, "test-worker", metrics.WorkerID)
	assert.Equal(t, false, metrics.Done)
}

// TestWorkerAssignDuplicate tests that assigning twice returns 409
func TestWorkerAssignDuplicate(t *testing.T) {
	w := NewWorker("test-worker")
	ctx := context.Background()

	// Create a minimal scenario
	yamlContent := `
name: test
stages:
  - duration: 1s
    target: 1
steps:
  - name: GET test
    method: GET
    url: http://example.com
`

	req := &AssignRequest{
		ScenarioYAML: []byte(yamlContent),
		AssignedVUs:  1,
	}

	// First assign should succeed
	err := w.Assign(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, WorkerStateRunning, w.state)

	// Second assign should fail with "already running"
	err = w.Assign(ctx, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

// TestWorkerVUScaling tests the VU scaling logic
func TestWorkerVUScaling(t *testing.T) {
	tests := []struct {
		name            string
		originalTargets []int
		assignedVUs     int
		expectedTargets []int
	}{
		{
			name:            "scale down 50%",
			originalTargets: []int{100, 100},
			assignedVUs:     50,
			expectedTargets: []int{50, 50},
		},
		{
			name:            "scale up 2x",
			originalTargets: []int{25, 50},
			assignedVUs:     100,
			expectedTargets: []int{50, 100},
		},
		{
			name:            "scale with zero target",
			originalTargets: []int{100, 0, 100},
			assignedVUs:     50,
			expectedTargets: []int{50, 0, 50},
		},
		{
			name:            "no scale needed",
			originalTargets: []int{50, 50},
			assignedVUs:     50,
			expectedTargets: []int{50, 50},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &scenarios.Scenario{
				Stages: make([]scenarios.Stage, len(tt.originalTargets)),
			}
			for i, target := range tt.originalTargets {
				sc.Stages[i] = scenarios.Stage{
					DurationRaw: "1s",
					Target:      target,
				}
			}

			scaleScenario(sc, tt.assignedVUs)

			for i, expected := range tt.expectedTargets {
				assert.Equal(t, expected, sc.Stages[i].Target, "stage %d target mismatch", i)
			}
		})
	}
}

// TestWorkerHandleAssignSuccess tests successful assignment via HTTP
func TestWorkerHandleAssignSuccess(t *testing.T) {
	w := NewWorker("test-worker")

	yamlContent := `
name: test
stages:
  - duration: 1s
    target: 1
steps:
  - name: GET test
    method: GET
    url: http://example.com
`

	req := &AssignRequest{
		ScenarioYAML: []byte(yamlContent),
		AssignedVUs:  1,
	}

	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest("POST", "/assign", bytes.NewReader(body))
	writer := httptest.NewRecorder()

	w.handleAssign(writer, httpReq)

	assert.Equal(t, http.StatusOK, writer.Code)

	var resp StatusResponse
	json.Unmarshal(writer.Body.Bytes(), &resp)
	assert.True(t, resp.OK)
}

// TestWorkerHandleAssignConflict tests duplicate assignment returns 409
func TestWorkerHandleAssignConflict(t *testing.T) {
	w := NewWorker("test-worker")

	yamlContent := `
name: test
stages:
  - duration: 1s
    target: 1
steps:
  - name: GET test
    method: GET
    url: http://example.com
`

	req := &AssignRequest{
		ScenarioYAML: []byte(yamlContent),
		AssignedVUs:  1,
	}

	body, _ := json.Marshal(req)

	// First request succeeds
	httpReq := httptest.NewRequest("POST", "/assign", bytes.NewReader(body))
	writer := httptest.NewRecorder()
	w.handleAssign(writer, httpReq)
	assert.Equal(t, http.StatusOK, writer.Code)

	// Second request should fail with 409
	httpReq = httptest.NewRequest("POST", "/assign", bytes.NewReader(body))
	writer = httptest.NewRecorder()
	w.handleAssign(writer, httpReq)
	assert.Equal(t, http.StatusConflict, writer.Code)
}

// TestWorkerHandleStop tests stopping the worker
func TestWorkerHandleStop(t *testing.T) {
	w := NewWorker("test-worker")

	httpReq := httptest.NewRequest("POST", "/stop", nil)
	writer := httptest.NewRecorder()

	w.handleStop(writer, httpReq)

	assert.Equal(t, http.StatusOK, writer.Code)
	var resp StatusResponse
	json.Unmarshal(writer.Body.Bytes(), &resp)
	assert.True(t, resp.OK)
}

// TestWorkerHandleHealth tests health check endpoint
func TestWorkerHandleHealth(t *testing.T) {
	w := NewWorker("test-worker")

	httpReq := httptest.NewRequest("GET", "/health", nil)
	writer := httptest.NewRecorder()

	w.handleHealth(writer, httpReq)

	assert.Equal(t, http.StatusOK, writer.Code)
	var resp StatusResponse
	json.Unmarshal(writer.Body.Bytes(), &resp)
	assert.True(t, resp.OK)
	assert.Equal(t, "healthy", resp.Message)
}

// TestWorkerHandleResultNotAvailable tests result endpoint when no result available
func TestWorkerHandleResultNotAvailable(t *testing.T) {
	w := NewWorker("test-worker")

	httpReq := httptest.NewRequest("GET", "/result", nil)
	writer := httptest.NewRecorder()

	w.handleResult(writer, httpReq)

	assert.Equal(t, http.StatusNotFound, writer.Code)
}

// TestWorkerHandleLive tests live metrics endpoint
func TestWorkerHandleLive(t *testing.T) {
	w := NewWorker("test-worker")

	httpReq := httptest.NewRequest("GET", "/live", nil)
	writer := httptest.NewRecorder()

	w.handleLive(writer, httpReq)

	assert.Equal(t, http.StatusOK, writer.Code)

	var resp LiveMetricsResponse
	json.Unmarshal(writer.Body.Bytes(), &resp)
	assert.Equal(t, "test-worker", resp.WorkerID)
	assert.False(t, resp.Done)
}

// TestScenarioStepsToEngineSteps tests step conversion
func TestScenarioStepsToEngineSteps(t *testing.T) {
	steps := []scenarios.Step{
		{
			Name:   "GET homepage",
			Method: "GET",
			URL:    "http://example.com",
			Headers: map[string]string{
				"User-Agent": "test",
			},
		},
		{
			Name:   "POST data",
			Method: "POST",
			URL:    "http://example.com/api",
			Body:   `{"key":"value"}`,
		},
	}

	engineSteps := scenarioStepsToEngineSteps(steps)

	assert.Equal(t, 2, len(engineSteps))
	assert.Equal(t, "GET homepage", engineSteps[0].Name)
	assert.Equal(t, "GET", engineSteps[0].Request.Method)
	assert.Equal(t, "http://example.com", engineSteps[0].Request.URL)
	assert.Equal(t, "POST data", engineSteps[1].Name)
	assert.Equal(t, "POST", engineSteps[1].Request.Method)
	assert.Equal(t, `{"key":"value"}`, string(engineSteps[1].Request.Body))
}

// TestScenarioStepsNameGeneration tests that step names are generated when empty
func TestScenarioStepsNameGeneration(t *testing.T) {
	steps := []scenarios.Step{
		{
			Method: "POST",
			URL:    "http://example.com/api",
		},
	}

	engineSteps := scenarioStepsToEngineSteps(steps)

	assert.Equal(t, 1, len(engineSteps))
	assert.Equal(t, "POST http://example.com/api", engineSteps[0].Name)
}

// TestWorkerAssignInvalidYAML tests assignment with invalid YAML
func TestWorkerAssignInvalidYAML(t *testing.T) {
	w := NewWorker("test-worker")
	ctx := context.Background()

	req := &AssignRequest{
		ScenarioYAML: []byte("invalid: yaml: content: ["),
		AssignedVUs:  1,
	}

	err := w.Assign(ctx, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// TestWorkerGetLiveMetricsNoEngine tests getting metrics when no engine started
func TestWorkerGetLiveMetricsNoEngine(t *testing.T) {
	w := NewWorker("test-worker")

	metrics := w.GetLiveMetrics()

	assert.NotNil(t, metrics)
	assert.Equal(t, "test-worker", metrics.WorkerID)
	assert.Equal(t, int64(0), metrics.Total)
	assert.Equal(t, 0.0, metrics.RPS)
	assert.False(t, metrics.Done)
}

// TestPrecompileRegexes tests regex compilation for capture patterns
func TestPrecompileRegexes(t *testing.T) {
	values := map[string]string{
		"token":     `regex:\"token\": \"([^\"]+)\"`,
		"user_id":   `$.user_id`,
		"bad_regex": `regex:[invalid(`,
	}

	compiled := precompileRegexes(values)

	// Should have compiled the valid regex
	assert.Equal(t, 1, len(compiled))
	assert.NotNil(t, compiled[`regex:\"token\": \"([^\"]+)\"`])

	// Invalid regex and non-regex patterns should not be in the map
	assert.Nil(t, compiled["user_id"])
	assert.Nil(t, compiled["bad_regex"])
}

// TestWorkerWebSocketStepConversion tests WebSocket step conversion
func TestWorkerWebSocketStepConversion(t *testing.T) {
	steps := []scenarios.Step{
		{
			Name:      "WS connect",
			Method:    "GET",
			URL:       "ws://echo.websocket.org",
			Protocol:  "websocket",
			WSMessage: `{"msg":"hello"}`,
			WSExpect:  "hello",
		},
	}

	engineSteps := scenarioStepsToEngineSteps(steps)

	assert.Equal(t, 1, len(engineSteps))
	assert.Equal(t, "websocket", engineSteps[0].Protocol)
	assert.Equal(t, `{"msg":"hello"}`, string(engineSteps[0].Request.Body))
	assert.Equal(t, "hello", engineSteps[0].Request.Headers["X-WS-Expect"])
}

// TestScenarioVUScalingWithNoMaxTarget tests scaling when no positive target exists
func TestScenarioVUScalingWithNoMaxTarget(t *testing.T) {
	sc := &scenarios.Scenario{
		Stages: []scenarios.Stage{
			{DurationRaw: "1s", Target: 0},
			{DurationRaw: "1s", Target: 0},
		},
	}

	// Should not panic or cause issues
	scaleScenario(sc, 100)

	assert.Equal(t, 0, sc.Stages[0].Target)
	assert.Equal(t, 0, sc.Stages[1].Target)
}

// TestScenarioVUScalingWithZeroAssignedVUs tests scaling with zero assigned VUs
func TestScenarioVUScalingWithZeroAssignedVUs(t *testing.T) {
	sc := &scenarios.Scenario{
		Stages: []scenarios.Stage{
			{DurationRaw: "1s", Target: 100},
		},
	}

	scaleScenario(sc, 0)

	// Should remain unchanged when assigned VUs is 0
	assert.Equal(t, 100, sc.Stages[0].Target)
}
