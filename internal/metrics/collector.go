package metrics

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

// goroutineSampleInterval is how often the aggregator records the live goroutine
// count to track the generator's peak concurrency — frequent enough to catch a
// spike, sparse enough to add no measurable load.
const goroutineSampleInterval = 250 * time.Millisecond

// StepSummary holds live metrics for a single named scenario step.
type StepSummary struct {
	Name   string
	Total  int64
	Errors int64
	P50    time.Duration
	P90    time.Duration
	P95    time.Duration
	P99    time.Duration
}

// GroupSummary holds aggregated metrics across all steps in a named group.
type GroupSummary struct {
	Name   string
	Total  int64
	Errors int64
	P50    time.Duration
	P95    time.Duration
	P99    time.Duration
}

const (
	bufMultiplier = 10
	histMinNs     = 1              // 1 nanosecond
	histMaxNs     = 60_000_000_000 // 60 seconds in nanoseconds
	histSigFigs   = 3              // 0.1% precision
)

type Collector struct {
	ch        chan Sample
	quit      chan struct{}
	stopped   chan struct{}
	mu        sync.RWMutex
	sum       Summary
	hist      *hdrhistogram.Histogram
	corrHist  *hdrhistogram.Histogram // coordinated-omission-corrected latency (rate mode only)
	ttftHist  *hdrhistogram.Histogram // time-to-first-token (stream steps only)
	once      sync.Once
	startedAt time.Time
	dropped   atomic.Int64 // samples discarded due to full channel
	// Generator self-health baselines, sampled by the aggregator goroutine only.
	gcPauseBaseNs  uint64
	gcCountBase    uint32
	peakGoroutines int
	// Per-step tracking; only populated when samples carry a non-empty StepName.
	stepHists map[string]*hdrhistogram.Histogram
	stepSums  map[string]Summary
	stepOrder []string // insertion-ordered step names for stable display
	// Per-group tracking; only populated when samples carry a non-empty Group.
	groupHists map[string]*hdrhistogram.Histogram
	groupSums  map[string]Summary
	groupOrder []string // insertion-ordered group names for stable display
}

func NewCollector(maxVUs int) *Collector {
	if maxVUs < 1 {
		maxVUs = 1
	}
	c := &Collector{
		ch:         make(chan Sample, maxVUs*bufMultiplier),
		quit:       make(chan struct{}),
		stopped:    make(chan struct{}),
		hist:       hdrhistogram.New(histMinNs, histMaxNs, histSigFigs),
		corrHist:   hdrhistogram.New(histMinNs, histMaxNs, histSigFigs),
		ttftHist:   hdrhistogram.New(histMinNs, histMaxNs, histSigFigs),
		startedAt:  time.Now(),
		stepHists:  make(map[string]*hdrhistogram.Histogram),
		stepSums:   make(map[string]Summary),
		groupHists: make(map[string]*hdrhistogram.Histogram),
		groupSums:  make(map[string]Summary),
	}
	// Baseline GC pause so Stop() can report how much the generator paused during
	// the run — a self-health signal for measurement confidence.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	c.gcPauseBaseNs = ms.PauseTotalNs
	c.gcCountBase = ms.NumGC
	go c.aggregate()
	return c
}

