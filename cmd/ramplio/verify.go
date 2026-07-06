package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/machiko/ramplio/internal/engine"
	"github.com/machiko/ramplio/internal/metrics"
	"github.com/machiko/ramplio/internal/protocols"
	"github.com/machiko/ramplio/internal/scenarios"
	"github.com/spf13/cobra"
)

// minVerifySamples is the floor below which we refuse to judge accuracy — too few
// requests makes percentiles meaningless, so we ask for a longer run instead of
// reporting a false failure.
const minVerifySamples = 30

// pctCheck records one percentile's verdict: the injected ground truth, what we
// measured, and whether it sits in the acceptable [injected, injected+tol] band.
// Undercut (measured < injected) is the most serious failure — it can only mean
// the tool under-reports latency, i.e. a measurement bug.
type pctCheck struct {
	Name     string
	Injected time.Duration
	Measured time.Duration
	OK       bool
	Undercut bool
}

type verifyOutcome struct {
	Pass     bool
	Checks   []pctCheck
	Headline string
	Reason   string
}

type verifyHeader struct {
	Distribution string
	Load         string
	Tolerance    string
}

func newPctCheck(name string, injected, tol, measured time.Duration) pctCheck {
	return pctCheck{
		Name:     name,
		Injected: injected,
		Measured: measured,
		Undercut: measured < injected,
		OK:       measured >= injected && measured <= injected+tol,
	}
}

// evaluateFixed judges a fixed-latency run: every percentile must land in
// [injected, injected+tol]. Mirrors TestGroundTruth_FixedLatency.
func evaluateFixed(injected, tol time.Duration, sum metrics.Summary) verifyOutcome {
	checks := []pctCheck{
		newPctCheck("p50", injected, tol, sum.P50),
		newPctCheck("p90", injected, tol, sum.P90),
		newPctCheck("p95", injected, tol, sum.P95),
		newPctCheck("p99", injected, tol, sum.P99),
	}
	out := verifyOutcome{Checks: checks, Pass: true}
	anyUndercut := false
	for _, c := range checks {
		if !c.OK {
			out.Pass = false
		}
		if c.Undercut {
			anyUndercut = true
		}
	}
	applyVerdict(&out, anyUndercut, tol)
	return out
}

// evaluateBimodal judges a bimodal run: p50 must sit in the fast band and p99 in
// the slow band. Mirrors TestGroundTruth_BimodalSeparatesTail. p90/p95 are
// unstable under a bimodal split, so they are not judged here.
func evaluateBimodal(fast, slow, tol time.Duration, sum metrics.Summary) verifyOutcome {
	p50 := newPctCheck("p50（快帶）", fast, tol, sum.P50)
	p99 := newPctCheck("p99（慢帶）", slow, tol, sum.P99)
	out := verifyOutcome{Checks: []pctCheck{p50, p99}, Pass: p50.OK && p99.OK}
	switch {
	case out.Pass:
		out.Headline = "✓ 量測準確：p50 落在快帶、p99 落在慢帶，尾端被正確分離。"
		out.Reason = "雙峰分佈下平均值會抹平尾端，但百分位不該——這次 p99 正確反映了慢請求。"
	case p50.Undercut || p99.Undercut:
		out.Headline = "✗ 量測失準：有百分位低於注入值。"
		out.Reason = "量到的延遲不該低於注入值，這代表量測有 bug，請回報。"
	default:
		out.Headline = "✗ 量測未落在預期帶：可能是本機負載過高或容差過嚴。"
		out.Reason = "試著降低 --vus 或放寬 --tolerance 再跑一次。"
	}
	return out
}

func applyVerdict(out *verifyOutcome, anyUndercut bool, tol time.Duration) {
	switch {
	case out.Pass:
		out.Headline = "✓ 量測準確：所有百分位都落在注入值 +0~" + fmtMs(tol) + " 內。"
		out.Reason = "量測值只會 ≥ 注入值（多了本機往返），這次沒有低於——代表沒有低報延遲的 bug。"
	case anyUndercut:
		out.Headline = "✗ 量測失準：有百分位低於注入值。"
		out.Reason = "量到的延遲不該低於伺服器實際注入的延遲，這代表量測有 bug，請回報。"
	default:
		out.Headline = "✗ 量測超出容差：可能是本機負載過高或容差過嚴。"
		out.Reason = "試著降低 --vus 或放寬 --tolerance 再跑一次。"
	}
}

func writeVerifyReport(w io.Writer, h verifyHeader, out verifyOutcome) {
	fmt.Fprintln(w, "  量測自證 — 對已知延遲分佈施壓，反推 Ramplio 量得準不準")
	fmt.Fprintf(w, "  注入分佈：%s    施壓：%s    容差：%s\n\n", h.Distribution, h.Load, h.Tolerance)
	fmt.Fprintln(w, "  量測結果（注入值 → 量到值）")
	for _, c := range out.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		fmt.Fprintf(w, "    %-12s %6s → %-6s  %s\n", c.Name, fmtMs(c.Injected), fmtMs(c.Measured), mark)
	}
	fmt.Fprintf(w, "\n  %s\n", out.Headline)
	fmt.Fprintf(w, "    %s\n", out.Reason)
}

