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

	"github.com/machiko/ramplio/internal/engine"
	"github.com/machiko/ramplio/internal/metrics"
	"github.com/machiko/ramplio/internal/protocols"
	"github.com/machiko/ramplio/internal/reporter"
	"github.com/machiko/ramplio/internal/scenarios"
)

// Coordinator orchestrates distributed load testing across multiple workers.
type Coordinator struct {
	workers      []string // list of worker addresses
	scenarioYAML []byte
	cfg          *scenarios.Scenario
	httpCfg      protocols.HTTPConfig
	secret       string // shared secret sent to workers; empty disables auth

	client        *http.Client  // HTTP client for worker requests (TLS-configurable)
	pollInterval  time.Duration // live-metrics polling interval
	assignTimeout time.Duration // timeout for /assign broadcast

	mu           sync.RWMutex
	liveSnapshot reporter.LiveSnapshot
	startedAt    time.Time
}

// NewCoordinator creates a new coordinator with the given worker addresses.
func NewCoordinator(workers []string, scenarioYAML []byte, cfg *scenarios.Scenario, httpCfg protocols.HTTPConfig) *Coordinator {
	return &Coordinator{
		workers:       workers,
		scenarioYAML:  scenarioYAML,
		cfg:           cfg,
		httpCfg:       httpCfg,
		client:        &http.Client{},
		pollInterval:  time.Second,
		assignTimeout: 10 * time.Second,
	}
}

// SetSecret configures the shared secret sent to workers as a bearer token.
func (c *Coordinator) SetSecret(secret string) {
	c.secret = secret
}

// SetHTTPClient injects a custom HTTP client, e.g. one with a TLS config that
// trusts a private CA or skips verification for self-signed worker certs.
func (c *Coordinator) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.client = client
	}
}

// SetTiming overrides the polling interval and assign timeout. Non-positive
// values are ignored, leaving the existing default in place.
func (c *Coordinator) SetTiming(pollInterval, assignTimeout time.Duration) {
	if pollInterval > 0 {
		c.pollInterval = pollInterval
	}
	if assignTimeout > 0 {
		c.assignTimeout = assignTimeout
	}
}

// httpClient returns the configured client, falling back to the default client
// when the coordinator was built via a struct literal (e.g. in tests).
func (c *Coordinator) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return http.DefaultClient
}

// Run orchestrates the distributed test execution.
func (c *Coordinator) Run(ctx context.Context) (metrics.Summary, error) {
	c.startedAt = time.Now()

	// Step 1: Health check all workers
	if err := c.healthCheckWorkers(ctx); err != nil {
		return metrics.Summary{}, fmt.Errorf("health check failed: %w", err)
	}

	// Step 2: Run setup steps centrally and capture values
	setupCaptures, err := c.runSetup(ctx)
	if err != nil {
		return metrics.Summary{}, fmt.Errorf("setup failed: %w", err)
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

	// Step 6: Collect final histogram snapshots from all workers
	exports, err := c.collectResults(ctx)
	if err != nil {
		return metrics.Summary{}, fmt.Errorf("collect results failed: %w", err)
	}

	// Step 7: Merge snapshots into a statistically correct summary
	summary := metrics.MergeExports(exports)

	// Step 8: Run teardown steps centrally, seeded with setup captures
	if err := c.runTeardown(ctx, setupCaptures); err != nil {
		fmt.Fprintf(io.Discard, "warning: teardown failed: %v\n", err)
	}

	return summary, nil
}

// LiveSnapshot returns the current aggregated live metrics.
func (c *Coordinator) LiveSnapshot() reporter.LiveSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.liveSnapshot
}

