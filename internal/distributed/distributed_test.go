package distributed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkerRunActuallyGeneratesLoad is a regression test for a bug where the
// engine's run context was rooted at the /assign HTTP request context. net/http
// cancels that context as soon as the handler returns, which killed the engine
// instantly and produced zero requests. The run must survive the response and
// generate real load. This drives the full HTTP path (assign → result) against
// a local target.
func TestWorkerRunActuallyGeneratesLoad(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		rw.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	yaml := fmt.Sprintf(`name: regression
stages:
  - duration: 1s
    target: 10
steps:
  - name: GET /
    method: GET
    url: %s/
    assertions:
      status: 200
`, target.URL)

	worker := NewWorker("regression-worker")
	base := "http://" + serveWorker(t, worker)
	body, _ := json.Marshal(AssignRequest{ScenarioYAML: []byte(yaml), AssignedVUs: 10})
	resp, err := http.Post(base+"/assign", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// The result is published once the 1s run finishes.
	var res *http.Response
	client := &http.Client{Timeout: 2 * time.Second} // a hung worker must not hang the poll
	require.Eventually(t, func() bool {
		r, err := client.Get(base + "/result")
		if err != nil {
			return false
		}
		if r.StatusCode != http.StatusOK {
			r.Body.Close()
			return false
		}
		res = r
		return true
	}, 10*time.Second, 50*time.Millisecond, "worker never published a result")
	defer res.Body.Close()

	var result ResultResponse
	require.NoError(t, json.NewDecoder(res.Body).Decode(&result))

	assert.Greater(t, result.Export.Total, int64(0),
		"worker must generate load after the assign response is sent (run context must outlive the request)")
	assert.Greater(t, hits.Load(), int64(0), "target server should have received requests")
}

// TestDistributedIntegrationBasic tests coordinator-worker communication with scenario assignment
func TestDistributedIntegrationBasic(t *testing.T) {
	// Create a simple test scenario
	yamlContent := fmt.Sprintf(`
name: integration test
stages:
  - duration: 10ms
    target: 2
  - duration: 300ms
    target: 2
steps:
  - name: GET local
    method: GET
    url: %s/
`, localTarget(t))

	// Create and start workers
	addr1 := serveWorker(t, NewWorker("worker-1"))
	addr2 := serveWorker(t, NewWorker("worker-2"))

	// Parse scenario
	sc, err := scenarios.Parse(bytes.NewReader([]byte(yamlContent)))
	assert.NoError(t, err)

	// Create coordinator with workers
	coordinator := NewCoordinator(
		[]string{addr1, addr2},
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
	assert.Positive(t, summary.Total, "both workers' load must reach the merged summary")
}

// TestDistributedWorkerStateTransition tests worker state transitions during load test
func TestDistributedWorkerStateTransition(t *testing.T) {
	yamlContent := fmt.Sprintf(`
name: state test
stages:
  - duration: 100ms
    target: 1
steps:
  - name: GET local
    method: GET
    url: %s
`, localTarget(t))

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

	// Should reach done once the 100ms stage ends
	require.Eventually(t, func() bool { return worker.GetState() == WorkerStateDone },
		5*time.Second, 10*time.Millisecond, "worker never reached done")

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

// TestDistributedWorkerHTTPServer tests worker HTTP endpoints directly
func TestDistributedWorkerHTTPServer(t *testing.T) {
	worker := NewWorker("http-test-worker")

	baseURL := "http://" + serveWorker(t, worker)

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
	assert.Greater(t, snap.RPS, 180.0)  // Should be around 190 RPS (950/5)
	assert.Equal(t, 50, snap.ActiveVUs) // 25 + 25
}

// TestDistributedCompleteFlow tests the complete flow from scenario assignment through result collection
func TestDistributedCompleteFlow(t *testing.T) {
	// Create a simple scenario
	yamlContent := fmt.Sprintf(`
name: complete flow test
stages:
  - duration: 10ms
    target: 2
  - duration: 300ms
    target: 2
steps:
  - name: ping
    method: GET
    url: %s/status/200
`, localTarget(t))

	// Create and start workers
	a1 := serveWorker(t, NewWorker("worker-1"))
	a2 := serveWorker(t, NewWorker("worker-2"))

	// Parse scenario
	sc, _ := scenarios.Parse(bytes.NewReader([]byte(yamlContent)))

	// Create and run coordinator
	coord := NewCoordinator(
		[]string{a1, a2},
		[]byte(yamlContent),
		sc,
		protocols.DefaultHTTPConfig(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	summary, err := coord.Run(ctx)

	// Verify coordinator completed successfully
	assert.NoError(t, err)
	assert.Positive(t, summary.Total)
	assert.Zero(t, summary.Errors, "the local target always answers 200")
}

// serveWorker runs w's HTTP API on a port the test already holds (no
// find-then-rebind race with parallel packages) and returns its host:port.
// At cleanup the listener and every connection it accepted are closed, so
// neither the server nor kept-alive connections outlive the test.
func serveWorker(t *testing.T, w *Worker) string {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ln := &trackingListener{Listener: inner}
	t.Cleanup(ln.closeAll)
	go func() { _ = w.Serve(ln) }()
	return ln.Addr().String()
}

// trackingListener remembers accepted connections so a test can close them:
// Serve owns its http.Server, and closing only the listener leaves kept-alive
// connections (and their server goroutines) running.
type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.conns = append(l.conns, c)
		l.mu.Unlock()
	}
	return c, err
}

func (l *trackingListener) closeAll() {
	_ = l.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
}

// localTarget is an in-process load target that answers 200 to everything,
// so tests never depend on the network or a third-party site.
func localTarget(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// StartHTTPServer must report a listen failure instead of serving nothing.
func TestStartHTTPServer_ReportsListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	assert.Error(t, NewWorker("busy").StartHTTPServer(ln.Addr().String()), "the address is already taken")
}
