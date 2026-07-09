package dashboard_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockController is a test double for dashboard.Controller.
type mockController struct {
	snap        reporter.LiveSnapshot
	state       dashboard.State
	result      *dashboard.RunResult
	startCalled []dashboard.RunRequest
	stopCalled  bool
	startErr    error
}

func (m *mockController) Snapshot() reporter.LiveSnapshot               { return m.snap }
func (m *mockController) State() dashboard.State                        { return m.state }
func (m *mockController) Result() *dashboard.RunResult                  { return m.result }
func (m *mockController) ScenarioInfo() *dashboard.ScenarioMeta         { return nil }
func (m *mockController) LoadScenario(_ []byte, _ string) error         { return nil }
func (m *mockController) ActiveGuidedProfile() *dashboard.GuidedProfile { return nil }
func (m *mockController) Stop()                                         { m.stopCalled = true }
func (m *mockController) WriteReport(w io.Writer) error {
	if m.result == nil {
		return fmt.Errorf("no completed test run")
	}
	_, err := io.WriteString(w, "<html>mock report</html>")
	return err
}
func (m *mockController) StartDiscover(_ dashboard.DiscoverRequest) error { return nil }
func (m *mockController) DiscoverProgress() ([]dashboard.DiscoverProbeSnap, *dashboard.DiscoverResultSnap, *dashboard.DiscoverCurrentSnap, []int, bool) {
	return nil, nil, nil, nil, false
}
func (m *mockController) Start(req dashboard.RunRequest) error {
	if m.startErr != nil {
		return m.startErr
	}
	m.startCalled = append(m.startCalled, req)
	m.state = dashboard.StateRunning
	return nil
}

func newTestServer(t *testing.T, ctrl dashboard.Controller) (*dashboard.Server, context.CancelFunc) {
	t.Helper()
	return newTestServerWithToken(t, ctrl, "")
}

func newTestServerWithToken(t *testing.T, ctrl dashboard.Controller, token string) (*dashboard.Server, context.CancelFunc) {
	t.Helper()
	srv := dashboard.New(ctrl, 0, token)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, srv.Start(ctx))
	return srv, cancel
}

func TestServer_ServesHTML(t *testing.T) {
	ctrl := &mockController{snap: reporter.LiveSnapshot{Total: 42, RPS: 10.5}}
	srv, _ := newTestServer(t, ctrl)
	assert.NotEmpty(t, srv.Addr())
}

func TestServer_WebSocket_ReceivesMetrics(t *testing.T) {
	snap := reporter.LiveSnapshot{
		Total: 100, Errors: 2, RPS: 14.5,
		P50: 80 * time.Millisecond, P99: 250 * time.Millisecond,
		ActiveVUs: 5, Elapsed: 10 * time.Second,
	}
	ctrl := &mockController{snap: snap, state: dashboard.StateRunning}
	srv, _ := newTestServer(t, ctrl)

	wsURL := url.URL{Scheme: "ws", Host: srv.Addr(), Path: "/ws"}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(msg, &payload))

	assert.InDelta(t, 14.5, payload["rps"], 0.01)
	assert.Equal(t, float64(100), payload["total"])
	assert.Equal(t, float64(80), payload["p50_ms"])
	assert.Equal(t, float64(250), payload["p99_ms"])
	assert.Equal(t, "running", payload["state"])
}

func TestServer_WebSocket_IncludesResult(t *testing.T) {
	res := &dashboard.RunResult{Total: 500, RPS: 42.0, P99Ms: 150}
	ctrl := &mockController{state: dashboard.StateDone, result: res}
	srv, _ := newTestServer(t, ctrl)

	wsURL := url.URL{Scheme: "ws", Host: srv.Addr(), Path: "/ws"}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(msg, &payload))

	assert.Equal(t, "done", payload["state"])
	result, ok := payload["result"].(map[string]any)
	require.True(t, ok, "result field should be present when state is done")
	assert.Equal(t, float64(500), result["total"])
	assert.InDelta(t, 42.0, result["rps"], 0.01)
}

// 觀測快照必須完整通過 JSON 契約送達瀏覽器——
// 釘住前端讀取的鍵名(status/top/excluded_ops/truncated),重構時不可默默改名。
func TestServer_WebSocket_IncludesObserveSnap(t *testing.T) {
	res := &dashboard.RunResult{
		Total: 500,
		Observe: &dashboard.ObserveSnap{
			Status:      "ok",
			Truncated:   true,
			ExcludedOps: []string{"SELECT orders"},
			Top: []dashboard.ObserveDegradation{
				{Operation: "GET /checkout", BaselineP95Ms: 30, StressedP95Ms: 270, Factor: 9},
			},
		},
	}
	ctrl := &mockController{state: dashboard.StateDone, result: res}
	srv, _ := newTestServer(t, ctrl)

	wsURL := url.URL{Scheme: "ws", Host: srv.Addr(), Path: "/ws"}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(msg, &payload))

	result, ok := payload["result"].(map[string]any)
	require.True(t, ok, "result field should be present when state is done")
	obs, ok := result["observe"].(map[string]any)
	require.True(t, ok, "observe snapshot should ride along with the result")
	assert.Equal(t, "ok", obs["status"])
	assert.Equal(t, true, obs["truncated"])
	assert.Equal(t, []any{"SELECT orders"}, obs["excluded_ops"])
	top, ok := obs["top"].([]any)
	require.True(t, ok, "top degradations should be present")
	require.Len(t, top, 1)
	first := top[0].(map[string]any)
	assert.Equal(t, "GET /checkout", first["operation"])
	assert.InDelta(t, 9.0, first["factor"], 0.001)
}

