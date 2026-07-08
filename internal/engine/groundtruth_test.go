package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/require"
)

// groundTruthRounds bounds the retry loop in the ground-truth tests. CPU
// contention from other packages running in parallel (go test -p) can only
// inflate measured latency, never deflate it — so one clean round within
// tolerance proves the measurement pipeline is accurate, and drifted rounds
// are contention noise worth retrying, not measurement bugs.
const groundTruthRounds = 3

// driftTol widens an upper-bound drift tolerance under the race detector,
// whose instrumentation inflates scheduling and handler overhead. Lower-bound
// invariants (measured >= injected) are never relaxed.
func driftTol(d time.Duration) time.Duration {
	if raceEnabled {
		return d * 3
	}
	return d
}

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

// runGroundTruthRound executes one full ramp against url and returns the
// measured summary. Each round gets a fresh collector and engine so retries
// never mix samples.
func runGroundTruthRound(url string, stage scenarios.Stage) metrics.Summary {
	col := metrics.NewCollector(20)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{stage},
		Steps:    []engine.RampStep{rampStep(url)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)
	return eng.Run(context.Background())
}

// TestGroundTruth_FixedLatency is the foundational accuracy proof: against a
// target with a known fixed service time, every measured percentile must land
// in [known, known+tolerance]. Measured latency can only exceed the injected
// delay (localhost round-trip + handler overhead), never fall below it — so
// the lower bound is asserted on every round, while the upper bound accepts
// the best of a few rounds to shrug off CPU contention from parallel packages.
func TestGroundTruth_FixedLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("ground-truth timing test skipped in -short mode")
	}
	const known = 30 * time.Millisecond
	tol := driftTol(20 * time.Millisecond)
	s := latencyServer(t, func(int64) time.Duration { return known })

	var last metrics.Summary
	for round := 1; round <= groundTruthRounds; round++ {
		sum := runGroundTruthRound(s.URL, scenarios.Stage{Duration: 2 * time.Second, Target: 10})

		require.Greater(t, sum.Total, int64(50), "expected a meaningful sample size")
		require.GreaterOrEqual(t, sum.P50, known, "p50 must not undercut injected latency")
		require.GreaterOrEqual(t, sum.P99, known, "p99 must not undercut injected latency")

		if sum.P50 <= known+tol && sum.P99 <= known+tol {
			return
		}
		last = sum
		t.Logf("round %d/%d drifted beyond tolerance (p50=%v p99=%v tol=%v) — likely CPU contention, retrying",
			round, groundTruthRounds, sum.P50, sum.P99, tol)
	}
	t.Fatalf("all %d rounds drifted beyond tolerance: p50=%v p99=%v (known=%v tol=%v)",
		groundTruthRounds, last.P50, last.P99, known, tol)
}

// TestGroundTruth_BimodalSeparatesTail proves the histogram separates a tail
// from the body: ~10% of requests are slow, so p50 must sit in the fast band
// while p95/p99 must sit in the slow band. A naive mean would smear these.
// Band lower bounds are asserted on every round; upper bounds accept the best
// of a few rounds, same rationale as TestGroundTruth_FixedLatency.
func TestGroundTruth_BimodalSeparatesTail(t *testing.T) {
	if testing.Short() {
		t.Skip("ground-truth timing test skipped in -short mode")
	}
	const (
		fast = 10 * time.Millisecond
		slow = 120 * time.Millisecond
	)
	fastTol := driftTol(40 * time.Millisecond)
	slowTol := driftTol(60 * time.Millisecond)
	// Every 10th request is slow → ~10% tail, regardless of VU interleaving.
	s := latencyServer(t, func(n int64) time.Duration {
		if n%10 == 0 {
			return slow
		}
		return fast
	})

	var last metrics.Summary
	for round := 1; round <= groundTruthRounds; round++ {
		sum := runGroundTruthRound(s.URL, scenarios.Stage{Duration: 3 * time.Second, Target: 10})

		require.Greater(t, sum.Total, int64(100), "expected a meaningful sample size")
		// 90% of requests are fast → median sits in the fast band.
		require.GreaterOrEqual(t, sum.P50, fast)
		// Top ~10% are slow → p99 sits in the slow band.
		require.GreaterOrEqual(t, sum.P99, slow, "p99 should reflect the slow tail")

		if sum.P50 < fast+fastTol && sum.P99 < slow+slowTol {
			return
		}
		last = sum
		t.Logf("round %d/%d drifted beyond tolerance (p50=%v p99=%v) — likely CPU contention, retrying",
			round, groundTruthRounds, sum.P50, sum.P99)
	}
	t.Fatalf("all %d rounds drifted beyond tolerance: p50=%v p99=%v (fast=%v slow=%v)",
		groundTruthRounds, last.P50, last.P99, fast, slow)
}
