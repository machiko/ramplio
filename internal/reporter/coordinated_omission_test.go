package reporter_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/machiko/ramplio/v2/internal/metrics"
	"github.com/machiko/ramplio/v2/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInterpret_VerdictUsesCorrectedLatency proves the headline judges the
// latency the user actually experiences (corrected) rather than the flattering
// service time. Service p99 of 20ms alone would read "very fast"; a corrected
// p99 of 4s must drag the verdict to fail.
func TestInterpret_VerdictUsesCorrectedLatency(t *testing.T) {
	sum := metrics.Summary{
		Total:        1000,
		P99:          20 * time.Millisecond,
		HasCorrected: true,
		CorrectedP99: 4 * time.Second,
		WallTime:     10 * time.Second,
	}
	in := reporter.Interpret(sum)

	assert.Equal(t, "fail", in.Level, "corrected 4s p99 must fail the verdict")
}

// TestInterpret_NoCorrectedUsesService confirms VU-mode runs (no correction) keep
// judging on service latency unchanged.
func TestInterpret_NoCorrectedUsesService(t *testing.T) {
	sum := metrics.Summary{Total: 1000, P99: 20 * time.Millisecond, WallTime: 10 * time.Second}
	in := reporter.Interpret(sum)

	assert.Equal(t, "pass", in.Level)
}

// TestDiagnose_FlagsCoordinatedOmission checks the plain-language root-cause
// finding fires when requests queue (corrected p99 ≫ service p99).
func TestDiagnose_FlagsCoordinatedOmission(t *testing.T) {
	sum := metrics.Summary{
		Total:        1000,
		P99:          30 * time.Millisecond,
		HasCorrected: true,
		CorrectedP99: 2 * time.Second,
	}
	findings := reporter.Diagnose(sum)

	var found bool
	for _, f := range findings {
		if f.Title == "請求速率超過系統能消化的速度" {
			found = true
			assert.Equal(t, "critical", f.Severity)
		}
	}
	assert.True(t, found, "expected a coordinated-omission finding")
}

// TestDiagnose_NoFalseOmissionWhenKeepingUp ensures a healthy run where corrected
// ≈ service does not raise the omission finding.
func TestDiagnose_NoFalseOmissionWhenKeepingUp(t *testing.T) {
	sum := metrics.Summary{
		Total:        1000,
		P99:          30 * time.Millisecond,
		HasCorrected: true,
		CorrectedP99: 35 * time.Millisecond,
	}
	for _, f := range reporter.Diagnose(sum) {
		assert.NotEqual(t, "請求速率超過系統能消化的速度", f.Title)
	}
}

// TestSummaryToReport_IncludesCorrectedLatency verifies the JSON report carries
// corrected percentiles in rate mode and omits them otherwise.
func TestSummaryToReport_IncludesCorrectedLatency(t *testing.T) {
	rate := reporter.SummaryToReport(metrics.Summary{
		Total: 10, HasCorrected: true, CorrectedP99: 500 * time.Millisecond,
	})
	require.NotNil(t, rate.CorrectedLatency)
	assert.Equal(t, int64(500), rate.CorrectedLatency.P99Ms)

	vu := reporter.SummaryToReport(metrics.Summary{Total: 10})
	assert.Nil(t, vu.CorrectedLatency)
}

// TestPrintSummary_ShowsCorrectedSection confirms the terminal surfaces the
// under-load latency section when correction data is present.
func TestPrintSummary_ShowsCorrectedSection(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{
		Total: 100, P99: 20 * time.Millisecond,
		HasCorrected: true, CorrectedP50: 800 * time.Millisecond, CorrectedP99: 2 * time.Second,
		WallTime: 5 * time.Second,
	})
	out := buf.String()
	assert.Contains(t, out, "壓力下實際延遲")
}
