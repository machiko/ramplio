package metrics

import (
	"sync"
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

const (
	bufMultiplier = 10
	histMinNs     = 1                // 1 nanosecond
	histMaxNs     = 60_000_000_000   // 60 seconds in nanoseconds
	histSigFigs   = 3                // 0.1% precision
)

type Collector struct {
	ch        chan Sample
	quit      chan struct{}
	stopped   chan struct{}
	mu        sync.RWMutex
	sum       Summary
	hist      *hdrhistogram.Histogram
	once      sync.Once
	startedAt time.Time
}

func NewCollector(maxVUs int) *Collector {
	if maxVUs < 1 {
		maxVUs = 1
	}
	c := &Collector{
		ch:        make(chan Sample, maxVUs*bufMultiplier),
		quit:      make(chan struct{}),
		stopped:   make(chan struct{}),
		hist:      hdrhistogram.New(histMinNs, histMaxNs, histSigFigs),
		startedAt: time.Now(),
	}
	go c.aggregate()
	return c
}

// Add sends a sample to the collector. Non-blocking: drops if buffer full or collector stopped.
func (c *Collector) Add(s Sample) {
	select {
	case c.ch <- s:
	case <-c.quit:
	default:
	}
}

// Stop signals the collector to finish, drains remaining samples, and returns the summary
// with HDR-computed percentiles. Safe to call multiple times.
func (c *Collector) Stop() Summary {
	c.once.Do(func() { close(c.quit) })
	<-c.stopped

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hist.TotalCount() > 0 {
		c.sum.P50 = nsToD(c.hist.ValueAtQuantile(50))
		c.sum.P90 = nsToD(c.hist.ValueAtQuantile(90))
		c.sum.P95 = nsToD(c.hist.ValueAtQuantile(95))
		c.sum.P99 = nsToD(c.hist.ValueAtQuantile(99))
	}
	return c.sum
}

// LiveSummary returns a point-in-time snapshot of the running summary with a live WallTime.
// Safe to call concurrently with Add.
func (c *Collector) LiveSummary() Summary {
	c.mu.RLock()
	s := c.sum
	c.mu.RUnlock()
	s.WallTime = time.Since(c.startedAt)
	return s
}

// LivePercentiles returns current HDR histogram percentiles.
// Safe to call concurrently with Add.
func (c *Collector) LivePercentiles() (p50, p90, p95, p99 time.Duration) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.hist.TotalCount() == 0 {
		return 0, 0, 0, 0
	}
	return nsToD(c.hist.ValueAtQuantile(50)),
		nsToD(c.hist.ValueAtQuantile(90)),
		nsToD(c.hist.ValueAtQuantile(95)),
		nsToD(c.hist.ValueAtQuantile(99))
}

func (c *Collector) aggregate() {
	defer close(c.stopped)
	for {
		select {
		case s := <-c.ch:
			c.recordSample(s)
		case <-c.quit:
			for {
				select {
				case s := <-c.ch:
					c.recordSample(s)
				default:
					return
				}
			}
		}
	}
}

func (c *Collector) recordSample(s Sample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sum.record(s)
	if ns := int64(s.Latency); ns > 0 {
		_ = c.hist.RecordValue(ns)
	}
}

func nsToD(ns int64) time.Duration {
	return time.Duration(ns)
}
