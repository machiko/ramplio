package engine

import (
	"sync"
	"sync/atomic"
	"time"
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
func (w *failWindow) reset() {
	w.mu.Lock()
	for i := range w.times {
		w.times[i] = time.Time{}
	}
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
