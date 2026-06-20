package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/engine"
	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/scenarios"
	"github.com/stretchr/testify/assert"
)

// latencyServer responds after a deterministic per-request delay chosen by fn.
// The request count (1-based) is passed to fn so callers can shape a known
// latency distribution — the ground truth we validate measurements against.
func latencyServer(t *testing.T, fn func(n int64) time.Duration) *httptest.Server {
	t.Helper()
	var n atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d := fn(n.Add(1)); d > 0 {
			time.Sleep(d)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	return s
}

// TestGroundTruth_FixedLatency is the foundational accuracy proof: against a
// target with a known fixed service time, every measured percentile must land
// in [known, known+tolerance]. Measured latency can only exceed the injected
// delay (localhost round-trip + handler overhead), never fall below it.
func TestGroundTruth_FixedLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("ground-truth timing test skipped in -short mode")
	}
	const known = 30 * time.Millisecond
	s := latencyServer(t, func(int64) time.Duration { return known })

	col := metrics.NewCollector(20)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 2 * time.Second, Target: 10}},
		Steps:    []engine.RampStep{rampStep(s.URL)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	assert.Greater(t, sum.Total, int64(50), "expected a meaningful sample size")
	const tol = 20 * time.Millisecond
	assert.GreaterOrEqual(t, sum.P50, known, "p50 must not undercut injected latency")
	assert.LessOrEqual(t, sum.P50, known+tol, "p50 drifted beyond tolerance")
	assert.GreaterOrEqual(t, sum.P99, known, "p99 must not undercut injected latency")
	assert.LessOrEqual(t, sum.P99, known+tol, "p99 drifted beyond tolerance")
}

// TestGroundTruth_BimodalSeparatesTail proves the histogram separates a tail
// from the body: ~10% of requests are slow, so p50 must sit in the fast band
// while p95/p99 must sit in the slow band. A naive mean would smear these.
func TestGroundTruth_BimodalSeparatesTail(t *testing.T) {
	if testing.Short() {
		t.Skip("ground-truth timing test skipped in -short mode")
	}
	const (
		fast = 10 * time.Millisecond
		slow = 120 * time.Millisecond
	)
	// Every 10th request is slow → ~10% tail, regardless of VU interleaving.
	s := latencyServer(t, func(n int64) time.Duration {
		if n%10 == 0 {
			return slow
		}
		return fast
	})

	col := metrics.NewCollector(20)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 3 * time.Second, Target: 10}},
		Steps:    []engine.RampStep{rampStep(s.URL)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	assert.Greater(t, sum.Total, int64(100), "expected a meaningful sample size")
	// 90% of requests are fast → median sits in the fast band.
	assert.GreaterOrEqual(t, sum.P50, fast)
	assert.Less(t, sum.P50, fast+40*time.Millisecond, "p50 should reflect the fast majority")
	// Top ~10% are slow → p99 sits in the slow band.
	assert.GreaterOrEqual(t, sum.P99, slow, "p99 should reflect the slow tail")
	assert.Less(t, sum.P99, slow+60*time.Millisecond, "p99 drifted beyond tolerance")
}
