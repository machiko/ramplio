package engine

import (
	"context"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
)

// drainDispatch runs the dispatcher alone for d at the given rate, with a
// consumer that is never busy, and returns every ScheduledAt stamp it issued.
func drainDispatch(t *testing.T, rps float64, d time.Duration) []time.Time {
	t.Helper()
	e := NewRamp(RampConfig{}, metrics.NewCollector(1))
	t.Cleanup(func() { e.collector.Stop() })
	sched := newRateSchedule()
	sched.set(rps)
	pool := &ratePool{jobs: make(chan time.Time, 64), max: 1, start: func() {}}
	pool.idle.Store(1) // a worker is always waiting: maybeGrow never grows

	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var stamps []time.Time
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ts := range pool.jobs {
			stamps = append(stamps, ts)
		}
	}()
	e.dispatch(ctx, sched, pool)
	close(pool.jobs)
	<-done
	return stamps
}

// Lateness is attributed, not hidden: when the dispatcher wakes late it keeps
// the intended send time as ScheduledAt, so coordinated-omission correction
// sees the generator's own lag instead of a schedule that drifted to match.
// Consecutive stamps are therefore exactly one interval apart.
func TestDispatch_StampsFollowTheIntendedCadence(t *testing.T) {
	const rps = 1000
	stamps := drainDispatch(t, rps, 300*time.Millisecond)
	if len(stamps) < 250 {
		t.Fatalf("only %d stamps in 300ms at %d rps", len(stamps), rps)
	}
	interval := time.Second / rps
	for i := 1; i < len(stamps); i++ {
		if gap := stamps[i].Sub(stamps[i-1]); gap != interval {
			t.Fatalf("stamp %d: gap %v, want exactly %v (the schedule must never slip)", i, gap, interval)
		}
	}
}

// At rate 0 nothing is due; once a rate is set the schedule starts from then,
// not from the (idle) past — a pause must not be followed by a catch-up burst.
func TestDispatch_RateZeroIdlesThenRestarts(t *testing.T) {
	e := NewRamp(RampConfig{}, metrics.NewCollector(1))
	t.Cleanup(func() { e.collector.Stop() })
	sched := newRateSchedule() // rate 0
	pool := &ratePool{jobs: make(chan time.Time, 1024), max: 1, start: func() {}}
	pool.idle.Store(1)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	time.AfterFunc(400*time.Millisecond, func() { sched.set(100) })
	e.dispatch(ctx, sched, pool)
	close(pool.jobs)

	n := 0
	for ts := range pool.jobs {
		if ts.Before(t0.Add(390 * time.Millisecond)) {
			t.Fatalf("stamp %v is from the idle period", ts.Sub(t0))
		}
		n++
	}
	// ~300ms at 100 rps ≈ 30; a catch-up burst over the idle 400ms would be ~70.
	if n < 20 || n > 35 {
		t.Fatalf("got %d stamps after the rate was set, want ≈30 and no catch-up burst", n)
	}
}

// A rate increase takes effect at once: the send planned under the old, slower
// rate is pulled in to one new interval, not waited out.
func TestDispatch_RateIncreaseIsNotHeldBackByOldInterval(t *testing.T) {
	e := NewRamp(RampConfig{}, metrics.NewCollector(1))
	t.Cleanup(func() { e.collector.Stop() })
	sched := newRateSchedule()
	sched.set(0.5) // one request every 2s: the next send is due long after the test
	pool := &ratePool{jobs: make(chan time.Time, 1024), max: 1, start: func() {}}
	pool.idle.Store(1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	time.AfterFunc(40*time.Millisecond, func() { sched.set(1000) })
	e.dispatch(ctx, sched, pool)
	close(pool.jobs)

	n := 0
	for range pool.jobs {
		n++
	}
	// ≈360–460ms at 1000 rps once the change is noticed (within one re-read tick);
	// holding the old 2s gap would leave just the first request.
	if n < 250 {
		t.Fatalf("got %d stamps after raising the rate to 1000 rps, want ≥250", n)
	}
}
