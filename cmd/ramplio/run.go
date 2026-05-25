package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
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
		rps            int
		duration       string
		headers        []string
		body           string
		scenarioFile   string
		outputFile     string
		reportFile     string
		sinkDSNs       []string
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

			if vus != 1 && rps != 0 {
				return fmt.Errorf("--vus and --rps are mutually exclusive")
			}

			// Dashboard mode: browser handles test setup and control.
			if dashboardOn {
				return runDashboard(url, method, vus, rps, duration, scenarioFile, dashboardPort, httpCfg)
			}

			// CLI mode: --url or --scenario required.
			if scenarioFile == "" && url == "" {
				fmt.Fprintln(os.Stderr, `No target specified. Quick start:

  ramplio run --url https://example.com                     test with 1 user for 30 seconds
  ramplio run --url https://example.com --vus 50 -d 1m      50 concurrent users for 1 minute
  ramplio run --scenario my-test.yaml                       run a YAML scenario file
  ramplio run --dashboard                                   open the visual control panel

New to load testing? Run:  ramplio run --dashboard`)
				return fmt.Errorf("--url or --scenario is required")
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
			} else if rps > 0 {
				sum, err = runRPS(url, method, rps, duration, headers, body, httpCfg)
			} else {
				if !cmd.Flags().Changed("vus") {
					fmt.Println("Tip: running with 1 virtual user (default). Use --vus 50 to simulate more traffic.")
				}
				sum, err = runURL(url, method, vus, duration, headers, body, httpCfg)
			}
			if err != nil {
				return err
			}

			reporter.PrintSummary(os.Stdout, sum)
			thresholdMsg := checkThresholds(sum, thresholds)

			scenarioName := strings.TrimSuffix(scenarioFile, filepath.Ext(scenarioFile))
			if scenarioName == "" {
				scenarioName = url
			}
			for _, dsn := range sinkDSNs {
				sink, sinkErr := reporter.ParseSink(dsn)
				if sinkErr != nil {
					fmt.Fprintf(os.Stderr, "warning: sink %q: %v\n", dsn, sinkErr)
					continue
				}
				if writeErr := sink.Write(sum, scenarioName); writeErr != nil {
					fmt.Fprintf(os.Stderr, "warning: sink %q write: %v\n", dsn, writeErr)
				}
				_ = sink.Close()
			}

			if outputFile != "" {
				name := strings.TrimSuffix(scenarioFile, filepath.Ext(scenarioFile))
				if name == "" {
					name = url
				}
				if saveErr := saveResults(outputFile, sum, name, thresholdMsg); saveErr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not save results: %v\n", saveErr)
				} else {
					fmt.Printf("Results saved to %s\n", outputFile)
				}
			}

			if reportFile != "" {
				if f, createErr := os.Create(reportFile); createErr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not create report file: %v\n", createErr)
				} else {
					writeErr := reporter.WriteHTML(f, sum)
					f.Close()
					if writeErr != nil {
						fmt.Fprintf(os.Stderr, "warning: could not write report: %v\n", writeErr)
					} else {
						fmt.Printf("Report saved to %s\n", reportFile)
					}
				}
			}

			if thresholdMsg != "" {
				fmt.Fprintf(os.Stderr, "\n✗ Threshold exceeded: %s\n", thresholdMsg)
				fmt.Fprintln(os.Stderr, "\n  Common causes: server overloaded, API rate limit, auth expired, slow database")
				fmt.Fprintln(os.Stderr, "  Try:  reduce --vus  ·  check server logs  ·  run with --dashboard for live view")
				os.Exit(1)
			}
			if sum.ErrorRate() > 0 && thresholds == nil {
				fmt.Fprintf(os.Stderr, "\nWarning: %.1f%% error rate detected (%d errors).\n", sum.ErrorRate(), sum.Errors)
				fmt.Fprintln(os.Stderr, "  Add thresholds to a scenario YAML for pass/fail control: ramplio run --scenario my.yaml")
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&url, "url", "u", "", "Target URL")
	cmd.Flags().StringVar(&method, "method", "GET", "HTTP method")
	cmd.Flags().IntVar(&vus, "vus", 1, "Number of virtual users (mutually exclusive with --rps)")
	cmd.Flags().IntVar(&rps, "rps", 0, "Target requests per second — rate mode (mutually exclusive with --vus)")
	cmd.Flags().StringVarP(&duration, "duration", "d", "30s", "Test duration (e.g. 30s, 1m)")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "HTTP header (repeatable)")
	cmd.Flags().StringVar(&body, "body", "", "Request body")
	cmd.Flags().StringVarP(&scenarioFile, "scenario", "s", "", "Path to scenario YAML file")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Save JSON results to file")
	cmd.Flags().StringVarP(&reportFile, "report", "r", "", "Save HTML report to file (e.g. report.html)")
	cmd.Flags().BoolVar(&dashboardOn, "dashboard", false, "Open live web dashboard (PM control panel)")
	cmd.Flags().IntVar(&dashboardPort, "dashboard-port", 9999, "Dashboard port")
	cmd.Flags().BoolVar(&dnsCache, "dns-cache", false, "Cache DNS lookups to reduce latency measurement noise")
	cmd.Flags().StringVar(&prometheusAddr, "prometheus", "", "Expose Prometheus metrics on this address (e.g. :9100)")
	cmd.Flags().StringVar(&requestTimeout, "timeout", "", "Per-request timeout (e.g. 10s, 500ms); overrides scenario default")
	cmd.Flags().StringArrayVar(&sinkDSNs, "sink", nil, "Push results to an external sink (repeatable): csv:<file>  influxdb://host/bucket?token=T")

	return cmd
}

