package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestHistogram_PercentilePrecision 驗證 HDR 量測精度。
// 900 個 10ms + 100 個 1000ms = 1000 樣本：
//   - p50（500th）= 10ms
//   - p90（900th）= 10ms（剛好在邊界）
//   - p99（990th）= 1000ms（落入尾端 100 個樣本中）
func TestHistogram_PercentilePrecision(t *testing.T) {
	c := NewCollector(100)

	for i := 0; i < 900; i++ {
		c.Add(Sample{Latency: 10 * time.Millisecond, StatusCode: 200})
	}
	for i := 0; i < 100; i++ {
		c.Add(Sample{Latency: 1000 * time.Millisecond, StatusCode: 200})
	}

	sum := c.Stop()

	// p50 落在 bulk（~10ms）
	assert.InDelta(t, 10.0, float64(sum.P50.Milliseconds()), 2.0, "p50 should be ~10ms")
	// p99 落在尾端（>100ms）
	assert.Greater(t, sum.P99.Milliseconds(), int64(100), "p99 should be in tail")
	// p95 也應落在尾端（>100ms）
	assert.Greater(t, sum.P95.Milliseconds(), int64(100), "p95 should be in tail")
	// p99 >= p95
	assert.GreaterOrEqual(t, sum.P99.Milliseconds(), sum.P95.Milliseconds(), "p99 >= p95")
}

func TestHistogram_UniformDistribution(t *testing.T) {
	c := NewCollector(10)

	// 均勻分布：1ms ~ 100ms，各 1 個
	for ms := int64(1); ms <= 100; ms++ {
		c.Add(Sample{Latency: time.Duration(ms) * time.Millisecond, StatusCode: 200})
	}

	sum := c.Stop()

	assert.InDelta(t, 50.0, float64(sum.P50.Milliseconds()), 2.0, "p50 ≈ 50ms")
	assert.InDelta(t, 90.0, float64(sum.P90.Milliseconds()), 2.0, "p90 ≈ 90ms")
	assert.InDelta(t, 95.0, float64(sum.P95.Milliseconds()), 2.0, "p95 ≈ 95ms")
	assert.InDelta(t, 99.0, float64(sum.P99.Milliseconds()), 2.0, "p99 ≈ 99ms")
}

func TestHistogram_ZeroSamples(t *testing.T) {
	c := NewCollector(5)
	sum := c.Stop()

	assert.Equal(t, time.Duration(0), sum.P50)
	assert.Equal(t, time.Duration(0), sum.P99)
}
