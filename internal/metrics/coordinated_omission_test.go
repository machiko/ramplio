package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollector_CoordinatedOmissionCorrection proves the core of the correction:
// when a request sat in the queue past its scheduled dispatch time, the raw
// percentile reflects only service time while the corrected percentile reflects
// the full wait the user actually experienced.
func TestCollector_CoordinatedOmissionCorrection(t *testing.T) {
	c := NewCollector(200)
	base := time.Now()
	const (
		service = 20 * time.Millisecond
		waited  = 200 * time.Millisecond // due at base, completed 200ms later
	)
	for range 100 {
		c.Add(Sample{
			Latency:     service,
			StatusCode:  200,
			At:          base.Add(waited),
			ScheduledAt: base,
		})
	}
	sum := c.Stop()

	require.True(t, sum.HasCorrected, "rate-mode samples must produce corrected percentiles")
	assert.InDelta(t, service.Seconds(), sum.P99.Seconds(), 0.005, "raw p99 = service time")
	assert.InDelta(t, waited.Seconds(), sum.CorrectedP99.Seconds(), 0.01, "corrected p99 = time since due")
}

// TestCollector_NoScheduleNoCorrection confirms VU (closed-loop) samples, which
// carry no ScheduledAt, never produce a corrected reading — there is no omission.
func TestCollector_NoScheduleNoCorrection(t *testing.T) {
	c := NewCollector(100)
	for range 50 {
		c.Add(Sample{Latency: 10 * time.Millisecond, StatusCode: 200, At: time.Now()})
	}
	sum := c.Stop()

	assert.False(t, sum.HasCorrected)
	assert.Zero(t, sum.CorrectedP99)
}

// TestCollector_CorrectedFlooredAtService guards against clock skew: if a sample
// is stamped as completing before it was due, the corrected value must never fall
// below the measured service time.
func TestCollector_CorrectedFlooredAtService(t *testing.T) {
	c := NewCollector(10)
	base := time.Now()
	c.Add(Sample{
		Latency:     30 * time.Millisecond,
		StatusCode:  200,
		At:          base,
		ScheduledAt: base.Add(10 * time.Millisecond), // "due" after completion
	})
	sum := c.Stop()

	require.True(t, sum.HasCorrected)
	assert.InDelta(t, (30 * time.Millisecond).Seconds(), sum.CorrectedP99.Seconds(), 0.002)
}

// TestCollector_CapturesGeneratorHealth confirms the collector records its own
// peak goroutine count, so the report can judge whether the generator itself was
// healthy enough to trust the measurement.
func TestCollector_CapturesGeneratorHealth(t *testing.T) {
	c := NewCollector(100)
	// Let the aggregator's monitor tick at least once.
	time.Sleep(goroutineSampleInterval + 50*time.Millisecond)
	for range 10 {
		c.Add(Sample{Latency: 5 * time.Millisecond, StatusCode: 200, At: time.Now()})
	}
	sum := c.Stop()

	assert.Positive(t, sum.GeneratorPeakGoroutines, "peak goroutine count should be recorded")
	assert.GreaterOrEqual(t, sum.GeneratorGCPause, time.Duration(0))
}

// TestMergeExports_MergesCorrectedHistograms ensures the correction survives the
// distributed merge: corrected percentiles are recomputed from merged corrected
// histograms, not averaged.
func TestMergeExports_MergesCorrectedHistograms(t *testing.T) {
	mk := func(service, waited time.Duration, n int) HistogramExport {
		c := NewCollector(2 * n)
		base := time.Now()
		for range n {
			c.Add(Sample{Latency: service, StatusCode: 200, At: base.Add(waited), ScheduledAt: base})
		}
		c.Stop()
		return c.Export()
	}

	merged := MergeExports([]HistogramExport{
		mk(20*time.Millisecond, 150*time.Millisecond, 100),
		mk(20*time.Millisecond, 150*time.Millisecond, 100),
	})

	require.True(t, merged.HasCorrected)
	assert.Equal(t, int64(200), merged.Total)
	assert.InDelta(t, (150 * time.Millisecond).Seconds(), merged.CorrectedP99.Seconds(), 0.01)
}
