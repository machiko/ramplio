package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
)

type Config struct {
	VUs      int
	Duration time.Duration
	Request  protocols.Request
	Executor protocols.Executor
}

type Engine struct {
	cfg       Config
	collector *metrics.Collector
}

func New(cfg Config, collector *metrics.Collector) *Engine {
	return &Engine{cfg: cfg, collector: collector}
}

// Run starts the VU pool and blocks until duration expires or ctx is cancelled.
func (e *Engine) Run(ctx context.Context) metrics.Summary {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Duration)
	defer cancel()

	start := time.Now()

	var wg sync.WaitGroup
	for range e.cfg.VUs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.runVU(ctx)
		}()
	}
	wg.Wait()

	sum := e.collector.Stop()
	sum.WallTime = time.Since(start)
	return sum
}

func (e *Engine) runVU(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			result := e.cfg.Executor.Execute(ctx, e.cfg.Request)
			// Context cancellation during engine shutdown is expected — not a test error.
			if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, context.DeadlineExceeded) {
				return
			}
			e.collector.Add(metrics.Sample{
				Latency:    result.Latency,
				StatusCode: result.StatusCode,
				BytesRead:  result.BytesRead,
				Error:      result.Error,
				At:         time.Now(),
			})
		}
	}
}
