package engine_test

import (
	"context"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
)

// runCadence runs a rate-mode schedule against a zero-latency executor (no
// network, no target: only the generator's own pacing is measured) and returns
// how many requests it dispatched.
func runCadence(t *testing.T, stages []scenarios.Stage) (int64, metrics.Summary) {
	t.Helper()
	exec := &countingExec{}
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   stages,
		Steps:    []engine.RampStep{rampStep("http://unused.test")},
		Executor: exec,
	}, testCollector(t, engine.CollectorCapacity(stages)))
	sum := eng.Run(context.Background())
	return int64(exec.calls.Load()), sum
}

// The generator must deliver the declared rate. Timer wake-ups overshoot by
// tens of µs; a dispatcher that cannot catch up loses that slice of every
// interval and silently sends 10–25% less than asked at high rates — a load
// test that quietly under-loads is lying about the number on its own report.
func TestRateMode_GeneratorDeliversDeclaredRate(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-dependent rate test skipped in -short mode")
	}
	const hold = time.Second
	for _, rps := range []int{200, 2000, 6000} {
		t.Run(fmtRPS(rps), func(t *testing.T) {
			sent, sum := runCadence(t, []scenarios.Stage{
				{Duration: 10 * time.Millisecond, TargetRPS: rps},
				{Duration: hold, TargetRPS: rps},
			})
			planned := float64(rps) * (hold + 10*time.Millisecond).Seconds()
			achieved := float64(sent) / planned
			t.Logf("rps=%d sent=%d planned≈%.0f achieved=%.1f%%", rps, sent, planned, achieved*100)
			assert.GreaterOrEqual(t, achieved, 0.97, "generator fell short of the declared rate")
			assert.LessOrEqual(t, achieved, 1.03, "generator overshot the declared rate")
			// The run reports what it was asked to send, so the report can
			// compare it with what went out.
			assert.InDelta(t, planned, sum.GeneratorPlannedRequests, planned*0.03)
		})
	}
}

// Ramping: the rate follows the stage profile, so a 0→R ramp sends about half
// of R×duration, and a stage at rate 0 sends nothing.
func TestRateMode_RampFollowsProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-dependent rate test skipped in -short mode")
	}
	sent, _ := runCadence(t, []scenarios.Stage{
		{Duration: time.Second, TargetRPS: 1000}, // 0 → 1000 linear: ≈500 requests
		{Duration: 300 * time.Millisecond, TargetRPS: 0},
	})
	// The ramp-down 1000→0 adds ≈150; both ramps are sampled every 100ms.
	assert.InDelta(t, 650, sent, 110, "sent=%d", sent)
}

// delayExec answers every request after a fixed delay — a slow but healthy
// target. A request still in flight when the run ends is cancelled.
type delayExec struct{ d time.Duration }

func (x delayExec) Execute(ctx context.Context, _ protocols.Request) protocols.Result {
	select {
	case <-time.After(x.d):
		return protocols.Result{StatusCode: 200, Latency: x.d}
	case <-ctx.Done():
		return protocols.Result{Error: ctx.Err()}
	}
}

// The shortfall disclosure must describe the generator, not the samples: a
// slow target leaves requests in flight at the end (sent, never recorded) and
// a skipped `if` step consumes its slot without a sample. Neither is the
// generator falling behind, and neither may be reported as one.
func TestRateMode_NoFalseShortfall(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-dependent rate test skipped in -short mode")
	}
	stages := []scenarios.Stage{{Duration: 10 * time.Millisecond, TargetRPS: 200}, {Duration: time.Second, TargetRPS: 200}}
	cases := map[string]engine.RampConfig{
		"slow target": {Stages: stages, Steps: []engine.RampStep{rampStep("http://unused.test")}, Executor: delayExec{200 * time.Millisecond}},
		"if-skipped step": {Stages: stages, Executor: &countingExec{}, Steps: []engine.RampStep{
			rampStep("http://unused.test"),
			{Request: rampStep("http://unused.test").Request, If: `vars.on == "1"`},
		}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			sum := engine.NewRamp(cfg, testCollector(t, engine.CollectorCapacity(stages))).Run(context.Background())
			r := reporter.MeasurementConfidence(sum)
			t.Logf("total=%d planned=%d dispatched=%d level=%s", sum.Total, sum.GeneratorPlannedRequests, sum.GeneratorDispatchedRequests, r.Level)
			assert.NotContains(t, r.Note, "排程", "the generator kept its schedule; samples ≠ sends")
			// The bug this guards read ~50–80%; 10% leaves room for a slow CI box.
			assert.InDelta(t, sum.GeneratorPlannedRequests, sum.GeneratorDispatchedRequests, float64(sum.GeneratorPlannedRequests)*0.10)
		})
	}
}

func fmtRPS(rps int) string { return "rps=" + strconv.Itoa(rps) }

// BenchmarkRateGenerator measures the generator alone (zero-latency executor):
// how much of the declared rate it delivers and what each dispatched request
// costs the generator in CPU. Run interleaved against a baseline binary.
func BenchmarkRateGenerator(b *testing.B) {
	const rps = 3000
	stages := []scenarios.Stage{{Duration: 10 * time.Millisecond, TargetRPS: rps}, {Duration: time.Second, TargetRPS: rps}}
	var sent, cpuNs float64
	for range b.N {
		exec := &countingExec{}
		col := metrics.NewCollector(engine.CollectorCapacity(stages))
		eng := engine.NewRamp(engine.RampConfig{Stages: stages, Steps: []engine.RampStep{rampStep("http://unused.test")}, Executor: exec}, col)
		before := cpuTime()
		eng.Run(context.Background())
		cpuNs += float64(cpuTime() - before)
		sent += float64(exec.calls.Load())
	}
	b.ReportMetric(sent/float64(b.N)/(rps*1.01)*100, "achieved%")
	b.ReportMetric(cpuNs/sent, "cpu-ns/req")
}

// cpuTime is the process's user+system CPU time.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