// runDashboard starts the web control panel and blocks until Ctrl+C.
// If scenarioFile is set, the scenario is loaded and displayed in the browser (user clicks Run).
// If url is set (and no scenario), the test auto-starts immediately.
func runDashboard(url, method string, vus, rps int, duration, scenarioFile string, port int, httpCfg protocols.HTTPConfig) error {
	ctrl := newDashController(httpCfg)
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

	dashURL := "http://" + srv.Addr()
	fmt.Printf("Dashboard → %s\n\n", dashURL)
	fmt.Println("Press Ctrl+C to exit.")
	openBrowser(dashURL)

	switch {
	case scenarioFile != "":
		yamlBytes, err := os.ReadFile(scenarioFile)
		if err != nil {
			return fmt.Errorf("reading scenario: %w", err)
		}
		if err := ctrl.LoadScenario(yamlBytes); err != nil {
			return err
		}
		meta := ctrl.ScenarioInfo()
		fmt.Printf("Scenario loaded: %q — %d step(s), %d stage(s), %d max VUs\n",
			meta.Name, len(meta.StepNames), meta.StageCount, meta.MaxVUs)
		fmt.Println("Open the dashboard and click Run to start.")

	case url != "":
		req := dashboard.RunRequest{
			URL:      url,
			Method:   method,
			VUs:      vus,
			RPS:      rps,
			Duration: duration,
		}
		if err := ctrl.Start(req); err != nil {
			return err
		}
		if rps > 0 {
			fmt.Printf("\nTest auto-started: %d req/s for %s → %s\n", rps, duration, url)
		} else {
			fmt.Printf("\nTest auto-started: %d VUs for %s → %s\n", vus, duration, url)
		}
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
		StepMetrics:  p.col.LiveStepMetrics(),
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

	steps := scenarioStepsToRamp(sc.Steps)
	setupSteps := scenarioStepsToRamp(sc.Setup)
	teardownSteps := scenarioStepsToRamp(sc.Teardown)

	var dataRows []map[string]string
	var dataMode string
	if sc.VarsFrom != nil && sc.VarsFrom.File != "" {
		dataFile := sc.VarsFrom.File
		if !filepath.IsAbs(dataFile) {
			dataFile = filepath.Join(filepath.Dir(path), dataFile)
		}
		rows, err := scenarios.LoadDataFile(dataFile)
		if err != nil {
			return metrics.Summary{}, nil, fmt.Errorf("loading data file: %w", err)
		}
		dataRows = rows
		dataMode = sc.VarsFrom.Mode
		fmt.Printf("Data file: %s (%d rows, mode=%s)\n", sc.VarsFrom.File, len(rows), dataMode)
	}

	maxVUs := maxTarget(sc.Stages)
	col := metrics.NewCollector(maxVUs)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:         sc.Stages,
		Steps:          steps,
		SetupSteps:     setupSteps,
		TeardownSteps:  teardownSteps,
		Vars:           sc.Vars,
		DataRows:       dataRows,
		DataMode:       dataMode,
		Executor:       protocols.NewHTTPExecutor(httpCfg),
		WSExecutor:     protocols.NewWSExecutor(),
		CircuitBreaker: sc.CircuitBreaker,
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

// runRPS drives a rate-mode load test (fixed RPS via token bucket).
// Stages: ramp-up 25% → hold 50% → ramp-down 25%.
func runRPS(url, method string, targetRPS int, duration string, headers []string, body string, httpCfg protocols.HTTPConfig) (metrics.Summary, error) {
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

	rampDur := dur / 4
	if rampDur < time.Second {
		rampDur = time.Second
	}
	holdDur := dur - 2*rampDur

	stgs := []scenarios.Stage{
		{Duration: rampDur, TargetRPS: targetRPS},
		{Duration: holdDur, TargetRPS: targetRPS},
		{Duration: rampDur, TargetRPS: 0},
	}
	steps := []engine.RampStep{{Request: req}}

	workerCount := targetRPS * 5
	if workerCount < 10 {
		workerCount = 10
	}
	if workerCount > 5000 {
		workerCount = 5000
	}
	col := metrics.NewCollector(workerCount)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   stgs,
		Steps:    steps,
		Executor: protocols.NewHTTPExecutor(httpCfg),
	}, col)

	fmt.Printf("Running rate mode: %d req/s for %s → %s %s\n\n", targetRPS, duration, req.Method, url)
	return eng.Run(context.Background()), nil
}

