package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/ramplio/ramplio/internal/dashboard"
	"github.com/ramplio/ramplio/internal/engine"
	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/reporter"
	"github.com/ramplio/ramplio/internal/scenarios"
)

func newRunCmd() *cobra.Command {
	var (
		url            string
		method         string
		vus            int
		duration       string
		headers        []string
		body           string
		scenarioFile   string
		outputFile     string
		dashboardOn    bool
		dashboardPort  int
		dnsCache       bool
		prometheusAddr string
		requestTimeout string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a load test",
		Example: `  ramplio run --url https://example.com --vus 10 --duration 30s
  ramplio run --scenario testdata/smoke.yaml --output results.json
  ramplio run --dashboard
  ramplio run --dashboard --url https://example.com --vus 50 --duration 2m`,
		RunE: func(cmd *cobra.Command, args []string) error {
			httpCfg := protocols.DefaultHTTPConfig()
			httpCfg.DNSCache = dnsCache
			if requestTimeout != "" {
				d, err := time.ParseDuration(requestTimeout)
				if err != nil {
					return fmt.Errorf("invalid --timeout %q: %w", requestTimeout, err)
				}
				httpCfg.RequestTimeout = d
			}

			// Dashboard mode: browser handles test setup and control.
			if dashboardOn {
				if scenarioFile != "" {
					return fmt.Errorf("--scenario cannot be used with --dashboard; configure the test from the browser")
				}
				return runDashboard(url, method, vus, duration, dashboardPort, httpCfg)
			}

			// CLI mode: --url or --scenario required.
			if scenarioFile == "" && url == "" {
				return fmt.Errorf("either --url or --scenario is required")
			}
			if scenarioFile != "" && url != "" {
				return fmt.Errorf("--url and --scenario are mutually exclusive")
			}

			var (
				sum        metrics.Summary
				thresholds *scenarios.Thresholds
				err        error
			)

			if scenarioFile != "" {
				sum, thresholds, err = runScenario(scenarioFile, prometheusAddr, httpCfg)
			} else {
				sum, err = runURL(url, method, vus, duration, headers, body, httpCfg)
			}
			if err != nil {
				return err
			}

			reporter.PrintSummary(os.Stdout, sum)

			if outputFile != "" {
				if saveErr := saveJSON(outputFile, sum); saveErr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not save results: %v\n", saveErr)
				} else {
					fmt.Printf("Results saved to %s\n", outputFile)
				}
			}

			if exceeded := checkThresholds(sum, thresholds); exceeded != "" {
				fmt.Fprintf(os.Stderr, "\nThreshold exceeded: %s\n", exceeded)
				os.Exit(1)
			}
			if sum.ErrorRate() > 0 && thresholds == nil {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&url, "url", "u", "", "Target URL")
	cmd.Flags().StringVar(&method, "method", "GET", "HTTP method")
	cmd.Flags().IntVar(&vus, "vus", 1, "Number of virtual users")
	cmd.Flags().StringVarP(&duration, "duration", "d", "30s", "Test duration (e.g. 30s, 1m)")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "HTTP header (repeatable)")
	cmd.Flags().StringVar(&body, "body", "", "Request body")
	cmd.Flags().StringVarP(&scenarioFile, "scenario", "s", "", "Path to scenario YAML file")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Save JSON results to file")
	cmd.Flags().BoolVar(&dashboardOn, "dashboard", false, "Open live web dashboard (PM control panel)")
	cmd.Flags().IntVar(&dashboardPort, "dashboard-port", 9999, "Dashboard port")
	cmd.Flags().BoolVar(&dnsCache, "dns-cache", false, "Cache DNS lookups to reduce latency measurement noise")
	cmd.Flags().StringVar(&prometheusAddr, "prometheus", "", "Expose Prometheus metrics on this address (e.g. :9100)")
	cmd.Flags().StringVar(&requestTimeout, "timeout", "", "Per-request timeout (e.g. 10s, 500ms); overrides scenario default")

	return cmd
}

// runDashboard starts the web control panel and blocks until Ctrl+C.
// If url is non-empty, the test is auto-started immediately.
func runDashboard(url, method string, vus int, duration string, port int, httpCfg protocols.HTTPConfig) error {
	ctrl := newDashController()
	srv := dashboard.New(ctrl, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		<-sigs
		cancel()
	}()

	if err := srv.Start(ctx); err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}

	fmt.Printf("Dashboard → http://%s\n\n", srv.Addr())
	fmt.Println("Open the URL above in your browser. Press Ctrl+C to exit.")

	if url != "" {
		if err := ctrl.Start(dashboard.RunRequest{
			URL:      url,
			Method:   method,
			VUs:      vus,
			Duration: duration,
		}); err != nil {
			return err
		}
		fmt.Printf("\nTest auto-started: %d VUs for %s → %s\n", vus, duration, url)
	}

	<-ctx.Done()
	return nil
}

