package distributed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/reporter"
	"github.com/ramplio/ramplio/internal/scenarios"
)

// Coordinator orchestrates distributed load testing across multiple workers.
type Coordinator struct {
	workers      []string // list of worker addresses
	scenarioYAML []byte
	cfg          *scenarios.Scenario
	httpCfg      protocols.HTTPConfig

	mu          sync.RWMutex
	liveSnapshot reporter.LiveSnapshot
	collector   *metricsCollector
	startedAt   time.Time
}

// NewCoordinator creates a new coordinator with the given worker addresses.
func NewCoordinator(workers []string, scenarioYAML []byte, cfg *scenarios.Scenario, httpCfg protocols.HTTPConfig) *Coordinator {
	return &Coordinator{
		workers:      workers,
		scenarioYAML: scenarioYAML,
		cfg:          cfg,
		httpCfg:      httpCfg,
		collector: &metricsCollector{
			partials: make(map[string]*PartialSummary),
		},
	}
}

// Run orchestrates the distributed test execution.
func (c *Coordinator) Run(ctx context.Context) (metrics.Summary, error) {
	c.startedAt = time.Now()

	// Step 1: Health check all workers
	if err := c.healthCheckWorkers(ctx); err != nil {
		return metrics.Summary{}, fmt.Errorf("health check failed: %w", err)
	}

	// Step 2: Run setup steps locally and capture values
	setupCaptures := make(map[string]string)
	if len(c.cfg.Setup) > 0 {
		captures, err := c.runSetup(ctx)
		if err != nil {
			return metrics.Summary{}, fmt.Errorf("setup failed: %w", err)
		}
		setupCaptures = captures
	}

	// Step 3: Allocate VUs to workers
	allocation := c.allocateVUs()

	// Step 4: Broadcast assign requests to all workers
	if err := c.broadcastAssign(ctx, allocation, setupCaptures); err != nil {
		return metrics.Summary{}, fmt.Errorf("broadcast assign failed: %w", err)
	}

	// Step 5: Poll live metrics and aggregate
	done := make(chan struct{})
	pollErr := make(chan error, 1)
	go func() {
		pollErr <- c.pollWorkers(ctx)
		close(done)
	}()

	// Wait for all workers to finish
	select {
	case err := <-pollErr:
		if err != nil && err != context.Canceled {
			return metrics.Summary{}, fmt.Errorf("polling failed: %w", err)
		}
	case <-ctx.Done():
		// Context cancelled, stop all workers
		_ = c.stopAllWorkers(ctx)
		<-done
	}

	// Step 6: Collect final results from all workers
	partials, err := c.collectResults(ctx)
	if err != nil {
		return metrics.Summary{}, fmt.Errorf("collect results failed: %w", err)
	}

	// Step 7: Merge partial summaries into final summary
	summary := mergePartials(partials)

	// Step 8: Run teardown steps
	if len(c.cfg.Teardown) > 0 {
		if err := c.runTeardown(ctx); err != nil {
			fmt.Fprintf(io.Discard, "warning: teardown failed: %v\n", err)
		}
	}

	return summary, nil
}

// LiveSnapshot returns the current aggregated live metrics.
func (c *Coordinator) LiveSnapshot() reporter.LiveSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.liveSnapshot
}

