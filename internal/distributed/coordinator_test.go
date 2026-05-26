package distributed

import (
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/scenarios"
	"github.com/stretchr/testify/assert"
)

// TestAllocateVUsEven tests even VU distribution
func TestAllocateVUsEven(t *testing.T) {
	c := &Coordinator{
		workers: []string{":7700", ":7701", ":7702"},
		cfg: &scenarios.Scenario{
			Stages: []scenarios.Stage{
				{DurationRaw: "1s", Target: 30},
				{DurationRaw: "1s", Target: 30},
			},
		},
	}

	allocation := c.allocateVUs()

	assert.Equal(t, 3, len(allocation))
	assert.Equal(t, 10, allocation[":7700"])
	assert.Equal(t, 10, allocation[":7701"])
	assert.Equal(t, 10, allocation[":7702"])
}

// TestAllocateVUsWithRemainder tests VU distribution with remainder
func TestAllocateVUsWithRemainder(t *testing.T) {
	c := &Coordinator{
		workers: []string{":7700", ":7701", ":7702"},
		cfg: &scenarios.Scenario{
			Stages: []scenarios.Stage{
				{DurationRaw: "1s", Target: 100},
			},
		},
	}

	allocation := c.allocateVUs()

	assert.Equal(t, 3, len(allocation))
	// 100 / 3 = 33, remainder = 1
	assert.Equal(t, 34, allocation[":7700"]) // 33 + 1 (remainder)
	assert.Equal(t, 33, allocation[":7701"])
	assert.Equal(t, 33, allocation[":7702"])

	total := 0
	for _, vus := range allocation {
		total += vus
	}
	assert.Equal(t, 100, total)
}

// TestAllocateVUsWithManyWorkers tests distribution among many workers
func TestAllocateVUsWithManyWorkers(t *testing.T) {
	workers := make([]string, 4)
	for i := range workers {
		workers[i] = ":770" + string(byte('0'+i))
	}

	c := &Coordinator{
		workers: workers,
		cfg: &scenarios.Scenario{
			Stages: []scenarios.Stage{
				{DurationRaw: "1s", Target: 100},
			},
		},
	}

	allocation := c.allocateVUs()

	total := 0
	for _, vus := range allocation {
		total += vus
	}
	assert.Equal(t, 100, total)

	// 100 / 4 = 25 each, no remainder
	for _, vus := range allocation {
		assert.Equal(t, 25, vus)
	}
}

// TestAllocateVUsZeroTarget tests allocation with zero target
func TestAllocateVUsZeroTarget(t *testing.T) {
	c := &Coordinator{
		workers: []string{":7700", ":7701"},
		cfg: &scenarios.Scenario{
			Stages: []scenarios.Stage{
				{DurationRaw: "1s", Target: 0},
			},
		},
	}

	allocation := c.allocateVUs()

	assert.Equal(t, 0, allocation[":7700"])
	assert.Equal(t, 0, allocation[":7701"])
}

// TestMergePartialsBasic tests basic metric merging
func TestMergePartialsBasic(t *testing.T) {
	partials := []PartialSummary{
		{
			WorkerID:  "worker-1",
			Total:     100,
			Errors:    5,
			BytesIn:   1000,
			MinNs:     1000000,
			MaxNs:     5000000,
			P50Ns:     2000000,
			P99Ns:     4500000,
			WallNs:    10000000000,
		},
		{
			WorkerID:  "worker-2",
			Total:     100,
			Errors:    3,
			BytesIn:   1200,
			MinNs:     1500000,
			MaxNs:     4500000,
			P50Ns:     2500000,
			P99Ns:     4000000,
			WallNs:    10000000000,
		},
	}

	sum := mergePartials(partials)

	assert.Equal(t, int64(200), sum.Total)
	assert.Equal(t, int64(8), sum.Errors)
	assert.Equal(t, int64(2200), sum.BytesIn)
	assert.Equal(t, time.Duration(1000000), sum.MinLatency)
	assert.Equal(t, time.Duration(5000000), sum.MaxLatency)
}

// TestMergePartialsWithSteps tests merging with per-step metrics
func TestMergePartialsWithSteps(t *testing.T) {
	partials := []PartialSummary{
		{
			WorkerID: "worker-1",
			Total:    50,
			Errors:   2,
			Steps: []PartialStepSummary{
				{Name: "GET /", Total: 50, Errors: 2},
			},
		},
		{
			WorkerID: "worker-2",
			Total:    50,
			Errors:   1,
			Steps: []PartialStepSummary{
				{Name: "GET /", Total: 50, Errors: 1},
			},
		},
	}

	sum := mergePartials(partials)

	assert.Equal(t, int64(100), sum.Total)
	assert.Equal(t, int64(3), sum.Errors)
	assert.Equal(t, 1, len(sum.Steps))
	assert.Equal(t, "GET /", sum.Steps[0].Name)
	assert.Equal(t, int64(100), sum.Steps[0].Total)
	assert.Equal(t, int64(3), sum.Steps[0].Errors)
}