// Add sends a sample to the collector. Non-blocking: drops if buffer full or collector stopped.
func (c *Collector) Add(s Sample) {
	select {
	case c.ch <- s:
	case <-c.quit:
	default:
		c.dropped.Add(1)
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
	if c.corrHist.TotalCount() > 0 {
		c.sum.HasCorrected = true
		c.sum.CorrectedP50 = nsToD(c.corrHist.ValueAtQuantile(50))
		c.sum.CorrectedP90 = nsToD(c.corrHist.ValueAtQuantile(90))
		c.sum.CorrectedP95 = nsToD(c.corrHist.ValueAtQuantile(95))
		c.sum.CorrectedP99 = nsToD(c.corrHist.ValueAtQuantile(99))
	}
	if c.ttftHist.TotalCount() > 0 {
		c.sum.HasTTFT = true
		c.sum.TTFTP50 = nsToD(c.ttftHist.ValueAtQuantile(50))
		c.sum.TTFTP90 = nsToD(c.ttftHist.ValueAtQuantile(90))
		c.sum.TTFTP95 = nsToD(c.ttftHist.ValueAtQuantile(95))
		c.sum.TTFTP99 = nsToD(c.ttftHist.ValueAtQuantile(99))
	}
	if len(c.stepOrder) > 0 {
		steps := make([]StepSummary, 0, len(c.stepOrder))
		for _, name := range c.stepOrder {
			hist := c.stepHists[name]
			sum := c.stepSums[name]
			ss := StepSummary{Name: name, Total: sum.Total, Errors: sum.Errors}
			if hist.TotalCount() > 0 {
				ss.P50 = nsToD(hist.ValueAtQuantile(50))
				ss.P90 = nsToD(hist.ValueAtQuantile(90))
				ss.P95 = nsToD(hist.ValueAtQuantile(95))
				ss.P99 = nsToD(hist.ValueAtQuantile(99))
			}
			steps = append(steps, ss)
		}
		c.sum.Steps = steps
	}
	if len(c.groupOrder) > 0 {
		groups := make([]GroupSummary, 0, len(c.groupOrder))
		for _, name := range c.groupOrder {
			hist := c.groupHists[name]
			sum := c.groupSums[name]
			gs := GroupSummary{Name: name, Total: sum.Total, Errors: sum.Errors}
			if hist.TotalCount() > 0 {
				gs.P50 = nsToD(hist.ValueAtQuantile(50))
				gs.P95 = nsToD(hist.ValueAtQuantile(95))
				gs.P99 = nsToD(hist.ValueAtQuantile(99))
			}
			groups = append(groups, gs)
		}
		c.sum.Groups = groups
	}
	c.sum.DroppedSamples = c.dropped.Load()

	// Generator self-health: how much the generator itself paused for GC and its
	// peak goroutine count during the run. Read after the aggregator has exited
	// (we are past <-c.stopped), so peakGoroutines is safe to read here.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	c.sum.GeneratorGCPause = time.Duration(ms.PauseTotalNs - c.gcPauseBaseNs)
	c.sum.GeneratorGCCount = int64(ms.NumGC - c.gcCountBase)
	c.sum.GeneratorPeakGoroutines = c.peakGoroutines

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
	mon := time.NewTicker(goroutineSampleInterval)
	defer mon.Stop()
	for {
		select {
		case s := <-c.ch:
			c.recordSample(s)
		case <-mon.C:
			// Only the aggregator writes peakGoroutines; Stop() reads it after this
			// goroutine has returned (via the stopped channel), so no lock is needed.
			if g := runtime.NumGoroutine(); g > c.peakGoroutines {
				c.peakGoroutines = g
			}
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
	// Coordinated-omission correction: when a scheduled dispatch time is present
	// (rate mode), the latency the user really sees is from when the request was
	// due, not when a worker happened to send it. Floor at service latency to
	// guard against clock skew making the corrected value smaller.
	if !s.ScheduledAt.IsZero() {
		corrected := s.At.Sub(s.ScheduledAt)
		if corrected < s.Latency {
			corrected = s.Latency
		}
		if ns := int64(corrected); ns > 0 {
			_ = c.corrHist.RecordValue(ns)
		}
	}
	// TTFT(串流首 chunk 到達)只在 stream 步驟的樣本上出現。
	// rate 模式做與 corrHist 同一模型的 CO 修正:generator 追不上排程時,
	// 使用者從「請求應送出」就開始等——TTFT 若只從實際發送起算會系統性
	// 低報,corrected_latency 飆高而 ttft 看似健康,兩數字互相矛盾。
	// 排隊等待 = (At - ScheduledAt) - Latency,floor 0 防時鐘偏移。
	if s.TTFT > 0 {
		ttft := s.TTFT
		if !s.ScheduledAt.IsZero() {
			if queueDelay := s.At.Sub(s.ScheduledAt) - s.Latency; queueDelay > 0 {
				ttft += queueDelay
			}
		}
		if ns := int64(ttft); ns > 0 {
			_ = c.ttftHist.RecordValue(ns)
		}
	}
	if s.StepName != "" {
		if _, ok := c.stepHists[s.StepName]; !ok {
			c.stepHists[s.StepName] = hdrhistogram.New(histMinNs, histMaxNs, histSigFigs)
			c.stepOrder = append(c.stepOrder, s.StepName)
		}
		sum := c.stepSums[s.StepName]
		sum.record(s)
		c.stepSums[s.StepName] = sum
		if ns := int64(s.Latency); ns > 0 {
			_ = c.stepHists[s.StepName].RecordValue(ns)
		}
	}
	if s.Group != "" {
		if _, ok := c.groupHists[s.Group]; !ok {
			c.groupHists[s.Group] = hdrhistogram.New(histMinNs, histMaxNs, histSigFigs)
			c.groupOrder = append(c.groupOrder, s.Group)
		}
		gsum := c.groupSums[s.Group]
		gsum.record(s)
		c.groupSums[s.Group] = gsum
		if ns := int64(s.Latency); ns > 0 {
			_ = c.groupHists[s.Group].RecordValue(ns)
		}
	}
}

// LiveStepMetrics returns a snapshot of per-step metrics in scenario step order.
// Returns nil when no samples with StepName have been recorded.
func (c *Collector) LiveStepMetrics() []StepSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.stepOrder) == 0 {
		return nil
	}
	result := make([]StepSummary, 0, len(c.stepOrder))
	for _, name := range c.stepOrder {
		hist := c.stepHists[name]
		sum := c.stepSums[name]
		ss := StepSummary{
			Name:   name,
			Total:  sum.Total,
			Errors: sum.Errors,
		}
		if hist.TotalCount() > 0 {
			ss.P50 = nsToD(hist.ValueAtQuantile(50))
			ss.P90 = nsToD(hist.ValueAtQuantile(90))
			ss.P95 = nsToD(hist.ValueAtQuantile(95))
			ss.P99 = nsToD(hist.ValueAtQuantile(99))
		}
		result = append(result, ss)
	}
	return result
}

// LiveGroupMetrics returns a snapshot of per-group metrics.
func (c *Collector) LiveGroupMetrics() []GroupSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.groupOrder) == 0 {
		return nil
	}
	result := make([]GroupSummary, 0, len(c.groupOrder))
	for _, name := range c.groupOrder {
		hist := c.groupHists[name]
		sum := c.groupSums[name]
		gs := GroupSummary{Name: name, Total: sum.Total, Errors: sum.Errors}
		if hist.TotalCount() > 0 {
			gs.P50 = nsToD(hist.ValueAtQuantile(50))
			gs.P95 = nsToD(hist.ValueAtQuantile(95))
			gs.P99 = nsToD(hist.ValueAtQuantile(99))
		}
		result = append(result, gs)
	}
	return result
}

func nsToD(ns int64) time.Duration {
	return time.Duration(ns)
}