// rampProvider supplies live metrics snapshots from a running scenario (TUI / Prometheus path).
type rampProvider struct {
	col       *metrics.Collector
	ramp      *engine.RampEngine
	startedAt time.Time
}

func (p *rampProvider) Snapshot() reporter.LiveSnapshot {
	sum := p.col.LiveSummary()
	p50, p90, p95, p99 := p.col.LivePercentiles()
	cur, total, pct := p.ramp.StageInfo()
	return reporter.LiveSnapshot{
		Total:        sum.Total,
		Errors:       sum.Errors,
		RPS:          sum.RPS(),
		MeanLatency:  sum.MeanLatency(),
		P50:          p50,
		P90:          p90,
		P95:          p95,
		P99:          p99,
		ActiveVUs:    p.ramp.ActiveVUs(),
		StageCurrent: cur,
		StageTotal:   total,
		StagePct:     pct,
		Elapsed:      time.Since(p.startedAt),
	}
}

func runScenario(path, promAddr string, httpCfg protocols.HTTPConfig) (metrics.Summary, *scenarios.Thresholds, error) {
	sc, err := scenarios.ParseFile(path)
	if err != nil {
		return metrics.Summary{}, nil, fmt.Errorf("loading scenario: %w", err)
	}

	if h := sc.HTTP; h != nil {
		if h.MaxIdleConns != nil {
			httpCfg.MaxIdleConns = *h.MaxIdleConns
		}
		if h.MaxIdleConnsPerHost != nil {
			httpCfg.MaxIdleConnsPerHost = *h.MaxIdleConnsPerHost
		}
		if h.RequestTimeoutMs != nil {
			httpCfg.RequestTimeout = time.Duration(*h.RequestTimeoutMs) * time.Millisecond
		}
	}

	steps := make([]engine.RampStep, len(sc.Steps))
	for i, s := range sc.Steps {
		steps[i] = engine.RampStep{
			Request: protocols.Request{
				Method:  strings.ToUpper(s.Method),
				URL:     s.URL,
				Headers: s.Headers,
				Body:    []byte(s.Body),
			},
			Assertions: s.Assertions,
		}
	}

	maxVUs := maxTarget(sc.Stages)
	col := metrics.NewCollector(maxVUs)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   sc.Stages,
		Steps:    steps,
		Executor: protocols.NewHTTPExecutor(httpCfg),
	}, col)

	fmt.Printf("Running scenario %q  (%d stages, %d step(s))\n\n", sc.Name, len(sc.Stages), len(sc.Steps))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var sum metrics.Summary

	go func() {
		defer close(done)
		sum = eng.Run(ctx)
	}()

	provider := &rampProvider{col: col, ramp: eng, startedAt: time.Now()}

	if promAddr != "" {
		prom := reporter.NewPrometheusServer(provider, promAddr)
		if err := prom.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: prometheus unavailable: %v\n", err)
		} else {
			fmt.Printf("Prometheus → http://%s/metrics\n\n", promAddr)
		}
	}

	if err := reporter.RunTUI(provider, cancel, done); err != nil {
		<-done
	}
	cancel()
	<-done

	return sum, sc.Thresholds, nil
}

func runURL(url, method string, vus int, duration string, headers []string, body string, httpCfg protocols.HTTPConfig) (metrics.Summary, error) {
	dur, err := time.ParseDuration(duration)
	if err != nil {
		return metrics.Summary{}, fmt.Errorf("invalid duration %q: %w", duration, err)
	}

	req := protocols.Request{
		Method:  strings.ToUpper(method),
		URL:     url,
		Headers: parseHeaders(headers),
	}
	if body != "" {
		req.Body = []byte(body)
	}

	col := metrics.NewCollector(vus)
	eng := engine.New(engine.Config{
		VUs:      vus,
		Duration: dur,
		Request:  req,
		Executor: protocols.NewHTTPExecutor(httpCfg),
	}, col)

	fmt.Printf("Running %d VUs for %s → %s %s\n\n", vus, duration, req.Method, url)
	return eng.Run(context.Background()), nil
}

func saveJSON(path string, sum metrics.Summary) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return reporter.WriteJSON(f, sum)
}

func checkThresholds(sum metrics.Summary, t *scenarios.Thresholds) string {
	if t == nil {
		return ""
	}
	if t.ErrorRatePct != nil && sum.ErrorRate() > *t.ErrorRatePct {
		return fmt.Sprintf("error_rate %.2f%% > %.2f%%", sum.ErrorRate(), *t.ErrorRatePct)
	}
	if t.P99Ms != nil && float64(sum.P99.Milliseconds()) > *t.P99Ms {
		return fmt.Sprintf("p99 %dms > %.0fms", sum.P99.Milliseconds(), *t.P99Ms)
	}
	return ""
}

func parseHeaders(raw []string) map[string]string {
	headers := make(map[string]string, len(raw))
	for _, h := range raw {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return headers
}

func maxTarget(stages []scenarios.Stage) int {
	max := 1
	for _, s := range stages {
		if s.Target > max {
			max = s.Target
		}
	}
	return max
}