// TestWeightedPercentile tests percentile calculation with weighting
func TestWeightedPercentile(t *testing.T) {
	partials := []PartialSummary{
		{
			Total:  60,
			P99Ns:  100,
			P95Ns:  80,
			P50Ns:  40,
			MaxNs:  150,
			MinNs:  10,
		},
		{
			Total:  40,
			P99Ns:  200,
			P95Ns:  180,
			P50Ns:  120,
			MaxNs:  250,
			MinNs:  20,
		},
	}

	// P99 = (100*60 + 200*40) / (60+40) = 14000/100 = 140
	p99 := weightedPercentile(partials, func(p *PartialSummary) int64 { return p.P99Ns })
	assert.Equal(t, time.Duration(140), p99)

	// P50 = (40*60 + 120*40) / 100 = 7200/100 = 72
	p50 := weightedPercentile(partials, func(p *PartialSummary) int64 { return p.P50Ns })
	assert.Equal(t, time.Duration(72), p50)
}

// TestWeightedPercentileEmpty tests empty partials
func TestWeightedPercentileEmpty(t *testing.T) {
	partials := []PartialSummary{}
	result := weightedPercentile(partials, func(p *PartialSummary) int64 { return p.P99Ns })
	assert.Equal(t, time.Duration(0), result)
}

// TestNormalizeAddrWithPort tests address normalization with port
func TestNormalizeAddrWithPort(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{":7700", ":7700"},
		{"localhost:7700", "localhost:7700"},
		{"192.168.1.1:8080", "192.168.1.1:8080"},
	}

	for _, tt := range tests {
		result := normalizeAddr(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

// TestNormalizeAddrWithoutPort tests address normalization without port
func TestNormalizeAddrWithoutPort(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"localhost", "localhost:7700"},
		{"192.168.1.1", "192.168.1.1:7700"},
		{"example.com", "example.com:7700"},
	}

	for _, tt := range tests {
		result := normalizeAddr(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

// TestMergePartialsNoPartials tests merging with no partials
func TestMergePartialsNoPartials(t *testing.T) {
	partials := []PartialSummary{}
	sum := mergePartials(partials)

	assert.Equal(t, int64(0), sum.Total)
	assert.Equal(t, int64(0), sum.Errors)
	assert.Equal(t, int64(0), sum.BytesIn)
}

// TestMergePartialsSinglePartial tests merging with single partial
func TestMergePartialsSinglePartial(t *testing.T) {
	partials := []PartialSummary{
		{
			WorkerID:       "worker-1",
			Total:          100,
			Errors:         5,
			MinNs:          1000000,
			MaxNs:          5000000,
			P99Ns:          4500000,
			BytesIn:        1000,
			WallNs:         10000000000,
			DroppedSamples: 2,
		},
	}

	sum := mergePartials(partials)

	assert.Equal(t, int64(100), sum.Total)
	assert.Equal(t, int64(5), sum.Errors)
	assert.Equal(t, int64(1000), sum.BytesIn)
	assert.Equal(t, int64(2), sum.DroppedSamples)
}

// TestMergePartialsWithMaxLatency tests max latency merging
func TestMergePartialsWithMaxLatency(t *testing.T) {
	partials := []PartialSummary{
		{
			Total:  50,
			MaxNs:  3000000,
			MinNs:  500000,
			WallNs: 5000000000,
		},
		{
			Total:  50,
			MaxNs:  8000000,
			MinNs:  800000,
			WallNs: 5000000000,
		},
	}

	sum := mergePartials(partials)

	// Max latency should be the maximum of all maxes
	assert.Equal(t, time.Duration(8000000), sum.MaxLatency)
	// Min latency should be the minimum of all mins
	assert.Equal(t, time.Duration(500000), sum.MinLatency)
	// Wall time should be the maximum wall time
	assert.Equal(t, time.Duration(5000000000), sum.WallTime)
}

// TestAllocateVUsSingleWorker tests single worker allocation
func TestAllocateVUsSingleWorker(t *testing.T) {
	c := &Coordinator{
		workers: []string{":7700"},
		cfg: &scenarios.Scenario{
			Stages: []scenarios.Stage{
				{DurationRaw: "1s", Target: 100},
			},
		},
	}

	allocation := c.allocateVUs()

	assert.Equal(t, 1, len(allocation))
	assert.Equal(t, 100, allocation[":7700"])
}

// TestAllocateVUsNoWorkers tests allocation with no workers
func TestAllocateVUsNoWorkers(t *testing.T) {
	c := &Coordinator{
		workers: []string{},
		cfg: &scenarios.Scenario{
			Stages: []scenarios.Stage{
				{DurationRaw: "1s", Target: 100},
			},
		},
	}

	allocation := c.allocateVUs()

	assert.Equal(t, 0, len(allocation))
}

// TestAllocateVUsComplexStages tests allocation with multiple stages
func TestAllocateVUsComplexStages(t *testing.T) {
	c := &Coordinator{
		workers: []string{":7700", ":7701"},
		cfg: &scenarios.Scenario{
			Stages: []scenarios.Stage{
				{DurationRaw: "10s", Target: 50},
				{DurationRaw: "30s", Target: 100},
				{DurationRaw: "10s", Target: 25},
			},
		},
	}

	allocation := c.allocateVUs()

	// Should allocate based on max target (100)
	// 100 / 2 = 50, remainder = 0
	assert.Equal(t, 50, allocation[":7700"])
	assert.Equal(t, 50, allocation[":7701"])

	total := 0
	for _, vus := range allocation {
		total += vus
	}
	assert.Equal(t, 100, total)
}
