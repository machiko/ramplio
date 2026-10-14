package distributed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pathCounter is a target that counts hits per path and remembers the
// Authorization header teardown sent.
type pathCounter struct {
	mu         sync.Mutex
	hits       map[string]int
	logoutAuth string
}

func newPathCounter(t *testing.T) (*pathCounter, *httptest.Server) {
	t.Helper()
	pc := &pathCounter{hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc.mu.Lock()
		pc.hits[r.URL.Path]++
		if r.URL.Path == "/logout" {
			pc.logoutAuth = r.Header.Get("Authorization")
		}
		pc.mu.Unlock()
		if r.URL.Path == "/login" {
			_, _ = w.Write([]byte(`{"token":"t"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return pc, srv
}

func (pc *pathCounter) count(path string) int {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.hits[path]
}

func (pc *pathCounter) logoutAuthorization() string {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.logoutAuth
}

// lifecycleYAML runs 2 VUs for hold, with a setup that captures a token and a
// teardown that logs out with it.
func lifecycleYAML(url, hold string) string {
	return fmt.Sprintf(`name: lifecycle
stages:
  - duration: 10ms
    target: 2
  - duration: %[2]s
    target: 2
setup:
  - name: login
    method: POST
    url: %[1]s/login
    capture:
      token: $.token
steps:
  - name: work
    method: GET
    url: %[1]s/work
teardown:
  - name: logout
    method: POST
    url: %[1]s/logout
    headers:
      Authorization: "Bearer {{capture.token}}"
`, url, hold)
}

// stillRunning reports whether workers keep sending load: the target count is
// sampled across a short gap after the run returned (one in-flight request may
// still land).
func stillRunning(pc *pathCounter) bool {
	before := pc.count("/work")
	time.Sleep(300 * time.Millisecond)
	return pc.count("/work")-before > 1
}

// Setup and teardown run exactly once for the whole cluster, on the
// coordinator (README: "分散模式下 setup 和 teardown 只在 Coordinator 執行一次").
// Teardown side effects (log out, delete test data) must not repeat per worker.
func TestDistributed_SetupAndTeardownRunOncePerCluster(t *testing.T) {
	pc, srv := newPathCounter(t)

	sum := runCluster(t, lifecycleYAML(srv.URL, "200ms"), 2)

	require.Positive(t, sum.Total)
	assert.Positive(t, pc.count("/work"))
	assert.Equal(t, 1, pc.count("/login"), "setup must run once, on the coordinator")
	assert.Equal(t, 1, pc.count("/logout"), "teardown must run once, on the coordinator — not again on every worker")
	assert.Equal(t, "Bearer t", pc.logoutAuthorization(), "teardown must be seeded with the setup captures")
}

// fakeWorker is a stand-in worker server: it advertises stage shares on /health
// and delegates the other endpoints to handlers, counting /stop calls.
func fakeWorker(t *testing.T, stops *atomic.Int32, handle func(w http.ResponseWriter, r *http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"ok":true,"capabilities":["` + CapabilityStageShares + `"]}`))
		case "/stop":
			stops.Add(1)
			handle(w, r)
		default:
			handle(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// Once setup has run, teardown must run exactly once however the run ends.
// Here a worker refuses its assignment after setup logged in: the worker that
// did start is stopped first (so the load is not still using the session), then
// teardown logs out. The refusing worker is left alone — a refusal (e.g. 409
// busy) can mean it is running another coordinator's test.
func TestDistributed_TeardownRunsOnceWhenAssignFails(t *testing.T) {
	pc, srv := newPathCounter(t)
	var refusedStops atomic.Int32
	refusing := fakeWorker(t, &refusedStops, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/assign" {
			http.Error(w, "busy", http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	addrs := append(startWorkers(t, 1), refusing)
	coord := newTestCoordinator(t, addrs, lifecycleYAML(srv.URL, "30s"))

	_, err := coord.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, pc.count("/login"))
	assert.Equal(t, 1, pc.count("/logout"), "teardown must still run once when the run fails after setup")
	assert.False(t, stillRunning(pc), "the worker that started must be stopped, not left running its 30s stage")
	assert.Zero(t, refusedStops.Load(), "a worker that refused the assignment must not be stopped")
}

// A failure to stop a worker means it may still be loading the target while
// teardown logs out — that must reach the user, not be swallowed.
func TestDistributed_StopFailureIsReported(t *testing.T) {
	pc, srv := newPathCounter(t)
	var stops atomic.Int32
	unreachable := fakeWorker(t, &stops, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assign":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/live":
			_, _ = w.Write([]byte(`not json`)) // poll fails → the run aborts
		case "/stop":
			conn, _, err := w.(http.Hijacker).Hijack() // drop the connection
			if err == nil {
				_ = conn.Close()
			}
		}
	})
	coord := newTestCoordinator(t, []string{unreachable}, lifecycleYAML(srv.URL, "30s"))

	_, err := coord.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "polling failed")
	assert.Contains(t, err.Error(), "stopping workers", "a failed stop must be part of the reported error")
	assert.Positive(t, stops.Load())
	assert.Equal(t, 1, pc.count("/logout"), "teardown still runs once")
}

// Ctrl-C cancels the run context. Workers must actually be stopped and
// teardown must still run once — both need a context that outlives the cancel.
func TestDistributed_TeardownRunsOnceWhenCancelled(t *testing.T) {
	pc, srv := newPathCounter(t)
	coord := newTestCoordinator(t, startWorkers(t, 2), lifecycleYAML(srv.URL, "30s"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel once load is flowing — i.e. mid-test, after setup and assignment —
	// rather than after a fixed delay that a loaded machine might not meet.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for pc.count("/work") == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	_, err := coord.Run(ctx) // a cancelled run may report an error; cleanup is what matters here
	t.Logf("run returned after %v: %v", time.Since(start), err)

	assert.Less(t, time.Since(start), 15*time.Second, "cancel must end the run promptly")
	require.Equal(t, 1, pc.count("/login"), "setup must have run before the cancel")
	assert.Positive(t, pc.count("/work"))
	assert.Equal(t, 1, pc.count("/logout"), "teardown must run once even after Ctrl-C")
	assert.Equal(t, "Bearer t", pc.logoutAuthorization())
	assert.False(t, stillRunning(pc), "cancel must stop the workers")
}

// A worker runs only the load: setup and teardown belong to the coordinator.
func TestWorker_DoesNotRunSetupOrTeardown(t *testing.T) {
	pc, srv := newPathCounter(t)

	w := NewWorker("no-lifecycle")
	t.Cleanup(w.Stop)
	require.NoError(t, w.Assign(context.Background(), &AssignRequest{
		ScenarioYAML:  []byte(lifecycleYAML(srv.URL, "200ms")),
		StageShares:   []StageShare{{Target: 1}, {Target: 1}},
		SetupCaptures: map[string]string{"token": "t"},
	}))
	require.Eventually(t, func() bool { return w.GetState() == WorkerStateDone },
		5*time.Second, 20*time.Millisecond)

	assert.Positive(t, pc.count("/work"))
	assert.Zero(t, pc.count("/login"), "workers seed setup captures instead of running setup")
	assert.Zero(t, pc.count("/logout"), "workers must not run teardown")
}
