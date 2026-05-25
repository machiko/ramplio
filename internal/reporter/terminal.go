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

	section("Results")
	line("Total requests:", fmt.Sprintf("%d", sum.Total))
	line("Duration:", fmt.Sprintf("%.2fs", sum.WallTime.Seconds()))
	line("Req/sec:", fmt.Sprintf("%.1f", sum.RPS()))

	section("Latency")
	line("Min:", formatDuration(sum.MinLatency))
	line("Mean:", formatDuration(sum.MeanLatency()))
	line("p50:", formatDuration(sum.P50))
	line("p90:", formatDuration(sum.P90))
	line("p95:", formatDuration(sum.P95))
	line("p99:", formatDuration(sum.P99))
	line("Max:", formatDuration(sum.MaxLatency))

	section("Status")
	success := sum.Total - sum.Errors
	if sum.Total > 0 {
		line("Success (2xx):", fmt.Sprintf("%d (%.1f%%)", success, float64(success)/float64(sum.Total)*100))
		line("Errors:", fmt.Sprintf("%d (%.1f%%)", sum.Errors, sum.ErrorRate()))
	}

	if len(sum.Steps) > 0 {
		section("Per-step Breakdown")
		// Find max p99 to identify the bottleneck step.
		maxP99 := sum.Steps[0].P99
		for _, s := range sum.Steps[1:] {
			if s.P99 > maxP99 {
				maxP99 = s.P99
			}
		}
		fmt.Fprintf(w, "  %-30s %8s %8s %8s %8s\n", "Step", "Total", "p50", "p99", "Errors")
		fmt.Fprintf(w, "  %s\n", strings.Repeat("─", 66))
		for _, s := range sum.Steps {
			tag := ""
			if s.P99 == maxP99 && len(sum.Steps) > 1 {
				tag = " ◀ slowest"
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
		section("Group Breakdown")
		fmt.Fprintf(w, "  %-24s %8s %8s %8s %8s\n", "Group", "Total", "p50", "p95", "Errors")
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

	level, icon, headline := "PASS", "✓", "Your API handled the load well."
	if errRate >= 5.0 || p99ms >= 1000 {
		level, icon, headline = "FAIL", "✗", "Your API struggled under this load."
	} else if errRate >= 1.0 || p99ms >= 500 {
		level, icon, headline = "WARN", "⚠", "Your API performed acceptably, with some concerns."
	}

	section := func(title string) {
		fmt.Fprintf(w, "\n%s\n%s\n", title, strings.Repeat("─", len(title)))
	}
	section("What this means")
	fmt.Fprintf(w, "  %s %s — %s\n", icon, level, headline)

	fmt.Fprintf(w, "\n  Speed:       Half your users got a response in %s. 99%% within %s.\n",
		formatDuration(sum.P50), formatDuration(sum.P99))

	switch {
	case errRate == 0:
		fmt.Fprintln(w, "  Reliability: All requests succeeded — no errors.")
	case errRate < 0.1:
		fmt.Fprintf(w, "  Reliability: Roughly 1 in %d requests failed (%.2f%%).\n", int(100.0/errRate), errRate)
	default:
		fmt.Fprintf(w, "  Reliability: %.1f%% of requests failed (%d errors).\n", errRate, sum.Errors)
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
		fmt.Fprintf(w, "  Bottleneck:  Slowest step: %q (%s p99).\n", slowestName, formatDuration(slowestP99))
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