// healthCheckWorkers performs health check on all workers.
func (c *Coordinator) healthCheckWorkers(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	for _, addr := range c.workers {
		resp, err := http.Get(fmt.Sprintf("http://%s/health", normalizeAddr(addr)))
		if err != nil {
			return fmt.Errorf("worker %s health check failed: %w", addr, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("worker %s returned status %d", addr, resp.StatusCode)
		}
	}
	return nil
}

// allocateVUs distributes assigned VUs evenly among workers using integer-max-remainder method.
func (c *Coordinator) allocateVUs() map[string]int {
	maxTarget := 0
	for _, stage := range c.cfg.Stages {
		if stage.Target > maxTarget {
			maxTarget = stage.Target
		}
	}

	allocation := make(map[string]int)
	numWorkers := len(c.workers)

	if numWorkers == 0 || maxTarget == 0 {
		return allocation
	}

	// Distribute VUs evenly
	baseVUs := maxTarget / numWorkers
	remainder := maxTarget % numWorkers

	for i, worker := range c.workers {
		vu := baseVUs
		if i < remainder {
			vu++
		}
		allocation[worker] = vu
	}

	return allocation
}

// broadcastAssign sends assign requests to all workers.
func (c *Coordinator) broadcastAssign(ctx context.Context, allocation map[string]int, setupCaptures map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, len(c.workers))

	for _, worker := range c.workers {
		assignedVUs := allocation[worker]
		wg.Add(1)
		go func(addr string, vus int) {
			defer wg.Done()
			req := &AssignRequest{
				ScenarioYAML:  c.scenarioYAML,
				AssignedVUs:   vus,
				SetupCaptures: setupCaptures,
			}
			body, _ := json.Marshal(req)
			resp, err := http.Post(
				fmt.Sprintf("http://%s/assign", normalizeAddr(addr)),
				"application/json",
				bytes.NewReader(body),
			)
			if err != nil {
				errCh <- fmt.Errorf("worker %s assign failed: %w", addr, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				errCh <- fmt.Errorf("worker %s returned status %d: %s", addr, resp.StatusCode, string(body))
			}
		}(worker, assignedVUs)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}

	return nil
}

// pollWorkers periodically polls all workers for live metrics until they complete.
func (c *Coordinator) pollWorkers(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	allDone := false
	for !allDone {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var wg sync.WaitGroup
			mu := sync.Mutex{}
			liveMetrics := make(map[string]*LiveMetricsResponse)
			var pollErr error

			for _, worker := range c.workers {
				wg.Add(1)
				go func(addr string) {
					defer wg.Done()
					resp, err := http.Get(fmt.Sprintf("http://%s/live", normalizeAddr(addr)))
					if err != nil {
						mu.Lock()
						if pollErr == nil {
							pollErr = fmt.Errorf("worker %s live poll failed: %w", addr, err)
						}
						mu.Unlock()
						return
					}
					defer resp.Body.Close()

					var metrics LiveMetricsResponse
					if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
						mu.Lock()
						if pollErr == nil {
							pollErr = fmt.Errorf("worker %s decode failed: %w", addr, err)
						}
						mu.Unlock()
						return
					}

					mu.Lock()
					liveMetrics[addr] = &metrics
					mu.Unlock()
				}(worker)
			}

			wg.Wait()

			if pollErr != nil {
				return pollErr
			}

			// Aggregate live metrics and update snapshot
			c.aggregateLiveMetrics(liveMetrics)

			// Check if all workers are done
			allDone = true
			for _, metrics := range liveMetrics {
				if !metrics.Done {
					allDone = false
					break
				}
			}
		}
	}

	return nil
}

// aggregateLiveMetrics aggregates live metrics from all workers into a LiveSnapshot.
func (c *Coordinator) aggregateLiveMetrics(liveMetrics map[string]*LiveMetricsResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var totalRequests int64
	var totalErrors int64
	var maxP99Ns int64
	totalActiveVUs := 0

	for _, lm := range liveMetrics {
		totalRequests += lm.Total
		totalErrors += lm.Errors
		if lm.P99Ns > maxP99Ns {
			maxP99Ns = lm.P99Ns
		}
		totalActiveVUs += lm.ActiveVUs
	}

	elapsed := time.Since(c.startedAt).Seconds()
	rps := 0.0
	if elapsed > 0 {
		rps = float64(totalRequests) / elapsed
	}

	c.liveSnapshot = reporter.LiveSnapshot{
		Total:     totalRequests,
		Errors:    totalErrors,
		RPS:       rps,
		ActiveVUs: totalActiveVUs,
		P99:       time.Duration(maxP99Ns),
	}
}

// stopAllWorkers sends stop requests to all workers.
func (c *Coordinator) stopAllWorkers(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var firstErr error
	mu := sync.Mutex{}

	for _, worker := range c.workers {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			resp, err := http.Post(
				fmt.Sprintf("http://%s/stop", normalizeAddr(addr)),
				"application/json",
				nil,
			)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("worker %s stop failed: %w", addr, err)
				}
				mu.Unlock()
				return
			}
			resp.Body.Close()
		}(worker)
	}

	wg.Wait()
	return firstErr
}

