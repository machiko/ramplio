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
