package engine

// 熔斷器:滑動時間窗口內失敗達門檻即觸發並鎖存,所有 VU 與 dispatcher
// 在每次請求前輪詢。

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/machiko/ramplio/v3/internal/scenarios"
)

// failWindow implements a sliding-time-window failure counter for the circuit breaker.
// Unlike a bare atomic counter, it avoids false trips when many VUs fail simultaneously
// in a burst but then recover quickly.
type failWindow struct {
	mu        sync.Mutex
	times     []time.Time // ring buffer of recent failure timestamps
	head      int
	threshold int
	window    time.Duration
	// tripped is atomic because every VU and the rate dispatcher poll it
	// lock-free (isCircuitTripped) while record sets it under mu.
	tripped atomic.Bool
	// dirty is set (under mu) while the ring holds a failure recorded since
	// the last reset. A success on a clean window — every request of a healthy
	// run — then returns on one atomic load, instead of serialising all VUs on
	// mu to zero a ring that is already empty.
	dirty atomic.Bool
}

// newBreakerWindow builds the circuit breaker's window from the scenario
// config, or returns nil when the breaker is disabled.
func newBreakerWindow(cb *scenarios.CircuitBreakerConfig) *failWindow {
	if cb == nil || cb.ConsecutiveFailures <= 0 {
		return nil
	}
	windowDur := time.Duration(cb.WindowSeconds) * time.Second
	if windowDur <= 0 {
		windowDur = time.Second
	}
	return newFailWindow(cb.ConsecutiveFailures, windowDur)
}

func newFailWindow(threshold int, windowDur time.Duration) *failWindow {
	return &failWindow{
		times:     make([]time.Time, threshold),
		threshold: threshold,
		window:    windowDur,
	}
}

// record adds a failure timestamp and returns whether the circuit should trip.
func (w *failWindow) record(t time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tripped.Load() {
		return true
	}
	w.times[w.head] = t
	w.head = (w.head + 1) % w.threshold
	w.dirty.Store(true)
	cutoff := t.Add(-w.window)
	count := 0
	for _, ts := range w.times {
		if !ts.IsZero() && ts.After(cutoff) {
			count++
		}
	}
	if count >= w.threshold {
		w.tripped.Store(true)
		return true
	}
	return false
}

// reset clears the window (called on successful request).
//
// Skipping the lock on a clean window is safe because record and the slow
// path below maintain, under mu, the invariant "the ring holds a failure ⇒
// dirty is true": a reset that loads false therefore saw an empty ring. The
// only record it can miss is one running concurrently, and ordering the reset
// before that record is a valid outcome of the race either way.
func (w *failWindow) reset() {
	if !w.dirty.Load() {
		return
	}
	w.mu.Lock()
	clear(w.times)
	w.dirty.Store(false)
	w.mu.Unlock()
}

// isCircuitTripped returns true when the circuit breaker has fired.
func (e *RampEngine) isCircuitTripped() bool {
	return e.cbWindow != nil && e.cbWindow.tripped.Load()
}

func (e *RampEngine) incFails() {
	if e.cbWindow != nil {
		e.cbWindow.record(time.Now())
	}
}

// recordSuccess tells the breaker a request succeeded, wiping the failures
// counted so far: the breaker counts consecutive failures.
func (e *RampEngine) recordSuccess() {
	if e.cbWindow != nil {
		e.cbWindow.reset()
	}
}
