package distributed

import (
	"testing"

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