// saveResults writes results to path. Uses JUnit XML format for .xml files,
// JSON for all other extensions.
func saveResults(path string, sum metrics.Summary, scenarioName, thresholdMsg string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if filepath.Ext(path) == ".xml" {
		return reporter.WriteJUnit(f, sum, scenarioName, thresholdMsg)
	}
	return reporter.WriteJSON(f, sum)
}

func checkThresholds(sum metrics.Summary, t *scenarios.Thresholds) string {
	if t == nil {
		return ""
	}
	if t.ErrorRatePct != nil && sum.ErrorRate() > *t.ErrorRatePct {
		return fmt.Sprintf("error_rate %.2f%% > %.2f%%", sum.ErrorRate(), *t.ErrorRatePct)
	}
	if t.P50Ms != nil && float64(sum.P50.Milliseconds()) > *t.P50Ms {
		return fmt.Sprintf("p50 %dms > %.0fms", sum.P50.Milliseconds(), *t.P50Ms)
	}
	if t.P90Ms != nil && float64(sum.P90.Milliseconds()) > *t.P90Ms {
		return fmt.Sprintf("p90 %dms > %.0fms", sum.P90.Milliseconds(), *t.P90Ms)
	}
	if t.P95Ms != nil && float64(sum.P95.Milliseconds()) > *t.P95Ms {
		return fmt.Sprintf("p95 %dms > %.0fms", sum.P95.Milliseconds(), *t.P95Ms)
	}
	if t.P99Ms != nil && float64(sum.P99.Milliseconds()) > *t.P99Ms {
		return fmt.Sprintf("p99 %dms > %.0fms", sum.P99.Milliseconds(), *t.P99Ms)
	}
	if t.MaxMs != nil && float64(sum.MaxLatency.Milliseconds()) > *t.MaxMs {
		return fmt.Sprintf("max %dms > %.0fms", sum.MaxLatency.Milliseconds(), *t.MaxMs)
	}
	if t.ThroughputRps != nil && sum.RPS() < *t.ThroughputRps {
		return fmt.Sprintf("throughput %.2f rps < %.2f rps", sum.RPS(), *t.ThroughputRps)
	}
	for stepName, st := range t.Steps {
		for _, ss := range sum.Steps {
			if ss.Name != stepName {
				continue
			}
			if st.P95Ms != nil && float64(ss.P95.Milliseconds()) > *st.P95Ms {
				return fmt.Sprintf("step %q p95 %dms > %.0fms", stepName, ss.P95.Milliseconds(), *st.P95Ms)
			}
			if st.P99Ms != nil && float64(ss.P99.Milliseconds()) > *st.P99Ms {
				return fmt.Sprintf("step %q p99 %dms > %.0fms", stepName, ss.P99.Milliseconds(), *st.P99Ms)
			}
			if st.ErrorRatePct != nil && ss.Total > 0 {
				errRate := float64(ss.Errors) / float64(ss.Total) * 100
				if errRate > *st.ErrorRatePct {
					return fmt.Sprintf("step %q error_rate %.2f%% > %.2f%%", stepName, errRate, *st.ErrorRatePct)
				}
			}
		}
	}
	return ""
}

// scenarioStepsToRamp converts a slice of scenario steps to engine.RampStep values.
func scenarioStepsToRamp(steps []scenarios.Step) []engine.RampStep {
	out := make([]engine.RampStep, len(steps))
	for i, s := range steps {
		name := s.Name
		if name == "" {
			name = strings.ToUpper(s.Method) + " " + s.URL
		}
		hdrs := s.Headers
		if hdrs == nil {
			hdrs = make(map[string]string)
		}
		// Inject WS expect as a synthetic header so WSExecutor can check it.
		if strings.EqualFold(s.Protocol, "websocket") && s.WSExpect != "" {
			hdrs["X-WS-Expect"] = s.WSExpect
		}
		body := []byte(s.Body)
		if strings.EqualFold(s.Protocol, "websocket") && s.WSMessage != "" && s.Body == "" {
			body = []byte(s.WSMessage)
		}
		out[i] = engine.RampStep{
			Name: name,
			Request: protocols.Request{
				Method:  strings.ToUpper(s.Method),
				URL:     s.URL,
				Headers: hdrs,
				Body:    body,
			},
			Assertions: s.Assertions,
			Auth:       s.Auth,
			Capture:    s.Capture,
			Retry:      s.Retry,
			Pause:      s.Pause,
			Group:      s.Group,
			Protocol:   s.Protocol,
			If:         s.If,
			Loop:       s.Loop,
		}
	}
	return out
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

// openBrowser opens url in the default system browser. Failures are silently
// ignored — the URL is already printed so the user can open it manually.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
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
