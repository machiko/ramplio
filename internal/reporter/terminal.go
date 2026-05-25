package reporter

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
)

const divider = "────────────────────────────────────────"

func PrintSummary(w io.Writer, sum metrics.Summary) {
	line := func(label, value string) {
		fmt.Fprintf(w, "  %-20s %s\n", label, value)
	}
	section := func(title string) {
		fmt.Fprintf(w, "\n%s\n%s\n", title, strings.Repeat("─", len(title)))
	}

	section("測試結果")
	line("總請求數：", fmt.Sprintf("%d", sum.Total))
	line("測試時長：", fmt.Sprintf("%.2fs", sum.WallTime.Seconds()))
	line("每秒請求：", fmt.Sprintf("%.1f", sum.RPS()))

	section("延遲分佈")
	line("最短：", formatDuration(sum.MinLatency))
	line("平均：", formatDuration(sum.MeanLatency()))
	line("p50：", formatDuration(sum.P50))
	line("p90：", formatDuration(sum.P90))
	line("p95：", formatDuration(sum.P95))
	line("p99：", formatDuration(sum.P99))
	line("最長：", formatDuration(sum.MaxLatency))

	section("回應狀態")
	success := sum.Total - sum.Errors
	if sum.Total > 0 {
		line("成功 (2xx)：", fmt.Sprintf("%d (%.1f%%)", success, float64(success)/float64(sum.Total)*100))
		line("失敗：", fmt.Sprintf("%d (%.1f%%)", sum.Errors, sum.ErrorRate()))
	}

	if len(sum.Steps) > 0 {
		section("各步驟明細")
		// Find max p99 to identify the bottleneck step.
		maxP99 := sum.Steps[0].P99
		for _, s := range sum.Steps[1:] {
			if s.P99 > maxP99 {
				maxP99 = s.P99
			}
		}
		fmt.Fprintf(w, "  %-30s %8s %8s %8s %8s\n", "步驟", "總數", "p50", "p99", "失敗率")
		fmt.Fprintf(w, "  %s\n", strings.Repeat("─", 66))
		for _, s := range sum.Steps {
			tag := ""
			if s.P99 == maxP99 && len(sum.Steps) > 1 {
				tag = " ◀ 最慢"
			}
			errPct := float64(0)
			if s.Total > 0 {
				errPct = float64(s.Errors) / float64(s.Total) * 100
			}
			fmt.Fprintf(w, "  %-30s %8d %8s %8s %6.1f%%%s\n",
				truncate(s.Name, 30),
				s.Total,
				formatDuration(s.P50),
				formatDuration(s.P99),
				errPct,
				tag,
			)
		}
	}

	if len(sum.Groups) > 0 {
		section("群組明細")
		fmt.Fprintf(w, "  %-24s %8s %8s %8s %8s\n", "群組", "總數", "p50", "p95", "失敗率")
		fmt.Fprintf(w, "  %s\n", strings.Repeat("─", 62))
		for _, g := range sum.Groups {
			errPct := float64(0)
			if g.Total > 0 {
				errPct = float64(g.Errors) / float64(g.Total) * 100
			}
			fmt.Fprintf(w, "  %-24s %8d %8s %8s %6.1f%%\n",
				truncate(g.Name, 24),
				g.Total,
				formatDuration(g.P50),
				formatDuration(g.P95),
				errPct,
			)
		}
	}

	fmt.Fprintln(w, "\n"+divider)
	printInterpretation(w, sum)
}

// printInterpretation appends a plain-language verdict below the technical summary.
func printInterpretation(w io.Writer, sum metrics.Summary) {
	errRate := sum.ErrorRate()
	p99ms := sum.P99.Milliseconds()

	level, icon, headline := "通過", "✓", "服務在此負載下表現良好。"
	if errRate >= 5.0 || p99ms >= 1000 {
		level, icon, headline = "未通過", "✗", "服務在此負載下出現明顯問題。"
	} else if errRate >= 1.0 || p99ms >= 500 {
		level, icon, headline = "警告", "⚠", "服務尚可接受，但有需要注意的地方。"
	}

	section := func(title string) {
		fmt.Fprintf(w, "\n%s\n%s\n", title, strings.Repeat("─", len(title)*3/2))
	}
	section("結果解讀")
	fmt.Fprintf(w, "  %s %s — %s\n", icon, level, headline)

	fmt.Fprintf(w, "\n  速度：     有一半的用戶在 %s 內收到回應，99%% 的用戶在 %s 內。\n",
		formatDuration(sum.P50), formatDuration(sum.P99))

	switch {
	case errRate == 0:
		fmt.Fprintln(w, "  穩定性：   所有請求均成功，沒有任何錯誤。")
	case errRate < 0.1:
		fmt.Fprintf(w, "  穩定性：   約每 %d 個請求中有 1 個失敗（%.2f%%）。\n", int(100.0/errRate), errRate)
	default:
		fmt.Fprintf(w, "  穩定性：   %.1f%% 的請求失敗（共 %d 個錯誤）。\n", errRate, sum.Errors)
	}

	if len(sum.Steps) > 1 {
		var slowestName string
		var slowestP99 time.Duration
		for _, s := range sum.Steps {
			if s.P99 > slowestP99 {
				slowestP99 = s.P99
				slowestName = s.Name
			}
		}
		fmt.Fprintf(w, "  瓶頸：     最慢的步驟是 %q（p99 %s）。\n", slowestName, formatDuration(slowestP99))
	}

	fmt.Fprintln(w, "\n"+divider)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func formatDuration(d interface{ Milliseconds() int64 }) string {
	ms := d.Milliseconds()
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.2fs", float64(ms)/1000)
}
