package dashboard_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/scenariogen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func postGenerate(t *testing.T, srv *dashboard.Server, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(
		fmt.Sprintf("http://%s/api/generate", srv.Addr()),
		"application/json",
		strings.NewReader(body),
	)
	require.NoError(t, err)
	return resp
}

// A download-only request generates the YAML and CSV but must not touch the
// controller's in-memory scenario state.
func TestServer_Generate_DownloadOnly(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{
		"base_url": "https://example.com",
		"steps": [{"path": "/users/{{data.user_id}}", "method": "GET", "status_code": "200"}],
		"vus": 10, "duration": "1m", "shape": "steady",
		"columns": [{"name": "user_id", "kind": "int_seq", "start_set": true}],
		"rows": 5, "data_file": "data.csv"
	}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out dashboard.GenerateResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))

	assert.Contains(t, out.YAML, "vars_from:")
	assert.Contains(t, out.YAML, "{{data.user_id}}")
	assert.Contains(t, out.CSV, "user_id")
	assert.Contains(t, out.CSV, "\n1\n") // int_seq starting at 1
	assert.Equal(t, "data.csv", out.DataFile)
	assert.Empty(t, out.Warnings) // reference and declaration match
	assert.False(t, ctrl.loadWithDataCalled, "download must not load into memory")
}

// A run request generates the same output and additionally loads the scenario
// into memory with the CSV supplied in-process (no disk file).
func TestServer_Generate_RunLoadsIntoMemory(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{
		"base_url": "https://example.com",
		"steps": [{"path": "/users/{{data.user_id}}", "method": "GET", "status_code": "200"}],
		"vus": 10, "duration": "1m", "shape": "steady",
		"columns": [{"name": "user_id", "kind": "int_seq", "start_set": true}],
		"rows": 3, "data_file": "data.csv",
		"run": true
	}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.True(t, ctrl.loadWithDataCalled, "run must load into memory")
	assert.Contains(t, string(ctrl.loadWithDataYAML), "vars_from:")
	assert.Contains(t, ctrl.loadWithDataCSV, "user_id")
	// header + 3 data rows = 4 lines
	lines := strings.Split(strings.TrimSpace(ctrl.loadWithDataCSV), "\n")
	assert.Len(t, lines, 4, "expected 3 data rows plus header")
}

// A step referencing an undeclared {{data.X}} column surfaces a warning at
// generation time.
func TestServer_Generate_ReportsWarnings(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{
		"base_url": "https://example.com",
		"steps": [{"path": "/search", "method": "GET", "status_code": "200", "body": "{{data.keywrod}}"}],
		"vus": 5, "duration": "1m", "shape": "steady",
		"columns": [{"name": "keyword", "kind": "list", "list_values": ["a"]}],
		"rows": 2
	}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out dashboard.GenerateResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Warnings, 2)
	assert.Contains(t, strings.Join(out.Warnings, "\n"), "keywrod")
}

// No columns: still generates a runnable scenario with no vars_from and no CSV.
func TestServer_Generate_NoColumns(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{
		"base_url": "https://example.com",
		"steps": [{"path": "/", "method": "GET", "status_code": "200"}],
		"vus": 10, "duration": "1m", "shape": "steady", "run": true
	}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out dashboard.GenerateResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.NotContains(t, out.YAML, "vars_from:")
	assert.Empty(t, out.CSV)
	assert.True(t, ctrl.loadWithDataCalled)
	assert.Empty(t, ctrl.loadWithDataCSV)
}

func TestServer_Generate_RejectsMissingBaseURL(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{"steps": [{"path": "/", "method": "GET", "status_code": "200"}], "vus": 10}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestServer_Generate_RejectsNoSteps(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{"base_url": "https://example.com", "vus": 10}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// A hostile or mistyped row count is clamped so the server never allocates an
// unbounded CSV. The cap is 1_000_000 data rows (mirrors the CLI limit).
func TestServer_Generate_ClampsHugeRowCount(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{
		"base_url": "https://example.com",
		"steps": [{"path": "/", "method": "GET", "status_code": "200"}],
		"columns": [{"name": "id", "kind": "int_seq"}],
		"rows": 2000000
	}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out dashboard.GenerateResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	// 1 header line + 1_000_000 data lines, each terminated by "\n".
	assert.Equal(t, 1_000_001, strings.Count(out.CSV, "\n"))
}

// The CSV cost is columns × rows; capping each dimension alone still permits an
// astronomical product, so the product itself is bounded.
func TestServer_Generate_RejectsTooManyCells(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	// 3 columns × 1,000,000 rows = 3M cells > the 2M cap.
	body := `{
		"base_url": "https://example.com",
		"steps": [{"path": "/", "method": "GET", "status_code": "200"}],
		"columns": [{"name":"a","kind":"uuid"},{"name":"b","kind":"uuid"},{"name":"c","kind":"uuid"}],
		"rows": 1000000
	}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.False(t, ctrl.loadWithDataCalled)
}

func TestServer_Generate_RejectsTooManyColumns(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	cols := make([]scenariogen.DataColumn, 300)
	for i := range cols {
		cols[i] = scenariogen.DataColumn{Name: fmt.Sprintf("c%d", i), Kind: scenariogen.KindPlaceholder}
	}
	reqBody, err := json.Marshal(dashboard.GenerateRequest{
		BaseURL: "https://example.com",
		Steps:   []scenariogen.Step{{Path: "/", Method: "GET", StatusCode: "200"}},
		Columns: cols,
		Rows:    10,
	})
	require.NoError(t, err)
	resp := postGenerate(t, srv, string(reqBody))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// Cookie auth references a session CSV that only exists on disk, so running it
// straight from the browser must be rejected rather than loaded broken.
func TestServer_Generate_RejectsCookieRun(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	body := `{
		"base_url": "https://example.com",
		"steps": [{"path": "/dashboard", "method": "GET", "status_code": "200"}],
		"auth": {"kind": "cookie", "csv_file": "sessions.csv", "cookie_name": "session"},
		"run": true
	}`
	resp := postGenerate(t, srv, body)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.False(t, ctrl.loadWithDataCalled)
}

// The served dashboard HTML must actually wire in the generate wizard UI, so a
// browser reaches the form and its submit path. Guards against the template and
// endpoint drifting apart (the browser E2E is not part of CI).
func TestServer_ServesGenerateUI(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	resp, err := http.Get(fmt.Sprintf("http://%s/", srv.Addr()))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)

	for _, marker := range []string{
		"pickMode('generate')",     // home entry
		"setupMode === 'generate'", // form view
		"generateScenario",         // submit handler
		"generateAndRun",           // 直接開跑 handler
		"/api/generate",            // endpoint wiring
	} {
		assert.Contains(t, html, marker, "served HTML missing generate-UI marker %q", marker)
	}
}

func TestServer_Generate_RejectsInvalidMethod(t *testing.T) {
	ctrl := &mockController{state: dashboard.StateIdle}
	srv, _ := newTestServer(t, ctrl)

	resp, err := http.Get(fmt.Sprintf("http://%s/api/generate", srv.Addr()))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
