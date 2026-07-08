package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/discover"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/spf13/cobra"
)

func newDiscoverCmd() *cobra.Command {
	var (
		url           string
		tolerance     string
		maxRPS        int
		probeDuration string
		baselineFile  string
	)

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "給網址，自動找出你的服務能撐多少人",
		Long: `對目標以遞增的請求速率自動探測，找出容量上限。不需要任何壓測知識。

當你想回答「我的服務撐得住多少流量？」時，就跑這個。`,
		Example: `  ramplio discover --url https://example.com
  ramplio discover --url https://example.com --tolerance 1s
  ramplio discover --url https://example.com --max-rps 300 --probe-duration 30s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			tol, err := time.ParseDuration(tolerance)
			if err != nil {
				return fmt.Errorf("invalid --tolerance %q: %w", tolerance, err)
			}
			pd, err := time.ParseDuration(probeDuration)
			if err != nil {
				return fmt.Errorf("invalid --probe-duration %q: %w", probeDuration, err)
			}

			cfg := discover.Config{
				URL:           url,
				Tolerance:     tol,
				MaxRPS:        maxRPS,
				ProbeDuration: pd,
				HTTPConfig:    protocols.DefaultHTTPConfig(),
			}

			ctx, cancel := context.WithCancel(context.Background())
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-sigs
				cancel()
			}()
			defer signal.Stop(sigs)

			probeCount := len(discover.ProbeSequence(maxRPS))
			estMinutes := float64(int(pd.Seconds())*probeCount) / 60.0

			fmt.Printf("  目標：    %s\n", url)
			fmt.Printf("  容許值：  p99 < %s、錯誤率 < 1%%\n", tolerance)
			fmt.Printf("  探測點：  最多 %d 個等級（預估 %.0f–%.0f 分鐘）\n\n",
				probeCount, estMinutes*0.5, estMinutes)
			fmt.Print("  正在探測吞吐容量…\n\n")

			prober := discover.New(cfg)
			result := prober.Run(ctx, nil, func(pr discover.ProbeResult) {
				printDiscoverProbe(pr)
			})

			fmt.Println()
			printDiscoverReport(result, tol)

			if baselineFile != "" {
				b := baseline.FromDiscover(result, url)
				b.GitCommit = currentGitCommit()
				writeBaselineFile(os.Stdout, os.Stderr, baselineFile, b)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&url, "url", "u", "", "Target URL (required)")
	_ = cmd.MarkFlagRequired("url")
	cmd.Flags().StringVar(&tolerance, "tolerance", "2s", "Acceptable p99 response time (e.g. 1s, 500ms, 2s)")
	cmd.Flags().IntVar(&maxRPS, "max-rps", 500, "Stop probing above this request rate")
	cmd.Flags().StringVar(&probeDuration, "probe-duration", "15s", "Duration of each probe (e.g. 10s, 30s)")
	cmd.Flags().StringVar(&baselineFile, "save-baseline", "", "把容量結果存成 baseline 快照(供 ramplio compare 守門)")

	return cmd
}

func printDiscoverProbe(pr discover.ProbeResult) {
	writeDiscoverProbe(os.Stdout, pr)
}

func writeDiscoverProbe(w io.Writer, pr discover.ProbeResult) {
	icon := "✓"
	switch pr.Status {
	case discover.ProbeWarn:
		icon = "⚠"
	case discover.ProbeFail:
		icon = "✗"
	}

	var p99str string
	ms := pr.P99.Milliseconds()
	if ms >= 1000 {
		p99str = fmt.Sprintf("%.1fs", float64(ms)/1000)
	} else {
		p99str = fmt.Sprintf("%dms", ms)
	}

	fmt.Fprintf(w, "  每秒 %5d 個  %s  p99=%-8s  錯誤=%.1f%%\n",
		pr.RPS, icon, p99str, pr.ErrorRate)
}

const reportWidth = 46

// displayWidth approximates the terminal column width of s: ASCII runes count as
// 1, everything else (CJK, full-width punctuation) as 2. The report box aligns on
// this rather than len() (bytes) or rune count so the right border stays straight
// when Chinese text is mixed in.
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		if r < 128 {
			width++
		} else {
			width += 2
		}
	}
	return width
}

func printDiscoverReport(result discover.DiscoverResult, tolerance time.Duration) {
	writeDiscoverReport(os.Stdout, result, tolerance)
}

func writeDiscoverReport(w io.Writer, result discover.DiscoverResult, tolerance time.Duration) {
	top := "  ┌" + strings.Repeat("─", reportWidth) + "┐"
	bot := "  └" + strings.Repeat("─", reportWidth) + "┘"
	sep := "  ├" + strings.Repeat("─", reportWidth) + "┤"
	row := func(s string) {
		pad := reportWidth - 2 - displayWidth(s)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(w, "  │  %s%s│\n", s, strings.Repeat(" ", pad))
	}

	fmt.Fprintln(w, top)
	row("容量報告")
	fmt.Fprintln(w, sep)

	if result.SafeLimit > 0 {
		row(fmt.Sprintf("安全上限：    每秒約 %d 個請求", result.SafeLimit))
	} else {
		row("安全上限：    每秒不到 5 個請求")
	}

	if result.BreakingPoint > 0 {
		row(fmt.Sprintf("臨界點：      每秒約 %d 個請求", result.BreakingPoint))
	} else if result.Exhausted {
		row("臨界點：      測試範圍內未觸及")
	} else {
		row("臨界點：      測試已中斷")
	}

	fmt.Fprintln(w, sep)
	row("這代表什麼：")
	row("")

	switch {
	case result.SafeLimit == 0:
		row("你的服務在很低的流量下就吃力了。")
		row("建議先檢查伺服器健康狀態，再做壓測。")
	case result.Exhausted:
		row(fmt.Sprintf("你的服務通過了全部 %d 個測試等級。", len(result.Probes)))
		row(fmt.Sprintf("最大安全吞吐量超過每秒 %d 個請求。", result.SafeLimit))
		row("想探更高可加 --max-rps 再跑一次。")
	default:
		row(fmt.Sprintf("你的服務每秒約能穩定處理 %d 個請求。", result.SafeLimit))
		row(fmt.Sprintf("超過後回應時間會拉長到 %s 以上。", tolerance))
	}

	row("")
	row("這個數字怎麼信？想驗證工具本身量得準不準，")
	row("跑一行 ramplio verify 即可一鍵自證。")

	fmt.Fprintln(w, bot)
}