// collectResults fetches final results from all workers.
func (c *Coordinator) collectResults(ctx context.Context) ([]PartialSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	results := make([]PartialSummary, 0, len(c.workers))
	mu := sync.Mutex{}
	var wg sync.WaitGroup
	var firstErr error

	for _, worker := range c.workers {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			resp, err := http.Get(fmt.Sprintf("http://%s/result", normalizeAddr(addr)))
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("worker %s result failed: %w", addr, err)
				}
				mu.Unlock()
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("worker %s returned status %d", addr, resp.StatusCode)
				}
				mu.Unlock()
				return
			}

			var result PartialSummary
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("worker %s decode failed: %w", addr, err)
				}
				mu.Unlock()
				return
			}

			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(worker)
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	return results, nil
}

// runSetup executes setup steps and returns captured values.
func (c *Coordinator) runSetup(ctx context.Context) (map[string]string, error) {
	// For now, return empty map. Full implementation would run setup steps
	// using a local engine similar to the run command.
	return make(map[string]string), nil
}

// runTeardown executes teardown steps.
func (c *Coordinator) runTeardown(ctx context.Context) error {
	// For now, no-op. Full implementation would run teardown steps.
	return nil
}

// mergePartials combines partial results from all workers into a single summary.
func mergePartials(partials []PartialSummary) metrics.Summary {
	var sum metrics.Summary

	if len(partials) == 0 {
		return sum
	}

	// Aggregate basic metrics
	for _, p := range partials {
		sum.Total += p.Total
		sum.Errors += p.Errors
		sum.BytesIn += p.BytesIn
		sum.DroppedSamples += p.DroppedSamples

		// Track min/max latencies
		if p.MinNs > 0 && (sum.MinLatency == 0 || time.Duration(p.MinNs) < sum.MinLatency) {
			sum.MinLatency = time.Duration(p.MinNs)
		}
		if p.MaxNs > sum.MaxLatency.Nanoseconds() {
			sum.MaxLatency = time.Duration(p.MaxNs)
		}

		// Track wall time
		if time.Duration(p.WallNs) > sum.WallTime {
			sum.WallTime = time.Duration(p.WallNs)
		}
	}

	// Calculate weighted percentiles
	if sum.Total > 0 {
		sum.P50 = weightedPercentile(partials, func(p *PartialSummary) int64 { return p.P50Ns })
		sum.P90 = weightedPercentile(partials, func(p *PartialSummary) int64 { return p.P90Ns })
		sum.P95 = weightedPercentile(partials, func(p *PartialSummary) int64 { return p.P95Ns })
		sum.P99 = weightedPercentile(partials, func(p *PartialSummary) int64 { return p.P99Ns })
	}

	// Merge step summaries
	stepMap := make(map[string]*metrics.StepSummary)
	for _, p := range partials {
		for _, step := range p.Steps {
			if stepMap[step.Name] == nil {
				stepMap[step.Name] = &metrics.StepSummary{Name: step.Name}
			}
			merged := stepMap[step.Name]
			merged.Total += step.Total
			merged.Errors += step.Errors
			// For step percentiles, also use weighted average
		}
	}

	// Convert map to slice
	sum.Steps = make([]metrics.StepSummary, 0, len(stepMap))
	for _, step := range stepMap {
		sum.Steps = append(sum.Steps, *step)
	}

	return sum
}

// weightedPercentile calculates a weighted average of percentiles.
func weightedPercentile(partials []PartialSummary, getter func(*PartialSummary) int64) time.Duration {
	if len(partials) == 0 {
		return 0
	}

	var totalWeight int64
	var weightedSum int64

	for i := range partials {
		p := &partials[i]
		val := getter(p)
		totalWeight += p.Total
		weightedSum += val * p.Total
	}

	if totalWeight == 0 {
		return 0
	}

	return time.Duration(weightedSum / totalWeight)
}

// normalizeAddr ensures the address has a port.
func normalizeAddr(addr string) string {
	if !strings.Contains(addr, ":") {
		return addr + ":7700"
	}
	return addr
}

// metricsCollector tracks partial results from workers.
type metricsCollector struct {
	mu       sync.RWMutex
	partials map[string]*PartialSummary
}
