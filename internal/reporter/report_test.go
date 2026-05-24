package reporter_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func richSummary() metrics.Summary {
	return metrics.Summary{
		Total:      1000,
		Errors:     10,
		MinLatency: 5 * time.Millisecond,
		MaxLatency: 800 * time.Millisecond,
		BytesIn:    204800,
		WallTime:   30 * time.Second,
		P50:        20 * time.Millisecond,
		P90:        45 * time.Millisecond,
		P95:        80 * time.Millisecond,
		P99:        200 * time.Millisecond,
	}
}

// ── JSON reporter ─────────────────────────────────────────────────────────────

func TestWriteJSON_ValidOutput(t *testing.T) {
	var buf bytes.Buffer
	err := reporter.WriteJSON(&buf, richSummary())
	require.NoError(t, err)

	var r reporter.Report
	require.NoError(t, json.Unmarshal(buf.Bytes(), &r))

	assert.Equal(t, int64(1000), r.Total)
	assert.Equal(t, int64(10), r.Errors)
	assert.InDelta(t, 1.0, r.ErrorRate, 0.01)
	assert.InDelta(t, 33.3, r.RPS, 0.2)
	assert.Equal(t, int64(20), r.Latency.P50Ms)
	assert.Equal(t, int64(200), r.Latency.P99Ms)
}

func TestWriteJSON_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, reporter.WriteJSON(&buf, richSummary()))

	r, err := reporter.ReadJSON(&buf)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), r.Total)
	assert.Equal(t, int64(200), r.Latency.P99Ms)
}

func TestWriteJSON_ZeroSummary(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, reporter.WriteJSON(&buf, metrics.Summary{}))
	assert.True(t, json.Valid(buf.Bytes()))
}

// ── HTML reporter ─────────────────────────────────────────────────────────────

func TestWriteHTML_ContainsChartData(t *testing.T) {
	var buf bytes.Buffer
	err := reporter.WriteHTML(&buf, richSummary())
	require.NoError(t, err)

	out := buf.String()
	assert.True(t, strings.Contains(out, "<html"), "should contain <html>")
	assert.True(t, strings.Contains(out, "1000"), "should contain total requests")
	assert.True(t, strings.Contains(out, "200"), "should contain p99 value")
	assert.True(t, strings.Contains(out, "chart"), "should contain chart reference")
}

func TestWriteHTML_FromReport(t *testing.T) {
	r := reporter.SummaryToReport(richSummary())
	var buf bytes.Buffer
	err := reporter.WriteHTMLFromReport(&buf, r)
	require.NoError(t, err)
	assert.NotEmpty(t, buf.String())
}
