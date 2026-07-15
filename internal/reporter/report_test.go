package reporter_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/reporter"
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

// TestPrintSummary_RPMForSlowEndpoint proves the per-minute conversion surfaces in
// both the technical summary line and the plain-language 承受能力 card when rps < 1,
// and stays absent once throughput reaches 1/s where per-second framing is fine.
func TestPrintSummary_RPMForSlowEndpoint(t *testing.T) {
	// 21 reqs / 210s ≈ 0.1 rps ≈ 6 RPM — the exact slow-endpoint case from the RAG run.
	slow := metrics.Summary{Total: 21, WallTime: 210 * time.Second, P99: 60 * time.Second}
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, slow)
	out := buf.String()
	assert.Contains(t, out, "RPM", "技術摘要行應附 RPM 換算")
	assert.Contains(t, out, "每分鐘", "白話卡片應附每分鐘換算")

	// 100 reqs / 10s = 10 rps — fast enough that RPM framing adds nothing.
	fast := metrics.Summary{Total: 100, WallTime: 10 * time.Second, P99: 50 * time.Millisecond}
	buf.Reset()
	reporter.PrintSummary(&buf, fast)
	fastOut := buf.String()
	assert.NotContains(t, fastOut, "RPM", "≥1 rps 不應顯示 RPM")
	assert.NotContains(t, fastOut, "每分鐘", "≥1 rps 不應顯示每分鐘換算")
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
