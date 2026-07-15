package metrics

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feed records the given latencies (in ms) into a collector and stops it.
// The collector is sized generously so the non-blocking Add never drops a
// sample — otherwise the computed percentiles would be unreliable.
func feed(latenciesMs []int, step string) *Collector {
	c := NewCollector(len(latenciesMs) + 100)
	for _, ms := range latenciesMs {
		c.Add(Sample{
			Latency:    time.Duration(ms) * time.Millisecond,
			StatusCode: 200,
			BytesRead:  10,
			StepName:   step,
		})
	}
	c.Stop()
	return c
}

// TestMergeExportsMatchesGroundTruth is the core guarantee: merging two
// workers' histogram snapshots must yield the same percentiles as feeding all
// samples into a single collector. This is what the old weighted-average merge
// got wrong — the p99 of combined data is NOT the average of per-shard p99s.
func TestMergeExportsMatchesGroundTruth(t *testing.T) {
	// Two skewed shards: shard A is mostly fast, shard B is mostly slow.
	shardA := []int{1, 1, 1, 1, 1, 2, 2, 2, 2, 100}
	shardB := []int{50, 50, 50, 50, 50, 80, 80, 80, 80, 200}

	cA := feed(shardA, "")
	cB := feed(shardB, "")

	merged := MergeExports([]HistogramExport{cA.Export(), cB.Export()})

	// Ground truth: a single collector fed every sample.
	all := append(append([]int{}, shardA...), shardB...)
	truth := feed(all, "").Stop()

	assert.Equal(t, int64(20), merged.Total)
	assert.Equal(t, truth.P50, merged.P50, "p50 must match ground truth")
	assert.Equal(t, truth.P90, merged.P90, "p90 must match ground truth")
	assert.Equal(t, truth.P95, merged.P95, "p95 must match ground truth")
	assert.Equal(t, truth.P99, merged.P99, "p99 must match ground truth")

	// Guard against regression to the averaging bug: the naive
	// (p99_A + p99_B)/2 would be far from the true combined p99.
	naiveAvg := (cA.Stop().P99 + cB.Stop().P99) / 2
	assert.NotEqual(t, naiveAvg, merged.P99,
		"merged p99 should reflect combined distribution, not the average of shard p99s")
}

// TestMergeExportsAggregatesScalars verifies counts, bytes, min/max and
// dropped samples aggregate across shards, and wall time takes the max.
func TestMergeExportsAggregatesScalars(t *testing.T) {
	a := HistogramExport{Total: 100, Errors: 5, BytesIn: 1000, MinNs: 1_000_000, MaxNs: 5_000_000, WallNs: 10_000_000_000, Dropped: 2}
	b := HistogramExport{Total: 100, Errors: 3, BytesIn: 1200, MinNs: 1_500_000, MaxNs: 4_500_000, WallNs: 8_000_000_000, Dropped: 1}

	sum := MergeExports([]HistogramExport{a, b})

	assert.Equal(t, int64(200), sum.Total)
	assert.Equal(t, int64(8), sum.Errors)
	assert.Equal(t, int64(2200), sum.BytesIn)
	assert.Equal(t, int64(3), sum.DroppedSamples)
	assert.Equal(t, time.Duration(1_000_000), sum.MinLatency)
	assert.Equal(t, time.Duration(5_000_000), sum.MaxLatency)
	// Shards run concurrently: wall time is the longest shard, not the sum.
	assert.Equal(t, time.Duration(10_000_000_000), sum.WallTime)
}

// TestMergeExportsEmpty returns the zero summary for no input.
func TestMergeExportsEmpty(t *testing.T) {
	sum := MergeExports(nil)
	assert.Equal(t, int64(0), sum.Total)
	assert.Equal(t, time.Duration(0), sum.P99)
}

// TestMergeExportsPerStep verifies per-step histograms merge independently.
func TestMergeExportsPerStep(t *testing.T) {
	cA := feed([]int{10, 10, 10}, "GET /")
	cB := feed([]int{20, 20, 20}, "GET /")

	sum := MergeExports([]HistogramExport{cA.Export(), cB.Export()})

	require.Len(t, sum.Steps, 1)
	assert.Equal(t, "GET /", sum.Steps[0].Name)
	assert.Equal(t, int64(6), sum.Steps[0].Total)
	// Combined step p50 should fall within the observed range.
	assert.GreaterOrEqual(t, sum.Steps[0].P50, 10*time.Millisecond)
	assert.LessOrEqual(t, sum.Steps[0].P50, 20*time.Millisecond)
}

// TestExportRoundTripsThroughJSON ensures the snapshot survives serialization,
// which is how it travels from worker to coordinator.
func TestExportRoundTripsThroughJSON(t *testing.T) {
	c := feed([]int{5, 10, 15, 20, 25}, "")
	exp := c.Export()

	raw, err := json.Marshal(exp)
	require.NoError(t, err)

	var decoded HistogramExport
	require.NoError(t, json.Unmarshal(raw, &decoded))

	direct := MergeExports([]HistogramExport{exp})
	viaJSON := MergeExports([]HistogramExport{decoded})
	assert.Equal(t, direct.P99, viaJSON.P99)
	assert.Equal(t, direct.Total, viaJSON.Total)
}

// TTFT histogram 必須通過 export→JSON→merge 全鏈——漏了任何一站,
// 分散式模式會靜默丟掉 TTFT 指標(v31-2 教訓:契約對照實作驗證)。
func TestMergeExportsCarriesTTFT(t *testing.T) {
	mk := func(ttftMs int64) HistogramExport {
		c := NewCollector(4)
		c.Add(Sample{Latency: 200 * time.Millisecond, TTFT: time.Duration(ttftMs) * time.Millisecond, StatusCode: 200})
		c.Add(Sample{Latency: 250 * time.Millisecond, TTFT: time.Duration(ttftMs+10) * time.Millisecond, StatusCode: 200})
		_ = c.Stop()
		return c.Export()
	}
	e1, e2 := mk(40), mk(80)

	// 模擬跨節點傳輸:JSON 往返後合併
	raw1, err := json.Marshal(e1)
	require.NoError(t, err)
	raw2, err := json.Marshal(e2)
	require.NoError(t, err)
	var d1, d2 HistogramExport
	require.NoError(t, json.Unmarshal(raw1, &d1))
	require.NoError(t, json.Unmarshal(raw2, &d2))

	sum := MergeExports([]HistogramExport{d1, d2})

	require.True(t, sum.HasTTFT, "合併後 TTFT 不可靜默消失")
	assert.InDelta(t, 40, sum.TTFTP50.Milliseconds(), 15, "p50 應落在兩節點分佈內")
	assert.InDelta(t, 90, sum.TTFTP99.Milliseconds(), 10)
}

// 無 TTFT 的 worker 匯出:合併結果 HasTTFT 維持 false。
func TestMergeExportsNoTTFT(t *testing.T) {
	c := NewCollector(4)
	c.Add(Sample{Latency: 10 * time.Millisecond, StatusCode: 200})
	_ = c.Stop()

	sum := MergeExports([]HistogramExport{c.Export()})

	assert.False(t, sum.HasTTFT)
}
