package reporter

import (
	"fmt"
	"io"
	"strings"

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

	if sum.DroppedSamples > 0 {
		fmt.Fprintf(w, "\n⚠  警告：%d 個樣本因緩衝區滿被丟棄，指標可能不完整。\n", sum.DroppedSamples)
	}

	fmt.Fprintln(w, "\n"+divider)
	printInterpretation(w, sum)
}

// printInterpretation renders the shared plain-language interpretation below
// the technical summary. The wording comes from Interpret() so the terminal,
// JSON and HTML outputs all read identically.
func printInterpretation(w io.Writer, sum metrics.Summary) {
	in := Interpret(sum)

	section := func(title string) {
		fmt.Fprintf(w, "\n%s\n%s\n", title, strings.Repeat("─", len(title)))
	}
	section("測試結果說明")

	fmt.Fprintf(w, "  整體結論：%s %s\n", in.Icon, in.Verdict)

	fmt.Fprintf(w, "\n  反應速度  %s %s\n", in.Speed.Icon, in.Speed.Label)
	fmt.Fprintf(w, "      99%% 的人點下去後，%s內就看到回應。\n", in.Speed.Value)
	fmt.Fprintf(w, "      （%s）\n", in.Speed.Note)

	fmt.Fprintf(w, "\n  穩定度　  %s %s\n", in.Stability.Icon, in.Stability.Label)
	fmt.Fprintf(w, "      %s\n", in.Stability.Note)

	fmt.Fprintf(w, "\n  承受能力  每秒約 %s 個請求\n", in.Capacity.Value)
	fmt.Fprintf(w, "      %s\n", in.Capacity.Note)

	if in.Bottleneck != "" {
		fmt.Fprintf(w, "\n  %s\n", in.Bottleneck)
	}

	fmt.Fprintf(w, "\n  一句話總結：%s\n", in.OneLiner)

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
