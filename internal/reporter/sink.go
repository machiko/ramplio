package reporter

import "github.com/machiko/ramplio/v2/internal/metrics"

// Sink is a write-only output target for test results.
// Implementations are responsible for their own I/O and must be safe for
// a single goroutine (the caller flushes exactly once after the test ends).
type Sink interface {
	Write(sum metrics.Summary, scenarioName string) error
	Close() error
}

// DetailedSink is an optional interface for sinks that support per-step/group breakdown.
// Implementations write detailed metrics to the sink when available.
type DetailedSink interface {
	Sink
	WriteDetailed(sum metrics.Summary, scenarioName string) error
}
