package engine

import (
	"context"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

// BenchmarkBreakerSuccessPath is the per-request breaker cost of a healthy
// run: every VU polls the trip flag and records a success, in parallel.
func BenchmarkBreakerSuccessPath(b *testing.B) {
	e := &RampEngine{cbWindow: newFailWindow(20, 10*time.Second)}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if e.isCircuitTripped() {
				b.Error("tripped on an all-success run")
				return
			}
			e.cbWindow.reset()
		}
	})
}

// BenchmarkBreakerMixedPath is the same with 1% of requests failing: the
// window is then often dirty and a success takes the lock to clear it, as it
// always did. The lock-free path must not make this case slower.
func BenchmarkBreakerMixedPath(b *testing.B) {
	e := &RampEngine{cbWindow: newFailWindow(20, 10*time.Second)}
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if e.isCircuitTripped() {
				b.Error("tripped: failures are never consecutive here")
				return
			}
			if i++; i%100 == 0 {
				e.incFails()
			} else {
				e.cbWindow.reset()
			}
		}
	})
}

// instantExec answers every request at once, so a run's throughput is bound
// by the engine's own per-request work.
type instantExec struct{}

func (instantExec) Execute(context.Context, protocols.Request) protocols.Result {
	return protocols.Result{StatusCode: 200}
}

// BenchmarkVUThroughputBreaker runs 64 VUs against an instant target with the
// circuit breaker on (and off, for reference) and reports requests per second.
func BenchmarkVUThroughputBreaker(b *testing.B) {
	for _, on := range []bool{true, false} {
		name := "breaker=off"
		var cb *scenarios.CircuitBreakerConfig
		if on {
			name = "breaker=on"
			cb = &scenarios.CircuitBreakerConfig{ConsecutiveFailures: 20, WindowSeconds: 10}
		}
		b.Run(name, func(b *testing.B) {
			stages := []scenarios.Stage{{Duration: 10 * time.Millisecond, Target: 64}, {Duration: 300 * time.Millisecond, Target: 64}}
			var total, secs float64
			for range b.N {
				col := metrics.NewCollector(CollectorCapacity(stages))
				sum := NewRamp(RampConfig{
					Stages:         stages,
					Steps:          []RampStep{{Request: protocols.Request{Method: "GET", URL: "http://unused.test"}}},
					Executor:       instantExec{},
					CircuitBreaker: cb,
				}, col).Run(context.Background())
				total += float64(sum.Total + sum.DroppedSamples)
				secs += sum.WallTime.Seconds()
			}
			b.ReportMetric(total/secs, "req/s")
		})
	}
}
