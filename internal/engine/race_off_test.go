//go:build !race

package engine_test

// raceEnabled reports whether the race detector is compiled in. See
// race_on_test.go for why timing tests care.
const raceEnabled = false
