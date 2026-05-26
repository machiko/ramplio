package distributed

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/scenarios"
	"github.com/stretchr/testify/assert"
)

// TestDistributedIntegrationBasic tests coordinator-worker communication with scenario assignment
func TestDistributedIntegrationBasic(t *testing.T) {
	// Create a simple test scenario
	yamlContent := `
name: integration test
stages:
  - duration: 100ms
    target: 2
steps:
  - name: GET example
    method: GET
    url: http://example.com/
`

	// Create and start workers
	worker1 := NewWorker("worker-1")
	worker2 := NewWorker("worker-2")

	// Start worker HTTP servers on dynamic ports
	port1 := findFreePort()
	port2 := findFreePort()

	go func() {
		_ = worker1.StartHTTPServer(":" + port1)
	}()
	go func() {
		_ = worker2.StartHTTPServer(":" + port2)
	}()

	// Wait for servers to start
	time.Sleep(100 * time.Millisecond)

	// Parse scenario
	sc, err := scenarios.Parse(bytes.NewReader([]byte(yamlContent)))
	assert.NoError(t, err)

	// Create coordinator with workers
	coordinator := NewCoordinator(
		[]string{"localhost:" + port1, "localhost:" + port2},
		[]byte(yamlContent),
		sc,
		protocols.DefaultHTTPConfig(),
	)

	// Run the distributed test
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	summary, err := coordinator.Run(ctx)

	// Verify coordinator ran without error and merged results
	assert.NoError(t, err)
	// Note: this scenario makes external requests to example.com, which might fail
	// The key test here is that the coordinator successfully orchestrated workers
	assert.NotNil(t, summary)
}

// TestDistributedWorkerStateTransition tests worker state transitions during load test
func TestDistributedWorkerStateTransition(t *testing.T) {
	yamlContent := `
name: state test
stages:
  - duration: 100ms
    target: 1
steps:
  - name: GET example
    method: GET
    url: http://example.com
`

	worker := NewWorker("state-test-worker")

	// Initial state should be idle
	assert.Equal(t, WorkerStateIdle, worker.GetState())

	// Assign work
	ctx := context.Background()
	req := &AssignRequest{
		ScenarioYAML: []byte(yamlContent),
		AssignedVUs:  1,
	}

	err := worker.Assign(ctx, req)
	assert.NoError(t, err)

	// Should now be running
	assert.Equal(t, WorkerStateRunning, worker.GetState())

	// Wait for completion
	time.Sleep(1 * time.Second)

	// Should now be done
	assert.Equal(t, WorkerStateDone, worker.GetState())

	// Should be able to get result
	result := worker.GetResult()
	assert.NotNil(t, result)
}

// TestDistributedVUAllocationAccuracy tests that VUs are correctly allocated and scaled
func TestDistributedVUAllocationAccuracy(t *testing.T) {
	// Create scenario with specific VU targets
	sc := &scenarios.Scenario{
		Stages: []scenarios.Stage{
			{DurationRaw: "1s", Target: 100},
			{DurationRaw: "1s", Target: 50},
		},
	}

	c := &Coordinator{
		workers: []string{":7700", ":7701", ":7702", ":7703"},
		cfg:     sc,
	}

	allocation := c.allocateVUs()

	// Verify allocation
	assert.Equal(t, 4, len(allocation))

	total := 0
	for _, vus := range allocation {
		total += vus
		assert.Equal(t, 25, vus) // 100 / 4 = 25 each
	}
	assert.Equal(t, 100, total)
}

