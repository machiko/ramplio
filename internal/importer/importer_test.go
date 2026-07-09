package importer_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/importer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── filter ──────────────────────────────────────────────────────────────────

func TestFilter_RemovesJS(t *testing.T) {
	data := singleEntryHAR("GET", "https://cdn.example.com/app.js", 200, "application/javascript", "")
	_, err := importer.ConvertBytes(data, importer.Options{Filter: true}, "test")
	assert.Error(t, err, "only static asset → should fail with no entries")
}

func TestFilter_RemovesCSS(t *testing.T) {
	data := singleEntryHAR("GET", "https://cdn.example.com/style", 200, "text/css", "")
	_, err := importer.ConvertBytes(data, importer.Options{Filter: true}, "test")
	assert.Error(t, err)
}

func TestFilter_KeepsAPIRequests(t *testing.T) {
	data := fixture(t, "../../testdata/importer/simple.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "simple")
	require.NoError(t, err)

	y := string(out)
	assert.Contains(t, y, "/users")
	assert.Contains(t, y, "/orders")
	assert.NotContains(t, y, "app.bundle.js")
}

func TestFilter_NoFilterFlag_IncludesStatic(t *testing.T) {
	data := fixture(t, "../../testdata/importer/simple.har")
	out, err := importer.ConvertBytes(data, importer.Options{Filter: false}, "simple")
	require.NoError(t, err)

	assert.Contains(t, string(out), "app.bundle.js")
}

// ─── login detection ─────────────────────────────────────────────────────────

func TestLoginFlow_CaptureAdded(t *testing.T) {
	data := fixture(t, "../../testdata/importer/login_flow.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "login_flow")
	require.NoError(t, err)

	y := string(out)
	assert.Contains(t, y, "capture:", "login step should declare capture")
	assert.Contains(t, y, "$.token", "token JSONPath should be captured")
}

func TestLoginFlow_RawTokenReplaced(t *testing.T) {
	data := fixture(t, "../../testdata/importer/login_flow.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "login_flow")
	require.NoError(t, err)

	y := string(out)
	assert.Contains(t, y, "{{capture.token}}", "subsequent steps must use captured token")
	assert.NotContains(t, y, "eyJhbGciOiJIUzI1NiJ9.test.sig", "raw token must be removed from YAML")
}

func TestLoginFlow_StepCount(t *testing.T) {
	data := fixture(t, "../../testdata/importer/login_flow.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "login_flow")
	require.NoError(t, err)

	assert.Equal(t, 3, strings.Count(string(out), "- name:"))
}

// ─── converter output ─────────────────────────────────────────────────────────

func TestConvert_DefaultStages(t *testing.T) {
	data := fixture(t, "../../testdata/importer/simple.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "simple")
	require.NoError(t, err)

	y := string(out)
	assert.Contains(t, y, "30s")
	assert.Contains(t, y, "60s")
}

func TestConvert_CustomDuration(t *testing.T) {
	data := fixture(t, "../../testdata/importer/simple.har")
	// 4m total → ramp=1m, hold=2m, ramp=1m (no 30s ramps)
	out, err := importer.ConvertBytes(data, importer.Options{Filter: true, Duration: 4 * time.Minute}, "simple")
	require.NoError(t, err)

	y := string(out)
	assert.Contains(t, y, "1m", "4m duration should produce 1m ramp stages")
	assert.Contains(t, y, "2m", "4m duration should produce 2m hold stage")
	assert.NotContains(t, y, "30s", "4m duration should not produce 30s stages")
}

func TestConvert_StatusAssertion(t *testing.T) {
	data := fixture(t, "../../testdata/importer/simple.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "simple")
	require.NoError(t, err)

	assert.Contains(t, string(out), "status: 2xx")
}

func TestConvert_ScenarioName_Stripped(t *testing.T) {
	data := fixture(t, "../../testdata/importer/simple.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "recordings/my-api.har")
	require.NoError(t, err)

	assert.Contains(t, string(out), "name: my-api")
}

func TestConvert_PostBody_Preserved(t *testing.T) {
	data := fixture(t, "../../testdata/importer/simple.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "simple")
	require.NoError(t, err)

	assert.Contains(t, string(out), "item_id")
}

func TestConvert_UserAgentFiltered(t *testing.T) {
	data := fixture(t, "../../testdata/importer/simple.har")
	out, err := importer.ConvertBytes(data, importer.DefaultOptions(), "simple")
	require.NoError(t, err)

	assert.NotContains(t, string(out), "User-Agent")
}

// ─── pre-auth token (CSRF / nonce) ───────────────────────────────────────────

// preAuthHAR builds a HAR with:
//  1. GET /api/csrf → JSON response with a long _token value
//  2. POST /login → body contains that _token verbatim
//  3. GET /dashboard → plain API call after login
func preAuthHAR() []byte {
	const csrfToken = "csrf_token_value_abc123xyz987_unique"
	entries := []any{
		map[string]any{
			"request": map[string]any{
				"method":  "GET",
				"url":     "https://app.example.com/api/csrf",
				"headers": []any{},
			},
			"response": map[string]any{
				"status": 200,
				"headers": []any{
					map[string]string{"name": "Content-Type", "value": "application/json"},
				},
				"content": map[string]any{
					"mimeType": "application/json",
					"text":     `{"_token": "` + csrfToken + `"}`,
				},
			},
		},
		map[string]any{
			"request": map[string]any{
				"method": "POST",
				"url":    "https://app.example.com/api/login",
				"headers": []any{
					map[string]string{"name": "Content-Type", "value": "application/x-www-form-urlencoded"},
				},
				"postData": map[string]string{
					"mimeType": "application/x-www-form-urlencoded",
					"text":     "email=user%40example.com&password=secret&_token=" + csrfToken,
				},
			},
			"response": map[string]any{
				"status": 200,
				"headers": []any{
					map[string]string{"name": "Content-Type", "value": "application/json"},
				},
				"content": map[string]any{
					"mimeType": "application/json",
					"text":     `{"status": "ok"}`,
				},
			},
		},
		map[string]any{
			"request": map[string]any{
				"method":  "GET",
				"url":     "https://app.example.com/api/dashboard",
				"headers": []any{},
			},
			"response": map[string]any{
				"status":  200,
				"headers": []any{},
				"content": map[string]any{"mimeType": "application/json", "text": `{}`},
			},
		},
	}
	h := map[string]any{"log": map[string]any{"entries": entries}}
	b, err := json.Marshal(h)
	if err != nil {
		panic(fmt.Sprintf("preAuthHAR: %v", err))
	}
	return b
}

func TestPreAuth_CaptureAdded(t *testing.T) {
	out, err := importer.ConvertBytes(preAuthHAR(), importer.DefaultOptions(), "test")
	require.NoError(t, err)

	y := string(out)
	assert.Contains(t, y, "capture:", "GET /api/csrf step should have capture")
	assert.Contains(t, y, "$._token", "should capture the _token JSONPath")
}

func TestPreAuth_TokenReplacedInLoginBody(t *testing.T) {
	out, err := importer.ConvertBytes(preAuthHAR(), importer.DefaultOptions(), "test")
	require.NoError(t, err)

	y := string(out)
	assert.Contains(t, y, "{{capture._token}}", "POST /login body should use template variable")
	assert.NotContains(t, y, "csrf_token_value_abc123xyz987_unique", "raw CSRF token must not appear in YAML")
}

func TestPreAuth_StepCount(t *testing.T) {
	out, err := importer.ConvertBytes(preAuthHAR(), importer.DefaultOptions(), "test")
	require.NoError(t, err)

	assert.Equal(t, 3, strings.Count(string(out), "- name:"))
}

// ─── from file ───────────────────────────────────────────────────────────────

func TestConvert_FromFile(t *testing.T) {
	out, err := importer.Convert("../../testdata/importer/simple.har", importer.DefaultOptions())
	require.NoError(t, err)
	assert.Contains(t, string(out), "/users")
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func fixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func singleEntryHAR(method, reqURL string, status int, contentType, body string) []byte {
	entry := map[string]any{
		"request": map[string]any{
			"method":  method,
			"url":     reqURL,
			"headers": []any{},
		},
		"response": map[string]any{
			"status": status,
			"headers": []any{
				map[string]string{"name": "Content-Type", "value": contentType},
			},
			"content": map[string]string{
				"mimeType": contentType,
				"text":     body,
			},
		},
	}
	h := map[string]any{
		"log": map[string]any{
			"entries": []any{entry},
		},
	}
	b, err := json.Marshal(h)
	if err != nil {
		panic(fmt.Sprintf("singleEntryHAR: %v", err))
	}
	return b
}
