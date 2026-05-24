package importer_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/importer"
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

type minHAR struct {
	Log struct {
		Entries []any `json:"entries"`
	} `json:"log"`
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
