package reporter_test

import (
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/stretchr/testify/assert"
)

func TestMeasurementConfidence_HighWhenClean(t *testing.T) {
	c := reporter.MeasurementConfidence(metrics.Summary{Total: 10000, WallTime: 30 * time.Second})
	assert.Equal(t, "high", c.Level)
}

func TestMeasurementConfidence_LowWhenManyDropped(t *testing.T) {
	// 2% dropped → above the 1% concern ratio.
	c := reporter.MeasurementConfidence(metrics.Summary{Total: 9800, DroppedSamples: 200, WallTime: 30 * time.Second})
	assert.Equal(t, "low", c.Level)
}

func TestMeasurementConfidence_MediumWhenFewDropped(t *testing.T) {
	c := reporter.MeasurementConfidence(metrics.Summary{Total: 100000, DroppedSamples: 5, WallTime: 30 * time.Second})
	assert.Equal(t, "medium", c.Level)
}

func TestMeasurementConfidence_LowWhenGeneratorGCHeavy(t *testing.T) {
	// 3s GC pause over a 30s run = 10% → tool likely distorted the numbers.
	c := reporter.MeasurementConfidence(metrics.Summary{Total: 10000, WallTime: 30 * time.Second, GeneratorGCPause: 3 * time.Second})
	assert.Equal(t, "low", c.Level)
}

func TestDiagnose_FlagsGeneratorGCInterference(t *testing.T) {
	sum := metrics.Summary{Total: 10000, WallTime: 30 * time.Second, GeneratorGCPause: 1500 * time.Millisecond}
	var found bool
	for _, f := range reporter.Diagnose(sum) {
		if f.Title == "量測可能被產生器自身的 GC 干擾" {
			found = true
		}
	}
	assert.True(t, found, "expected a generator-GC interference finding")
}

func TestSummaryToReport_IncludesConfidence(t *testing.T) {
	rep := reporter.SummaryToReport(metrics.Summary{Total: 100, WallTime: 5 * time.Second})
	assert.Equal(t, "high", rep.Confidence.Level)
}

// A rate-mode run that dispatched far fewer requests than its schedule planned
// is not the load the user asked for; the report must say so rather than
// present the numbers as if the declared RPS had been applied.
func TestMeasurementConfidence_FlagsGeneratorShortfall(t *testing.T) {
	sum := metrics.Summary{Total: 1580, GeneratorDispatchedRequests: 1580, GeneratorPlannedRequests: 2000, WallTime: time.Second}
	r := reporter.MeasurementConfidence(sum)
	assert.Equal(t, "medium", r.Level)
	assert.Contains(t, r.Note, "79%")
	assert.Contains(t, r.Note, "2000")
}

func TestMeasurementConfidence_NoShortfallNoteWhenOnSchedule(t *testing.T) {
	sum := metrics.Summary{Total: 1990, GeneratorDispatchedRequests: 1990, GeneratorPlannedRequests: 2000, WallTime: time.Second}
	r := reporter.MeasurementConfidence(sum)
	assert.Equal(t, "high", r.Level)
	assert.NotContains(t, r.Note, "排程")
}

// A run that plans only a few requests is one send away from any ratio; a
// difference of a request or two is rounding, not a generator falling behind.
func TestMeasurementConfidence_NoShortfallForRoundingOnTinyRuns(t *testing.T) {
	tiny := metrics.Summary{Total: 17, GeneratorDispatchedRequests: 17, GeneratorPlannedRequests: 18, WallTime: time.Second}
	assert.NotContains(t, reporter.MeasurementConfidence(tiny).Note, "排程")

	tinyBehind := metrics.Summary{Total: 10, GeneratorDispatchedRequests: 10, GeneratorPlannedRequests: 18, WallTime: time.Second}
	assert.Contains(t, reporter.MeasurementConfidence(tinyBehind).Note, "排程")
}

// The verdict follows what the generator dispatched, not what was recorded:
// fewer samples than sends (in-flight at the end, skipped `if` steps) is not a
// shortfall, and extra samples (`loop` repeats) must not hide a real one.
func TestMeasurementConfidence_ShortfallFollowsDispatchedNotSamples(t *testing.T) {
	onSchedule := metrics.Summary{Total: 1000, GeneratorDispatchedRequests: 1995, GeneratorPlannedRequests: 2000, WallTime: time.Second}
	assert.NotContains(t, reporter.MeasurementConfidence(onSchedule).Note, "排程")

	behind := metrics.Summary{Total: 4000, GeneratorDispatchedRequests: 1500, GeneratorPlannedRequests: 2000, WallTime: time.Second}
	assert.Contains(t, reporter.MeasurementConfidence(behind).Note, "75%")
}