// parseVerifyProfile resolves the injected latency profile from flags. With both
// --latency-fast and --latency-slow it is bimodal; otherwise fixed (defaulting to
// 50ms when nothing is given). Returns (profile, isBimodal, error).
func parseVerifyProfile(latency, fast, slow string, slowPct int) (latencyProfile, bool, error) {
	if slowPct < 0 || slowPct > 100 {
		return latencyProfile{}, false, fmt.Errorf("--slow-pct must be between 0 and 100, got %d", slowPct)
	}
	parse := func(name, raw string) (time.Duration, error) {
		if raw == "" {
			return 0, nil
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid %s %q: %w", name, raw, err)
		}
		return d, nil
	}
	fixedD, err := parse("--latency", latency)
	if err != nil {
		return latencyProfile{}, false, err
	}
	fastD, err := parse("--latency-fast", fast)
	if err != nil {
		return latencyProfile{}, false, err
	}
	slowD, err := parse("--latency-slow", slow)
	if err != nil {
		return latencyProfile{}, false, err
	}

	if (fastD > 0) != (slowD > 0) {
		return latencyProfile{}, false, fmt.Errorf("雙峰模式需同時指定 --latency-fast 與 --latency-slow")
	}
	if fixedD > 0 && fastD > 0 {
		return latencyProfile{}, false, fmt.Errorf("--latency 與雙峰（--latency-fast/--latency-slow）互斥")
	}
	if fastD > 0 && slowD > 0 {
		return latencyProfile{Fast: fastD, Slow: slowD, SlowPct: slowPct}, true, nil
	}
	if fixedD == 0 {
		fixedD = 50 * time.Millisecond
	}
	return latencyProfile{Fixed: fixedD}, false, nil
}

func newVerifyCmd() *cobra.Command {
	var (
		latency     string
		latencyFast string
		latencySlow string
		slowPct     int
		tolerance   string
		duration    string
		vus         int
	)

	cmd := &cobra.Command{
		Use: "verify",
		// A measurement failure is a real verdict, not a usage error — don't dump
		// the flag help after it.
		SilenceUsage: true,
		Short:        "一鍵自證：證明 Ramplio 的量測準不準",
		Long: `對一個注入了已知延遲分佈的內建 mock server 施壓，把量到的百分位和
注入的 ground truth 比對——量測值只可能 ≥ 注入值，若低於就代表量測有 bug。

不需要任何外部目標，也不靠跟其他工具比對：注入多少延遲是我們決定的事實，
量測該不該等於它是純粹的數學。準確時 exit 0、失準時 exit 1，可放進 CI。`,
		Example: `  ramplio verify
  ramplio verify --latency 100ms --tolerance 30ms
  ramplio verify --latency-fast 10ms --latency-slow 200ms --slow-pct 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, bimodal, err := parseVerifyProfile(latency, latencyFast, latencySlow, slowPct)
			if err != nil {
				return err
			}
			tol, err := time.ParseDuration(tolerance)
			if err != nil {
				return fmt.Errorf("invalid --tolerance %q: %w", tolerance, err)
			}
			dur, err := time.ParseDuration(duration)
			if err != nil {
				return fmt.Errorf("invalid --duration %q: %w", duration, err)
			}

			// Spin up the injected-latency target in-process on an ephemeral port
			// so we never collide with a service the user is already running.
			var reqCount atomic.Int64
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return fmt.Errorf("failed to start mock target: %w", err)
			}
			srv := &http.Server{Handler: newMockHandler(profile, &reqCount)}
			go func() { _ = srv.Serve(ln) }()
			defer srv.Close()
			url := "http://" + ln.Addr().String() + "/"

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-sigs
				cancel()
			}()
			defer signal.Stop(sigs)

			col := metrics.NewCollector(vus * 2)
			eng := engine.NewRamp(engine.RampConfig{
				Stages:   []scenarios.Stage{{Duration: dur, Target: vus}},
				Steps:    []engine.RampStep{{Request: protocols.Request{Method: "GET", URL: url}}},
				Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
			}, col)
			sum := eng.Run(ctx)

			if sum.Total < minVerifySamples {
				fmt.Printf("  樣本太少（只有 %d 筆），無法判定。試著加長 --duration 再跑一次。\n", sum.Total)
				return nil
			}

			var out verifyOutcome
			var dist string
			if bimodal {
				out = evaluateBimodal(profile.Fast, profile.Slow, tol, sum)
				dist = fmt.Sprintf("%s 快 / %s 慢（%d%%）", fmtMs(profile.Fast), fmtMs(profile.Slow), profile.SlowPct)
			} else {
				out = evaluateFixed(profile.Fixed, tol, sum)
				dist = "固定 " + fmtMs(profile.Fixed)
			}
			writeVerifyReport(os.Stdout, verifyHeader{
				Distribution: dist,
				Load:         fmt.Sprintf("%d VU × %s", vus, dur),
				Tolerance:    "±" + fmtMs(tol),
			}, out)

			if !out.Pass {
				return fmt.Errorf("量測自證未通過")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&latency, "latency", "", "固定注入延遲（預設 50ms；與雙峰互斥）")
	cmd.Flags().StringVar(&latencyFast, "latency-fast", "", "雙峰：快帶延遲（多數請求）")
	cmd.Flags().StringVar(&latencySlow, "latency-slow", "", "雙峰：慢帶延遲（尾端）")
	cmd.Flags().IntVar(&slowPct, "slow-pct", 10, "雙峰：多少 % 的請求走慢帶（0-100）")
	cmd.Flags().StringVar(&tolerance, "tolerance", "20ms", "可接受的量測誤差")
	cmd.Flags().StringVar(&duration, "duration", "3s", "施壓時長")
	cmd.Flags().IntVar(&vus, "vus", 10, "並發虛擬使用者數")
	return cmd
}
