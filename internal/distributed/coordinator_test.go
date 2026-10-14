package distributed

import (
	"fmt"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestSplitScheme verifies scheme extraction from worker addresses.
func TestSplitScheme(t *testing.T) {
	tests := []struct {
		input    string
		scheme   string
		hostport string
	}{
		{":7700", "http", ":7700"},
		{"localhost:7700", "http", "localhost:7700"},
		{"http://localhost:7700", "http", "localhost:7700"},
		{"https://w1:7700", "https", "w1:7700"},
		{"https://10.0.0.1", "https", "10.0.0.1"},
	}
	for _, tt := range tests {
		scheme, hostport := splitScheme(tt.input)
		assert.Equal(t, tt.scheme, scheme, "scheme for %q", tt.input)
		assert.Equal(t, tt.hostport, hostport, "hostport for %q", tt.input)
	}
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

// splitStageShares must hand every worker its share of every stage so that,
// stage by stage, the shares add up to exactly what the scenario declares —
// for VU targets and target RPS alike.
func TestSplitStageShares_SumToDeclaredLoadPerStage(t *testing.T) {
	stages := []scenarios.Stage{
		{Duration: 10 * time.Second, Target: 10},
		{Duration: 5 * time.Second, Target: 5}, // proportional scaling made this 2+2+2=6
		{Duration: 5 * time.Second, Target: 0},
	}
	got := splitStageShares(stages, 3)

	require.Len(t, got, 3)
	assert.Equal(t, []StageShare{{Target: 4}, {Target: 2}, {Target: 0}}, got[0])
	assert.Equal(t, []StageShare{{Target: 3}, {Target: 2}, {Target: 0}}, got[1])
	assert.Equal(t, []StageShare{{Target: 3}, {Target: 1}, {Target: 0}}, got[2])
}

func TestSplitStageShares_SplitsTargetRPS(t *testing.T) {
	got := splitStageShares([]scenarios.Stage{{TargetRPS: 40}, {TargetRPS: 7}}, 3)
	assert.Equal(t, []StageShare{{TargetRPS: 14}, {TargetRPS: 3}}, got[0])
	assert.Equal(t, []StageShare{{TargetRPS: 13}, {TargetRPS: 2}}, got[1])
	assert.Equal(t, []StageShare{{TargetRPS: 13}, {TargetRPS: 2}}, got[2])
}

// With more workers than load the surplus workers get zero — they must idle,
// not fall back to running the whole scenario.
func TestSplitStageShares_MoreWorkersThanLoad(t *testing.T) {
	got := splitStageShares([]scenarios.Stage{{Target: 1}}, 3)
	assert.Equal(t, [][]StageShare{{{Target: 1}}, {{Target: 0}}, {{Target: 0}}}, got)
}

func TestSplitStageShares_NoWorkers(t *testing.T) {
	assert.Nil(t, splitStageShares([]scenarios.Stage{{Target: 5}}, 0))
}

// Properties over many cluster sizes and loads: shares sum to the total, differ
// by at most one, never shrink as the total grows, and a worker's peak VU share
// is exactly what allocateVUs reports as its AssignedVUs.
func TestSplitStageShares_Properties(t *testing.T) {
	for n := 1; n <= 9; n++ {
		workers := make([]string, n)
		for i := range workers {
			workers[i] = fmt.Sprintf("w%d", i)
		}
		for total := 0; total <= 40; total++ {
			stages := []scenarios.Stage{{Target: total}, {Target: total / 2}, {TargetRPS: total}}
			shares := splitStageShares(stages, n)
			for j := range stages {
				sumVU, sumRPS := 0, 0
				lo, hi := total, 0
				for w := range shares {
					sumVU += shares[w][j].Target
					sumRPS += shares[w][j].TargetRPS
					v := shares[w][j].Target + shares[w][j].TargetRPS
					lo, hi = min(lo, v), max(hi, v)
				}
				require.Equal(t, stages[j].Target, sumVU, "n=%d total=%d stage=%d", n, total, j)
				require.Equal(t, stages[j].TargetRPS, sumRPS, "n=%d total=%d stage=%d", n, total, j)
				require.LessOrEqual(t, hi-lo, 1, "n=%d total=%d stage=%d", n, total, j)
			}
			if total > 0 {
				prev := splitStageShares([]scenarios.Stage{{Target: total - 1}}, n)
				cur := splitStageShares([]scenarios.Stage{{Target: total}}, n)
				for w := range cur {
					require.GreaterOrEqual(t, cur[w][0].Target, prev[w][0].Target, "monotone n=%d total=%d", n, total)
				}
			}
			alloc := (&Coordinator{workers: workers, cfg: &scenarios.Scenario{Stages: stages}}).allocateVUs()
			for w, addr := range workers {
				peak := 0
				for _, sh := range shares[w] {
					peak = max(peak, sh.Target)
				}
				require.Equal(t, alloc[addr], peak, "n=%d total=%d worker=%d", n, total, w)
			}
		}
	}
}