// newRequest builds an HTTP request to a worker endpoint with the shared
// secret attached as a bearer token when configured. The worker address may
// carry an explicit http:// or https:// scheme; bare addresses default to http.
func (c *Coordinator) newRequest(ctx context.Context, method, addr, path string, body io.Reader) (*http.Request, error) {
	scheme, hostport := splitScheme(addr)
	url := fmt.Sprintf("%s://%s%s", scheme, normalizeAddr(hostport), path)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// healthCheckWorkers performs health check on all workers.
func (c *Coordinator) healthCheckWorkers(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	for _, addr := range c.workers {
		req, err := c.newRequest(ctx, http.MethodGet, addr, "/health", nil)
		if err != nil {
			return fmt.Errorf("worker %s request build failed: %w", addr, err)
		}
		resp, err := c.httpClient().Do(req)
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
	ctx, cancel := context.WithTimeout(ctx, c.assignTimeout)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, len(c.workers))

	for _, worker := range c.workers {
		assignedVUs := allocation[worker]
		wg.Add(1)
		go func(addr string, vus int) {
			defer wg.Done()
			payload := &AssignRequest{
				ScenarioYAML:  c.scenarioYAML,
				AssignedVUs:   vus,
				SetupCaptures: setupCaptures,
			}
			body, _ := json.Marshal(payload)
			req, err := c.newRequest(ctx, http.MethodPost, addr, "/assign", bytes.NewReader(body))
			if err != nil {
				errCh <- fmt.Errorf("worker %s request build failed: %w", addr, err)
				return
			}
			resp, err := c.httpClient().Do(req)
			if err != nil {
				errCh <- fmt.Errorf("worker %s assign failed: %w", addr, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				msg, _ := io.ReadAll(resp.Body)
				errCh <- fmt.Errorf("worker %s returned status %d: %s", addr, resp.StatusCode, string(msg))
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
	ticker := time.NewTicker(c.pollInterval)
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
					req, err := c.newRequest(ctx, http.MethodGet, addr, "/live", nil)
					if err != nil {
						mu.Lock()
						if pollErr == nil {
							pollErr = fmt.Errorf("worker %s request build failed: %w", addr, err)
						}
						mu.Unlock()
						return
					}
					resp, err := c.httpClient().Do(req)
					if err != nil {
						mu.Lock()
						if pollErr == nil {
							pollErr = fmt.Errorf("worker %s live poll failed: %w", addr, err)
						}
						mu.Unlock()
						return
					}
					defer resp.Body.Close()

					var lm LiveMetricsResponse
					if err := json.NewDecoder(resp.Body).Decode(&lm); err != nil {
						mu.Lock()
						if pollErr == nil {
							pollErr = fmt.Errorf("worker %s decode failed: %w", addr, err)
						}
						mu.Unlock()
						return
					}

					mu.Lock()
					liveMetrics[addr] = &lm
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
			for _, lm := range liveMetrics {
				if !lm.Done {
					allDone = false
					break
				}
			}
		}
	}

	return nil
}

// aggregateLiveMetrics aggregates live metrics from all workers into a LiveSnapshot.
// Note: P99 here is the max across workers — an approximation acceptable for the
// live view only. The final report uses MergeExports for an exact percentile.
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
			req, err := c.newRequest(ctx, http.MethodPost, addr, "/stop", nil)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("worker %s request build failed: %w", addr, err)
				}
				mu.Unlock()
				return
			}
			resp, err := c.httpClient().Do(req)
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

// collectResults fetches final histogram snapshots from all workers.
func (c *Coordinator) collectResults(ctx context.Context) ([]metrics.HistogramExport, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	results := make([]metrics.HistogramExport, 0, len(c.workers))
	mu := sync.Mutex{}
	var wg sync.WaitGroup
	var firstErr error

	for _, worker := range c.workers {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			req, err := c.newRequest(ctx, http.MethodGet, addr, "/result", nil)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("worker %s request build failed: %w", addr, err)
				}
				mu.Unlock()
				return
			}
			resp, err := c.httpClient().Do(req)
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

			var result ResultResponse
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("worker %s decode failed: %w", addr, err)
				}
				mu.Unlock()
				return
			}

			mu.Lock()
			results = append(results, result.Export)
			mu.Unlock()
		}(worker)
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	return results, nil
}

// runSetup executes the scenario's setup steps once on the coordinator and
// returns the captured values, which are broadcast to every worker so all VUs
// share the same auth tokens.
func (c *Coordinator) runSetup(ctx context.Context) (map[string]string, error) {
	if len(c.cfg.Setup) == 0 {
		return map[string]string{}, nil
	}
	col := metrics.NewCollector(1)
	defer col.Stop()
	eng := engine.NewRamp(engine.RampConfig{
		SetupSteps: scenarioStepsToEngineSteps(c.cfg.Setup),
		Vars:       c.cfg.Vars,
		Executor:   protocols.NewHTTPExecutor(c.httpCfg),
		WSExecutor: protocols.NewWSExecutor(),
	}, col)
	return eng.RunSetup(ctx), nil
}

// runTeardown executes the scenario's teardown steps once on the coordinator,
// seeded with the setup captures (so a logout step can use the auth token).
func (c *Coordinator) runTeardown(ctx context.Context, setupCaptures map[string]string) error {
	if len(c.cfg.Teardown) == 0 {
		return nil
	}
	col := metrics.NewCollector(1)
	defer col.Stop()
	eng := engine.NewRamp(engine.RampConfig{
		TeardownSteps: scenarioStepsToEngineSteps(c.cfg.Teardown),
		Vars:          c.cfg.Vars,
		Executor:      protocols.NewHTTPExecutor(c.httpCfg),
		WSExecutor:    protocols.NewWSExecutor(),
	}, col)
	eng.RunTeardown(ctx, setupCaptures)
	return nil
}

// splitScheme separates an optional http:// or https:// prefix from a worker
// address. Bare addresses default to http for backward compatibility.
func splitScheme(addr string) (scheme, hostport string) {
	switch {
	case strings.HasPrefix(addr, "https://"):
		return "https", strings.TrimPrefix(addr, "https://")
	case strings.HasPrefix(addr, "http://"):
		return "http", strings.TrimPrefix(addr, "http://")
	default:
		return "http", addr
	}
}

// normalizeAddr ensures the address has a port.
func normalizeAddr(addr string) string {
	if !strings.Contains(addr, ":") {
		return addr + ":7700"
	}
	return addr
}
