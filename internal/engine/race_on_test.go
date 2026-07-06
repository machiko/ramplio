//go:build race

package engine_test

// raceEnabled reports whether the race detector is compiled in. Timing-based
// ground-truth tests widen their drift tolerances under -race because the
// detector's instrumentation inflates scheduling and handler overhead.
const raceEnabled = true