// TestDistributedMetricsMerging tests merging of metrics from multiple workers
func TestDistributedMetricsMerging(t *testing.T) {
	// Simulate results from 3 workers
	partials := []PartialSummary{
		{
			WorkerID:  "worker-1",
			Total:     100,
			Errors:    2,
			MinNs:     10000000,
			MaxNs:     50000000,
			P99Ns:     45000000,
			BytesIn:   10000,
			WallNs:    10000000000,
		},
		{
			WorkerID:  "worker-2",
			Total:     100,
			Errors:    1,
			MinNs:     12000000,
			MaxNs:     48000000,
			P99Ns:     44000000,
			BytesIn:   10000,
			WallNs:    10000000000,
		},
		{
			WorkerID:  "worker-3",
			Total:     100,
			Errors:    3,
			MinNs:     11000000,
			MaxNs:     52000000,
			P99Ns:     46000000,
			BytesIn:   10000,
			WallNs:    10000000000,
		},
	}

	sum := mergePartials(partials)

	// Verify aggregation
	assert.Equal(t, int64(300), sum.Total)
	assert.Equal(t, int64(6), sum.Errors)
	assert.Equal(t, int64(30000), sum.BytesIn)

	// Verify latencies
	assert.Equal(t, time.Duration(10000000), sum.MinLatency)
	assert.Equal(t, time.Duration(52000000), sum.MaxLatency)

	// Verify P99 is weighted average
	// (45*100 + 44*100 + 46*100) / 300 = 13500/300 = 45
	assert.Equal(t, time.Duration(45000000), sum.P99)
}

// TestDistributedWorkerHTTPServer tests worker HTTP endpoints directly
func TestDistributedWorkerHTTPServer(t *testing.T) {
	worker := NewWorker("http-test-worker")

	// Start HTTP server on dynamic port
	port := findFreePort()
	go func() {
		_ = worker.StartHTTPServer(":" + port)
	}()

	time.Sleep(100 * time.Millisecond)

	baseURL := "http://127.0.0.1:" + port

	// Test health endpoint
	resp, err := http.Get(baseURL + "/health")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Test live endpoint
	resp, err = http.Get(baseURL + "/live")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Test result endpoint (should fail, not started yet)
	resp, err = http.Get(baseURL + "/result")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// TestDistributedCoordinatorLiveSnapshot tests live snapshot aggregation
func TestDistributedCoordinatorLiveSnapshot(t *testing.T) {
	workers := []string{":7700", ":7701"}
	c := &Coordinator{
		workers:   workers,
		startedAt: time.Now().Add(-5 * time.Second), // Simulate 5 seconds of running
		cfg:       &scenarios.Scenario{},
	}

	// Simulate live metrics from workers
	liveMetrics := map[string]*LiveMetricsResponse{
		":7700": {
			WorkerID:  "worker-1",
			Total:     500,
			Errors:    5,
			RPS:       100,
			P99Ns:     50000000,
			ActiveVUs: 25,
			Done:      false,
		},
		":7701": {
			WorkerID:  "worker-2",
			Total:     450,
			Errors:    3,
			RPS:       90,
			P99Ns:     48000000,
			ActiveVUs: 25,
			Done:      false,
		},
	}

	c.aggregateLiveMetrics(liveMetrics)

	// Verify aggregation
	snap := c.LiveSnapshot()
	assert.Equal(t, int64(950), snap.Total)
	assert.Equal(t, int64(8), snap.Errors)
	assert.Greater(t, snap.RPS, 180.0) // Should be around 190 RPS (950/5)
	assert.Equal(t, 50, snap.ActiveVUs) // 25 + 25
}

// TestDistributedCompleteFlow tests the complete flow from scenario assignment through result collection
func TestDistributedCompleteFlow(t *testing.T) {
	// Create a simple scenario
	yamlContent := `
name: complete flow test
stages:
  - duration: 100ms
    target: 2
steps:
  - name: ping
    method: GET
    url: http://httpbin.org/status/200
`

	// Create workers
	w1 := NewWorker("worker-1")
	w2 := NewWorker("worker-2")

	p1 := findFreePort()
	p2 := findFreePort()

	// Start worker servers
	go func() {
		_ = w1.StartHTTPServer(":" + p1)
	}()
	go func() {
		_ = w2.StartHTTPServer(":" + p2)
	}()

	time.Sleep(100 * time.Millisecond)

	// Parse scenario
	sc, _ := scenarios.Parse(bytes.NewReader([]byte(yamlContent)))

	// Create and run coordinator
	coord := NewCoordinator(
		[]string{"localhost:" + p1, "localhost:" + p2},
		[]byte(yamlContent),
		sc,
		protocols.DefaultHTTPConfig(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	summary, err := coord.Run(ctx)

	// Verify coordinator completed successfully
	assert.NoError(t, err)
	assert.NotNil(t, summary)
}

// findFreePort finds an available port for testing
func findFreePort() string {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: 0})
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("%d", port)
}
