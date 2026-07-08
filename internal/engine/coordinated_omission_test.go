package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRateMode_PopulatesCorrectedLatency proves rate (open) mode threads a
// scheduled dispatch time through to the collector. With ample worker headroom
// the generator keeps up, so the corrected latency must track service latency
// closely — no false coordinated-omission inflation when none occurred.
func TestRateMode_PopulatesCorrectedLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("rate-mode timing test skipped in -short mode")
	}
	const known = 20 * time.Millisecond
	s := latencyServer(t, func(int64) time.Duration { return known })

	col := metrics.NewCollector(500)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 2 * time.Second, TargetRPS: 100}},
		Steps:    []engine.RampStep{rampStep(s.URL)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	assert.Greater(t, sum.Total, int64(50))
	require.True(t, sum.HasCorrected, "rate mode must populate corrected latency")
	assert.GreaterOrEqual(t, sum.CorrectedP99, sum.P99, "corrected can never undercut service time")
	assert.Less(t, sum.CorrectedP99, sum.P99+100*time.Millisecond,
		"with headroom the generator keeps up; corrected should not inflate")
}

// TestVUMode_NoCorrectedLatency confirms closed-loop mode reports no corrected
// latency — there is no dispatch schedule to omit against.
func TestVUMode_NoCorrectedLatency(t *testing.T) {
	s := okServer(t)

	col := metrics.NewCollector(20)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 300 * time.Millisecond, Target: 5}},
		Steps:    []engine.RampStep{rampStep(s.URL)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	assert.False(t, sum.HasCorrected, "VU mode has no schedule, so no correction")
}
