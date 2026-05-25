package reporter

import "github.com/ramplio/ramplio/internal/metrics"

// Sink is a write-only output target for test results.
// Implementations are responsible for their own I/O and must be safe for
// a single goroutine (the caller flushes exactly once after the test ends).
type Sink interface {
	Write(sum metrics.Summary, scenarioName string) error
	Close() error
}
