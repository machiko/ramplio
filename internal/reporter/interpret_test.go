package reporter_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterpret_Tiers(t *testing.T) {
	tests := []struct {
		name       string
		sum        metrics.Summary
		level      string
		speedLabel string
		stability  string
	}{
		{
			name:       "healthy",
			sum:        metrics.Summary{Total: 20046, Errors: 0, WallTime: 5 * time.Second, P99: 9 * time.Millisecond},
			level:      "pass",
			speedLabel: "非常快（幾乎即時）",
			stability:  "完美",
		},
		{
			name:       "warn on latency",
			sum:        metrics.Summary{Total: 1000, Errors: 0, WallTime: 10 * time.Second, P99: 1500 * time.Millisecond},
			level:      "warn",
			speedLabel: "偏慢",
			stability:  "完美",
		},
		{
			name:       "warn on errors",
			sum:        metrics.Summary{Total: 1000, Errors: 30, WallTime: 5 * time.Second, P99: 50 * time.Millisecond},
			level:      "warn",
			speedLabel: "非常快（幾乎即時）",
			stability:  "有點不穩",
		},
		{
			name:       "fail on errors",
			sum:        metrics.Summary{Total: 1000, Errors: 100, WallTime: 5 * time.Second, P99: 50 * time.Millisecond},
			level:      "fail",
			speedLabel: "非常快（幾乎即時）",
			stability:  "不穩定",
		},
		{
			name:       "fail on latency",
			sum:        metrics.Summary{Total: 1000, Errors: 0, WallTime: 10 * time.Second, P99: 4 * time.Second},
			level:      "fail",
			speedLabel: "很慢",
			stability:  "完美",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := reporter.Interpret(tt.sum)
			assert.Equal(t, tt.level, in.Level)
			assert.Equal(t, tt.speedLabel, in.Speed.Label)
			assert.Equal(t, tt.stability, in.Stability.Label)
			assert.NotEmpty(t, in.OneLiner)
			assert.NotEmpty(t, in.Capacity.Value)
		})
	}
}

func TestInterpret_BottleneckOnlyWithMultipleSteps(t *testing.T) {
	single := reporter.Interpret(metrics.Summary{Total: 10, Steps: []metrics.StepSummary{{Name: "a"}}})
	assert.Empty(t, single.Bottleneck)

	multi := reporter.Interpret(metrics.Summary{
		Total: 10,
		Steps: []metrics.StepSummary{
			{Name: "fast", P99: 5 * time.Millisecond},
			{Name: "slow", P99: 200 * time.Millisecond},
		},
	})
	assert.Contains(t, multi.Bottleneck, "slow")
}

// TestOutputsShareWording proves the terminal and JSON outputs render the exact
// same plain-language verdict — the whole point of the shared Interpret source.
func TestOutputsShareWording(t *testing.T) {
	sum := metrics.Summary{Total: 20046, Errors: 0, WallTime: 5 * time.Second, P99: 9 * time.Millisecond}
	in := reporter.Interpret(sum)

	var term bytes.Buffer
	reporter.PrintSummary(&term, sum)
	assert.Contains(t, term.String(), in.Verdict)
	assert.Contains(t, term.String(), in.Speed.Label)
	assert.Contains(t, term.String(), in.OneLiner)

	report := reporter.SummaryToReport(sum)
	require.Equal(t, in.Verdict, report.Verdict.Verdict)
	require.Equal(t, in.Speed.Label, report.Verdict.Speed.Label)
	require.Equal(t, in.OneLiner, report.Verdict.OneLiner)
}
