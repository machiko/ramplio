package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/distributed"
	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/observe"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/spf13/cobra"
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
		baselineFile   string
		reportFile     string
		sinkDSNs       []string
		dashboardOn    bool
		dashboardPort  int
		dashboardToken string
		dnsCache       bool
		traceContext   bool
		observeDSN     string
		strictTrust    bool
		prometheusAddr string
		requestTimeout string
		ignoreErrors   bool
		workers        []string
		workerSecret   string
		tlsCA          string
		tlsSkipVerify  bool
		pollInterval   string
		assignTimeout  string
		noTUI          bool
		noPreflight    bool
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
			httpCfg.TraceContext = traceContext
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

			// Distributed mode: --worker flags specify remote worker addresses.
			if len(workers) > 0 {
				if scenarioFile == "" {
					return fmt.Errorf("--scenario is required for distributed testing")
				}
				if url != "" {
					return fmt.Errorf("--url and --worker are mutually exclusive")
				}
				if workerSecret == "" {
					workerSecret = os.Getenv("RAMPLIO_WORKER_SECRET")
				}
				dopts, derr := buildDistOptions(workerSecret, tlsCA, tlsSkipVerify, pollInterval, assignTimeout)
				if derr != nil {
					return derr
				}
				dopts.noTUI = noTUI
				return runDistributed(scenarioFile, workers, prometheusAddr, httpCfg, dopts)
			}

			// Dashboard mode: browser handles test setup and control.
			if dashboardOn {
				return runDashboard(url, method, vus, rps, duration, scenarioFile, dashboardPort, dashboardToken, httpCfg, observeDSN)
			}

			// CLI mode: --url or --scenario required.
			if scenarioFile == "" && url == "" {
				printNextSteps(os.Stderr, "\n還沒指定測試目標。可以這樣開始：",
					nextStep{"測 1 個用戶 30 秒", "ramplio run --url https://example.com"},
					nextStep{"50 個同時用戶測 1 分鐘", "ramplio run --url https://example.com --vus 50 -d 1m"},
					nextStep{"執行 YAML 情境檔", "ramplio run --scenario my-test.yaml"},
					nextStep{"開視覺控制面板（最推薦）", "ramplio run --dashboard"},
				)
				fmt.Fprintln(os.Stderr, "\n  第一次用壓力測試？建議先跑：ramplio run --dashboard")
				return fmt.Errorf("--url or --scenario is required")
			}
			if scenarioFile != "" && url != "" {
				return fmt.Errorf("--url and --scenario are mutually exclusive")
			}

			// Pre-flight: one quick probe so a typo'd URL or down target fails
			// fast with a plain-language reason instead of after a full run.
			if !noPreflight {
				if pURL, pMethod, ok := preflightTarget(scenarioFile, url, method); ok {
					if pferr := runPreflight(cmd.Context(), os.Stderr, httpCfg, pURL, pMethod); pferr != nil {
						return pferr
					}
				}
			}

			// --observe 設定錯誤在開跑前攔截(fail fast),不浪費一輪壓測。
			observeSrc, obsCfgErr := validateObserveConfig(observeDSN, rps)
			if obsCfgErr != nil {
				return obsCfgErr
			}

			var (
				sum        metrics.Summary
				thresholds *scenarios.Thresholds
				err        error
			)

			runStart := time.Now()
			switch {
			case scenarioFile != "":
				sum, thresholds, err = runScenario(scenarioFile, prometheusAddr, httpCfg)
			case rps > 0:
				sum, err = runRPS(url, method, rps, duration, headers, body, httpCfg)
			default:
				if !cmd.Flags().Changed("vus") {
					fmt.Println("提示：目前用 1 個虛擬用戶（預設）。加上 --vus 50 可模擬更多同時流量。")
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
				var writeErr error
				if detailedSink, ok := sink.(reporter.DetailedSink); ok {
					writeErr = detailedSink.WriteDetailed(sum, scenarioName)
				} else {
					writeErr = sink.Write(sum, scenarioName)
				}
				if writeErr != nil {
					fmt.Fprintf(os.Stderr, "warning: sink %q write: %v\n", dsn, writeErr)
				}
				// CsvSink 的 close 是寫入側(檔案可能不完整),錯誤必須可見
				if cerr := sink.Close(); cerr != nil {
					fmt.Fprintf(os.Stderr, "warning: sink %q close: %v\n", dsn, cerr)
				}
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

			if baselineFile != "" {
				b := baseline.FromSummary(sum, scenarioName)
				b.GitCommit = currentGitCommit()
				writeBaselineFile(os.Stdout, os.Stderr, baselineFile, b)
			}

			// 觀測失敗預設只警告不中斷:trace 關聯是補充,不可污染 exit code。
			observeTrusted := false
			if observeSrc != nil {
				if runDur, durErr := time.ParseDuration(duration); durErr == nil {
					rampDur, holdDur := rateProfile(runDur)
					observeTrusted = runObservation(os.Stdout, os.Stderr, observeSrc, runStart, rampDur, holdDur)
				} else {
					fmt.Fprintf(os.Stderr, "warning: duration 解析失敗,略過觀測: %v\n", durErr)
				}
			}

			if reportFile != "" {
				if f, createErr := os.Create(reportFile); createErr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not create report file: %v\n", createErr)
				} else {
					writeErr := reporter.WriteHTML(f, sum)
					// 寫入側 close 錯誤 = 報告可能不完整,併入警告
					if cerr := f.Close(); writeErr == nil {
						writeErr = cerr
					}
					if writeErr != nil {
						fmt.Fprintf(os.Stderr, "warning: could not write report: %v\n", writeErr)
					} else {
						fmt.Printf("Report saved to %s\n", reportFile)
					}
				}
			}

			// strict-trust 守門刻意放在所有輸出產物(sink/output/baseline/report)
			// 之後:CI 失敗時使用者仍拿得到完整產物去診斷,不可信 ≠ 不可看。
			if gateErr := strictTrustGateErr(strictTrust, observeSrc != nil, observeTrusted); gateErr != nil {
				return gateErr
			}

			if thresholdMsg != "" {
				fmt.Fprintf(os.Stderr, "\n✗ 未達標準：%s\n", thresholdMsg)
				fmt.Fprintln(os.Stderr, "\n  常見原因：伺服器過載、API 流量限制、認證過期、資料庫太慢")
				fmt.Fprintln(os.Stderr, "  可以試：降低 --vus　·　檢查伺服器日誌　·　加 --dashboard 即時觀察")
				if !ignoreErrors {
					os.Exit(1)
				}
			}
			if sum.ErrorRate() > 0 && thresholds == nil {
				fmt.Fprintf(os.Stderr, "\n警告：偵測到 %.1f%% 的錯誤率（共 %d 個錯誤）。\n", sum.ErrorRate(), sum.Errors)
				fmt.Fprintln(os.Stderr, "  想要自動判定通過/失敗，可在情境 YAML 設定 thresholds：ramplio run --scenario my.yaml")
				if !ignoreErrors {
					os.Exit(1)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&url, "url", "u", "", "Target URL")
	cmd.Flags().StringVar(&method, "method", "GET", "HTTP method")
	cmd.Flags().IntVar(&vus, "vus", 1, "Number of virtual users (mutually exclusive with --rps)")
	cmd.Flags().IntVar(&rps, "rps", 0, "Target requests per second — rate mode (mutually exclusive with --vus)")
	cmd.Flags().StringVar(&baselineFile, "save-baseline", "", "把本次結果存成 baseline 快照(供 ramplio compare 守門)")
	cmd.Flags().StringVarP(&duration, "duration", "d", "30s", "Test duration (e.g. 30s, 1m)")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "HTTP header (repeatable)")
	cmd.Flags().StringVar(&body, "body", "", "Request body")
	cmd.Flags().StringVarP(&scenarioFile, "scenario", "s", "", "Path to scenario YAML file")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Save JSON results to file")
	cmd.Flags().StringVarP(&reportFile, "report", "r", "", "Save HTML report to file (e.g. report.html)")
	cmd.Flags().BoolVar(&dashboardOn, "dashboard", false, "Open live web dashboard (PM control panel)")
	cmd.Flags().IntVar(&dashboardPort, "dashboard-port", 9999, "Dashboard port")
	cmd.Flags().StringVar(&dashboardToken, "dashboard-token", "", "Bearer token to protect dashboard control endpoints (optional)")
	cmd.Flags().BoolVar(&dnsCache, "dns-cache", false, "Cache DNS lookups to reduce latency measurement noise")
	cmd.Flags().BoolVar(&traceContext, "trace-context", false, "對每個請求注入 W3C traceparent 供 APM 關聯壓測流量(每請求約 63ns 開銷,預設關閉)")
	cmd.Flags().StringVar(&observeDSN, "observe", "", "壓測後拉取目標系統 trace 做瓶頸關聯(例:jaeger://localhost:16686?service=checkout;僅 rate 模式)")
	cmd.Flags().BoolVar(&strictTrust, "strict-trust", false, "觀測結果不可信(拉取失敗/截斷/關聯不足)時視同失敗(CI 場景;需搭配 --observe)")
	cmd.Flags().StringVar(&prometheusAddr, "prometheus", "", "Expose Prometheus metrics on this address (e.g. :9100)")
	cmd.Flags().StringVar(&requestTimeout, "timeout", "", "Per-request timeout (e.g. 10s, 500ms); overrides scenario default")
	cmd.Flags().StringArrayVar(&sinkDSNs, "sink", nil, "Push results to an external sink (repeatable): csv:<file>  influxdb://host/bucket?token=T")
	cmd.Flags().BoolVar(&ignoreErrors, "ignore-errors", false, "Exit 0 even when errors or threshold violations occur (useful during debugging)")
	cmd.Flags().StringArrayVar(&workers, "worker", nil, "Worker address for distributed testing (repeatable, e.g. --worker localhost:7700 or --worker https://w1:7700)")
	cmd.Flags().StringVar(&workerSecret, "worker-secret", "", "Shared secret for authenticating with workers (or set RAMPLIO_WORKER_SECRET)")
	cmd.Flags().StringVar(&tlsCA, "tls-ca", "", "CA certificate file to verify https:// worker certs")
	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false, "Skip TLS verification for https:// workers (self-signed certs)")
	cmd.Flags().StringVar(&pollInterval, "poll-interval", "", "Live-metrics polling interval for distributed runs (e.g. 500ms, 2s; default 1s)")
	cmd.Flags().StringVar(&assignTimeout, "assign-timeout", "", "Timeout for broadcasting work to workers (e.g. 10s, 30s; default 10s)")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Disable the live TUI and print plain progress lines (auto-enabled when output is not a terminal; for distributed runs)")
	cmd.Flags().BoolVar(&noPreflight, "no-preflight", false, "Skip the quick reachability check before the test (run even if the target looks unreachable)")

	return cmd
}

