package scenarios_test

import (
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v2/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validYAML = `
name: smoke test
stages:
  - duration: 30s
    target: 50
  - duration: 60s
    target: 50
  - duration: 30s
    target: 0
steps:
  - name: GET health
    method: GET
    url: https://example.com/health
    assertions:
      status: 200
thresholds:
  error_rate_pct: 1.0
  p99_ms: 1000
`

func TestParse_ValidScenario(t *testing.T) {
	sc, err := scenarios.Parse(strings.NewReader(validYAML))
	require.NoError(t, err)

	assert.Equal(t, "smoke test", sc.Name)
	assert.Len(t, sc.Stages, 3)
	assert.Equal(t, 30*time.Second, sc.Stages[0].Duration)
	assert.Equal(t, 50, sc.Stages[0].Target)
	assert.Equal(t, time.Duration(0), sc.Stages[2].Duration-30*time.Second)

	require.Len(t, sc.Steps, 1)
	assert.Equal(t, "GET", sc.Steps[0].Method)
	assert.Equal(t, "https://example.com/health", sc.Steps[0].URL)
	require.NotNil(t, sc.Steps[0].Assertions)
	assert.Equal(t, scenarios.StatusExact(200), sc.Steps[0].Assertions.Status)

	require.NotNil(t, sc.Thresholds)
	assert.InDelta(t, 1.0, *sc.Thresholds.ErrorRatePct, 0.001)
	assert.InDelta(t, 1000.0, *sc.Thresholds.P99Ms, 0.001)
}

func TestParse_MissingStages(t *testing.T) {
	yaml := `
name: bad
steps:
  - name: GET /
    method: GET
    url: https://example.com
`
	_, err := scenarios.Parse(strings.NewReader(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stage")
}

func TestParse_MissingSteps(t *testing.T) {
	yaml := `
name: bad
stages:
  - duration: 10s
    target: 5
`
	_, err := scenarios.Parse(strings.NewReader(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step")
}

func TestParse_InvalidDuration(t *testing.T) {
	yaml := `
name: bad
stages:
  - duration: "not-a-duration"
    target: 5
steps:
  - name: GET /
    method: GET
    url: https://example.com
`
	_, err := scenarios.Parse(strings.NewReader(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duration")
}

func TestParse_MissingStepURL(t *testing.T) {
	yaml := `
name: bad
stages:
  - duration: 10s
    target: 5
steps:
  - name: no url
    method: GET
`
	_, err := scenarios.Parse(strings.NewReader(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url")
}

func TestParse_NegativeTarget(t *testing.T) {
	yaml := `
name: bad
stages:
  - duration: 10s
    target: -1
steps:
  - name: GET /
    method: GET
    url: https://example.com
`
	_, err := scenarios.Parse(strings.NewReader(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target")
}

func TestParseFile_NotFound(t *testing.T) {
	_, err := scenarios.ParseFile("/nonexistent/path.yaml")
	require.Error(t, err)
}

func TestParse_ThinkTime(t *testing.T) {
	yaml := `
name: think time test
stages:
  - duration: 10s
    target: 2
steps:
  - name: step one
    method: GET
    url: https://example.com/
    pause: 500ms
  - name: step two
    method: GET
    url: https://example.com/api
`
	sc, err := scenarios.Parse(strings.NewReader(yaml))
	require.NoError(t, err)
	assert.Equal(t, 500*time.Millisecond, sc.Steps[0].Pause)
	assert.Equal(t, time.Duration(0), sc.Steps[1].Pause)
}

func TestParse_InvalidPause(t *testing.T) {
	yaml := `
name: bad pause
stages:
  - duration: 10s
    target: 2
steps:
  - name: step
    method: GET
    url: https://example.com/
    pause: "not-a-duration"
`
	_, err := scenarios.Parse(strings.NewReader(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pause")
}

func TestParse_StatusWildcard(t *testing.T) {
	yaml := `
name: wildcard test
stages:
  - duration: 10s
    target: 1
steps:
  - name: get
    method: GET
    url: https://example.com/
    assertions:
      status: 2xx
`
	sc, err := scenarios.Parse(strings.NewReader(yaml))
	require.NoError(t, err)
	require.NotNil(t, sc.Steps[0].Assertions.Status)
	assert.True(t, sc.Steps[0].Assertions.Status.Match(200))
	assert.True(t, sc.Steps[0].Assertions.Status.Match(204))
	assert.False(t, sc.Steps[0].Assertions.Status.Match(404))
}