// 未啟用觀測時 observe 鍵必須缺席(omitempty)——卡片以「缺席」表達未啟用,
// 空物件會被前端誤判為有資料。
func TestServer_WebSocket_OmitsObserveWhenNil(t *testing.T) {
	res := &dashboard.RunResult{Total: 500}
	ctrl := &mockController{state: dashboard.StateDone, result: res}
	srv, _ := newTestServer(t, ctrl)

	wsURL := url.URL{Scheme: "ws", Host: srv.Addr(), Path: "/ws"}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(msg, &payload))

	result, ok := payload["result"].(map[string]any)
	require.True(t, ok)
	_, present := result["observe"]
	assert.False(t, present, "observe 未啟用時不應出現在 JSON")
}

func TestServer_MultipleClients(t *testing.T) {
	ctrl := &mockController{snap: reporter.LiveSnapshot{Total: 1}}
	srv, _ := newTestServer(t, ctrl)

	const numClients = 5
	conns := make([]*websocket.Conn, numClients)
	for i := range conns {
		wsURL := fmt.Sprintf("ws://%s/ws", srv.Addr())
		c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)
		conns[i] = c
		t.Cleanup(func() { c.Close() })
	}

	for _, c := range conns {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, msg, err := c.ReadMessage()
		require.NoError(t, err)
		assert.NotEmpty(t, msg)
	}
}

func TestServer_ShutdownOnContextCancel(t *testing.T) {
	ctrl := &mockController{}
	srv, cancel := newTestServer(t, ctrl)
	addr := srv.Addr()

	cancel()
	time.Sleep(300 * time.Millisecond)

	_, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://%s/ws", addr), nil)
	assert.Error(t, err, "new connections should be rejected after shutdown")
}

func TestServer_RunAPI_AcceptsValidRequest(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{"url":"http://example.com","method":"GET","vus":10,"duration":"30s"}`
	resp, err := http.Post(
		fmt.Sprintf("http://%s/api/run", srv.Addr()),
		"application/json",
		strings.NewReader(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	require.Len(t, ctrl.startCalled, 1)
	assert.Equal(t, "http://example.com", ctrl.startCalled[0].URL)
	assert.Equal(t, 10, ctrl.startCalled[0].VUs)
}

func TestServer_RunAPI_RejectsIfAlreadyRunning(t *testing.T) {
	ctrl := &mockController{startErr: fmt.Errorf("test already running")}
	srv, _ := newTestServer(t, ctrl)

	body := `{"url":"http://example.com","vus":5,"duration":"10s"}`
	resp, err := http.Post(
		fmt.Sprintf("http://%s/api/run", srv.Addr()),
		"application/json",
		strings.NewReader(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestServer_RunAPI_RejectsInvalidMethod(t *testing.T) {
	ctrl := &mockController{}
	srv, _ := newTestServer(t, ctrl)

	resp, err := http.Get(fmt.Sprintf("http://%s/api/run", srv.Addr()))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestServer_StopAPI_StopsTest(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateRunning}
	srv, _ := newTestServer(t, ctrl)

	resp, err := http.Post(
		fmt.Sprintf("http://%s/api/stop", srv.Addr()),
		"application/json",
		nil,
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, ctrl.stopCalled)
}

func TestServer_StatusAPI_ReturnsCurrentState(t *testing.T) {
	res := &dashboard.RunResult{Total: 200, RPS: 10.0}
	ctrl := &mockController{state: dashboard.StateDone, result: res}
	srv, _ := newTestServer(t, ctrl)

	resp, err := http.Get(fmt.Sprintf("http://%s/api/status", srv.Addr()))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	assert.Equal(t, "done", payload["state"])
	result, ok := payload["result"].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, 10.0, result["rps"], 0.01)
}

func TestServer_ReportAPI_DownloadsHTML(t *testing.T) {
	res := &dashboard.RunResult{Total: 500, RPS: 25.0}
	ctrl := &mockController{state: dashboard.StateDone, result: res}
	srv, _ := newTestServer(t, ctrl)

	resp, err := http.Get(fmt.Sprintf("http://%s/api/report", srv.Addr()))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment")
	assert.NotEmpty(t, resp.Header.Get("Content-Length"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "<html")
}

func TestServer_ReportAPI_404WhenNoTestRun(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	resp, err := http.Get(fmt.Sprintf("http://%s/api/report", srv.Addr()))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServer_Token_BlocksWithoutCredential(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServerWithToken(t, ctrl, "secret123")

	// POST /api/run without token → 401
	resp, err := http.Post(
		fmt.Sprintf("http://%s/api/run", srv.Addr()),
		"application/json",
		strings.NewReader(`{"url":"http://example.com","vus":1,"duration":"5s"}`),
	)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestServer_Token_AllowsWithBearerHeader(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServerWithToken(t, ctrl, "secret123")

	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s/api/run", srv.Addr()),
		strings.NewReader(`{"url":"http://example.com","vus":1,"duration":"5s"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret123")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestServer_Token_AllowsStatusWithoutCredential(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServerWithToken(t, ctrl, "secret123")

	// GET /api/status is read-only — no token required
	resp, err := http.Get(fmt.Sprintf("http://%s/api/status", srv.Addr()))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestServer_Token_WSWithQueryParam(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServerWithToken(t, ctrl, "secret123")

	// WebSocket without token → rejected
	wsURL := url.URL{Scheme: "ws", Host: srv.Addr(), Path: "/ws"}
	_, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	assert.Error(t, err, "WS without token should be rejected")

	// WebSocket with correct token → accepted
	wsURL.RawQuery = "token=secret123"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
	require.NoError(t, err, "WS with correct token should connect")
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.NotEmpty(t, msg)
}