// runDashboard starts the web control panel and blocks until Ctrl+C.
// If scenarioFile is set, the scenario is loaded and displayed in the browser (user clicks Run).
// If url is set (and no scenario), the test auto-starts immediately.
// observeDSN 在啟動時 fail-fast 解析;rate/VU 的取捨延到每次 run 才知道
// (由瀏覽器決定),VU 模式該次不觀測,不在此攔截。
func runDashboard(url, method string, vus, rps int, duration, scenarioFile string, port int, token string, httpCfg protocols.HTTPConfig, observeDSN string) error {
	var observeSrc observe.TraceSource
	if observeDSN != "" {
		src, err := parseObserveDSN(observeDSN)
		if err != nil {
			return err
		}
		observeSrc = src
	}
	ctrl := newDashController(httpCfg, observeSrc)
	srv := dashboard.New(ctrl, port, token)

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
	fmt.Printf("Dashboard → %s\n", dashURL)
	if token != "" {
		fmt.Printf("Token     → %s\n", token)
	}
	fmt.Println("\nPress Ctrl+C to exit.")
	openBrowser(dashURL)

	switch {
	case scenarioFile != "":
		yamlBytes, err := os.ReadFile(scenarioFile)
		if err != nil {
			return fmt.Errorf("reading scenario: %w", err)
		}
		if err := ctrl.LoadScenario(yamlBytes, filepath.Dir(scenarioFile)); err != nil {
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
		if promErr := prom.Start(ctx); promErr != nil {
			fmt.Fprintf(os.Stderr, "warning: prometheus unavailable: %v\n", promErr)
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

// distOptions carries the authentication, TLS and timing knobs for a
// distributed run, assembled from CLI flags.
type distOptions struct {
	secret        string
	client        *http.Client
	pollInterval  time.Duration
	assignTimeout time.Duration
	noTUI         bool
}

// buildDistOptions validates and assembles distributed-run options from flags.
func buildDistOptions(secret, tlsCA string, tlsSkipVerify bool, pollInterval, assignTimeout string) (distOptions, error) {
	opts := distOptions{secret: secret}

	if tlsCA != "" || tlsSkipVerify {
		tlsCfg := &tls.Config{InsecureSkipVerify: tlsSkipVerify} //nolint:gosec // opt-in for self-signed workers
		if tlsCA != "" {
			pem, err := os.ReadFile(tlsCA)
			if err != nil {
				return opts, fmt.Errorf("reading --tls-ca: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return opts, fmt.Errorf("--tls-ca %q contains no valid certificates", tlsCA)
			}
			tlsCfg.RootCAs = pool
		}
		opts.client = &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	}

	if pollInterval != "" {
		d, err := time.ParseDuration(pollInterval)
		if err != nil {
			return opts, fmt.Errorf("invalid --poll-interval %q: %w", pollInterval, err)
		}
		opts.pollInterval = d
	}
	if assignTimeout != "" {
		d, err := time.ParseDuration(assignTimeout)
		if err != nil {
			return opts, fmt.Errorf("invalid --assign-timeout %q: %w", assignTimeout, err)
		}
		opts.assignTimeout = d
	}
	return opts, nil
}

// runDistributed orchestrates a distributed load test across multiple worker processes.
func runDistributed(scenarioFile string, workerAddrs []string, promAddr string, httpCfg protocols.HTTPConfig, opts distOptions) error {
	sc, err := scenarios.ParseFile(scenarioFile)
	if err != nil {
		return fmt.Errorf("loading scenario: %w", err)
	}

	yamlBytes, err := os.ReadFile(scenarioFile)
	if err != nil {
		return fmt.Errorf("reading scenario: %w", err)
	}

	coordinator := distributed.NewCoordinator(workerAddrs, yamlBytes, sc, httpCfg)
	coordinator.SetSecret(opts.secret)
	coordinator.SetHTTPClient(opts.client)
	coordinator.SetTiming(opts.pollInterval, opts.assignTimeout)

	fmt.Printf("Running scenario %q on %d worker(s)  (%d stages, %d step(s))\n\n", sc.Name, len(workerAddrs), len(sc.Stages), len(sc.Steps))
	for i, addr := range workerAddrs {
		fmt.Printf("  Worker %d: %s\n", i+1, addr)
	}
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var sum metrics.Summary

	go func() {
		defer close(done)
		sum, err = coordinator.Run(ctx)
	}()

	// Create a live provider that wraps the coordinator's snapshot method
	provider := &coordinatorProvider{coordinator: coordinator, startedAt: time.Now()}

	if promAddr != "" {
		prom := reporter.NewPrometheusServer(provider, promAddr)
		if promErr := prom.Start(ctx); promErr != nil {
			fmt.Fprintf(os.Stderr, "warning: prometheus unavailable: %v\n", promErr)
		} else {
			fmt.Printf("Prometheus → http://%s/metrics\n\n", promAddr)
		}
	}

	// The TUI needs a real terminal. In CI/piped output (or with --no-tui),
	// fall back to plain progress lines so the run completes instead of the
	// TUI exiting immediately and cancelling the test.
	if opts.noTUI || !isTerminal() {
		runHeadlessProgress(provider, cancel, done, opts.pollInterval)
	} else if tuiErr := reporter.RunTUI(provider, cancel, done); tuiErr != nil {
		<-done
	}
	cancel()
	<-done

	if err != nil {
		return err
	}

	reporter.PrintSummary(os.Stdout, sum)
	return nil
}

// coordinatorProvider supplies live metrics snapshots from the coordinator (for TUI).
type coordinatorProvider struct {
	coordinator *distributed.Coordinator
	startedAt   time.Time
}

func (p *coordinatorProvider) Snapshot() reporter.LiveSnapshot {
	snap := p.coordinator.LiveSnapshot()
	snap.Elapsed = time.Since(p.startedAt)
	return snap
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
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: dur, Target: vus}},
		Steps:    []engine.RampStep{{Name: req.Method + " " + req.URL, Request: req}},
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

	rampDur, holdDur := rateProfile(dur)
	stgs := rateStages(targetRPS, dur)
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

	fmt.Printf("Running rate mode: %d req/s for %s → %s %s\n", targetRPS, duration, req.Method, url)
	fmt.Printf("%s\n\n", rateProfileLine(targetRPS, rampDur, holdDur))
	return eng.Run(context.Background()), nil
}

// saveResults writes results to path. Uses JUnit XML format for .xml files,
// JSON for all other extensions.
func saveResults(path string, sum metrics.Summary, scenarioName, thresholdMsg string) (err error) {
	f, createErr := os.Create(path)
	if createErr != nil {
		return createErr
	}
	// 寫入側 close 錯誤 = 結果檔可能不完整,必須傳回而非吞掉
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("關閉結果檔失敗(內容可能不完整): %w", cerr)
		}
	}()
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
		if s.Capture != nil {
			if compiled := precompileRegexes(s.Capture.Values); len(compiled) > 0 {
				out[i].CompiledRegexes = compiled
			}
		}
	}
	return out
}

// precompileRegexes builds a map of pattern → *regexp.Regexp for all regex: capture expressions.
func precompileRegexes(values map[string]string) map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp)
	for _, expr := range values {
		if strings.HasPrefix(expr, "regex:") {
			pattern := strings.TrimPrefix(expr, "regex:")
			if re, err := regexp.Compile(pattern); err == nil {
				out[pattern] = re
			}
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

// isTerminal reports whether stdout is an interactive terminal. When false
// (CI, pipes, file redirection), the live TUI cannot run.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runHeadlessProgress prints periodic plain-text progress lines until the run
// finishes, and cancels the run on SIGINT/SIGTERM. Used in place of the TUI in
// non-interactive environments.
func runHeadlessProgress(provider reporter.LiveProvider, cancel context.CancelFunc, done <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-sigs:
			fmt.Fprintln(os.Stderr, "\nInterrupted — stopping workers...")
			cancel()
			return
		case <-ticker.C:
			s := provider.Snapshot()
			fmt.Printf("[%s] VUs=%d  reqs=%d  errors=%d  rps=%.0f  p99=%dms\n",
				s.Elapsed.Round(time.Second), s.ActiveVUs, s.Total, s.Errors, s.RPS, s.P99.Milliseconds())
		}
	}
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
