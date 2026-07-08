package reporter

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/stretchr/testify/assert"
)

func TestCsvSink_Write_CreatesFileWithHeaders(t *testing.T) {
	tmpFile := t.TempDir() + "/test.csv"

	sink, err := NewCsvSink(tmpFile)
	assert.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{
		Total:      100,
		Errors:     5,
		WallTime:   10 * time.Second,
		P50:        100 * time.Millisecond,
		P95:        200 * time.Millisecond,
		P99:        300 * time.Millisecond,
		MaxLatency: 500 * time.Millisecond,
		BytesIn:    50000,
	}

	err = sink.Write(sum, "test-scenario")
	assert.NoError(t, err)

	content, err := os.ReadFile(tmpFile)
	assert.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	assert.Equal(t, 2, len(lines), "should have header + 1 data row")
	assert.Contains(t, lines[0], "timestamp,scenario,type,name")
	assert.Contains(t, lines[1], "test-scenario,global")
}

func TestCsvSink_WriteDetailed_OutputsGlobalStepsAndGroups(t *testing.T) {
	tmpFile := t.TempDir() + "/test.csv"

	sink, err := NewCsvSink(tmpFile)
	assert.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{
		Total:      100,
		Errors:     5,
		WallTime:   10 * time.Second,
		P50:        100 * time.Millisecond,
		P95:        200 * time.Millisecond,
		P99:        300 * time.Millisecond,
		MaxLatency: 500 * time.Millisecond,
		BytesIn:    50000,
		Steps: []metrics.StepSummary{
			{
				Name:   "get-user",
				Total:  50,
				Errors: 2,
				P50:    80 * time.Millisecond,
				P95:    150 * time.Millisecond,
				P99:    200 * time.Millisecond,
			},
			{
				Name:   "post-comment",
				Total:  50,
				Errors: 3,
				P50:    120 * time.Millisecond,
				P95:    250 * time.Millisecond,
				P99:    400 * time.Millisecond,
			},
		},
		Groups: []metrics.GroupSummary{
			{
				Name:   "auth-flow",
				Total:  100,
				Errors: 5,
				P50:    100 * time.Millisecond,
				P95:    200 * time.Millisecond,
				P99:    300 * time.Millisecond,
			},
		},
	}

	err = sink.WriteDetailed(sum, "test-scenario")
	assert.NoError(t, err)

	content, err := os.ReadFile(tmpFile)
	assert.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	// header + global + 2 steps + 1 group = 5 rows
	assert.Equal(t, 5, len(lines), "should have header + global + 2 steps + 1 group")

	// Check global row
	assert.Contains(t, lines[1], "global")

	// Check step rows
	assert.Contains(t, lines[2], "step,get-user")
	assert.Contains(t, lines[3], "step,post-comment")

	// Check group row
	assert.Contains(t, lines[4], "group,auth-flow")
}

func TestCsvSink_WriteDetailed_EmptyStepsAndGroups(t *testing.T) {
	tmpFile := t.TempDir() + "/test.csv"

	sink, err := NewCsvSink(tmpFile)
	assert.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{
		Total:      100,
		Errors:     0,
		WallTime:   10 * time.Second,
		P50:        100 * time.Millisecond,
		P95:        200 * time.Millisecond,
		P99:        300 * time.Millisecond,
		MaxLatency: 500 * time.Millisecond,
		BytesIn:    50000,
		Steps:      nil,
		Groups:     nil,
	}

	err = sink.WriteDetailed(sum, "test-scenario")
	assert.NoError(t, err)

	content, err := os.ReadFile(tmpFile)
	assert.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	// header + global only
	assert.Equal(t, 2, len(lines))
}
