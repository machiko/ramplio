package reporter

import (
	"fmt"
	"io"
	"strconv"
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

	if sum.DroppedSamples > 0 {
		fmt.Fprintf(w, "\n⚠  警告：%d 個樣本因緩衝區滿被丟棄，指標可能不完整。\n", sum.DroppedSamples)
	}

	fmt.Fprintln(w, "\n"+divider)
	printInterpretation(w, sum)
}

// printInterpretation appends a plain-language explanation below the technical
// summary. It avoids jargon (p50/p99/RPS) and translates the numbers into
// everyday terms so a non-expert can tell at a glance whether the site/API held
// up under load.
func printInterpretation(w io.Writer, sum metrics.Summary) {
	errRate := sum.ErrorRate()
	p99ms := sum.P99.Milliseconds()

	section := func(title string) {
		fmt.Fprintf(w, "\n%s\n%s\n", title, strings.Repeat("─", len(title)))
	}
	section("測試結果說明")

	// 整體結論 — one-glance verdict.
	icon, verdict := "✓", "網站很健康，可以放心上線"
	switch {
	case errRate >= 5.0 || p99ms >= 3000:
		icon, verdict = "✗", "網站在這個壓力下出問題了，建議先別上線"
	case errRate >= 1.0 || p99ms >= 1000:
		icon, verdict = "⚠", "網站堪用，但有地方需要注意"
	}
	fmt.Fprintf(w, "  整體結論：%s %s\n", icon, verdict)

	// 反應速度 — translate the tail latency into human-perceptible terms.
	speedIcon, speedLabel, speedNote := speedVerdict(p99ms)
	fmt.Fprintf(w, "\n  反應速度  %s %s\n", speedIcon, speedLabel)
	fmt.Fprintf(w, "      99%% 的人點下去後，%s內就看到回應。\n", humanizeDuration(sum.P99))
	fmt.Fprintf(w, "      （%s）\n", speedNote)

	// 穩定度 — how many requests failed, in plain language.
	stIcon, stLabel, stNote := stabilityVerdict(errRate, sum.Total, sum.Errors)
	fmt.Fprintf(w, "\n  穩定度　  %s %s\n", stIcon, stLabel)
	fmt.Fprintf(w, "      %s\n", stNote)

	// 承受能力 — throughput plus whether there is headroom left.
	fmt.Fprintf(w, "\n  承受能力  每秒約 %s 個請求\n", humanizeInt(int64(sum.RPS()+0.5)))
	switch {
	case errRate == 0:
		fmt.Fprintln(w, "      錯誤率 0，代表這個壓力下軟體還有餘裕。")
	case errRate < 5.0:
		fmt.Fprintln(w, "      已經開始出現少量失敗，可能接近能負荷的上限。")
	default:
		fmt.Fprintln(w, "      大量請求失敗，代表已經超過能負荷的量。")
	}

	// 瓶頸 — only meaningful when there is more than one step.
	if len(sum.Steps) > 1 {
		var slowestName string
		var slowestP99 time.Duration
		for _, s := range sum.Steps {
			if s.P99 > slowestP99 {
				slowestP99 = s.P99
				slowestName = s.Name
			}
		}
		fmt.Fprintf(w, "\n  最花時間的步驟是「%s」（%s內完成），要加快先從這裡下手。\n",
			slowestName, humanizeDuration(slowestP99))
	}

	// 一句話總結 — adapts to the combination of speed and stability.
	fmt.Fprintf(w, "\n  一句話總結：%s\n", oneLineSummary(p99ms, errRate))

	fmt.Fprintln(w, "\n"+divider)
}

// speedVerdict maps tail latency (p99, ms) to a human perception of speed.
func speedVerdict(p99ms int64) (icon, label, note string) {
	switch {
	case p99ms < 100:
		return "⚡", "非常快（幾乎即時）", "低於 0.1 秒，快到使用者根本感覺不到等待"
	case p99ms < 300:
		return "⚡", "很快（流暢）", "使用者幾乎感覺不到延遲"
	case p99ms < 1000:
		return "✓", "普通", "使用者開始能感覺到一點點等待"
	case p99ms < 3000:
		return "⚠", "偏慢", "使用者會明顯覺得卡頓"
	default:
		return "✗", "很慢", "使用者可能等不及就離開了"
	}
}

// stabilityVerdict describes the failure rate in plain language.
func stabilityVerdict(errRate float64, total, errors int64) (icon, label, note string) {
	switch {
	case errRate == 0:
		return "✓", "完美", fmt.Sprintf("這次共試了 %s 次，沒有任何一次失敗。", humanizeInt(total))
	case errRate < 1.0:
		return "✓", "良好", fmt.Sprintf("約每 %s 次才有 1 次失敗（%.2f%%），大致穩定。", humanizeInt(int64(100.0/errRate+0.5)), errRate)
	case errRate < 5.0:
		return "⚠", "有點不穩", fmt.Sprintf("%.1f%% 的請求失敗（共 %s 個），建議查一下原因。", errRate, humanizeInt(errors))
	default:
		return "✗", "不穩定", fmt.Sprintf("%.1f%% 的請求失敗（共 %s 個），服務開始撐不住了。", errRate, humanizeInt(errors))
	}
}

// oneLineSummary combines speed and stability into a single takeaway.
func oneLineSummary(p99ms int64, errRate float64) string {
	fast := p99ms < 1000
	stable := errRate < 1.0
	switch {
	case fast && stable:
		return "整體來說，網站又快又穩，可以放心。"
	case !fast && stable:
		return "網站很穩定，但反應偏慢，使用者體驗會打折扣。"
	case fast && !stable:
		return "網站反應很快，但有請求失敗，建議先解決穩定度問題。"
	default:
		return "網站又慢又不穩，建議先處理問題再上線。"
	}
}

// humanizeDuration renders a duration in 毫秒/秒 for non-technical readers.
func humanizeDuration(d time.Duration) string {
	ms := d.Milliseconds()
	if ms < 1000 {
		return fmt.Sprintf("%d 毫秒", ms)
	}
	return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
}

// humanizeInt formats an integer with thousands separators (e.g. 20046 → 20,046).
func humanizeInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
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
