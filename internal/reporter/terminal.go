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

	fmt.Fprintln(w, "\n"+divider)
}

func formatDuration(d interface{ Milliseconds() int64 }) string {
	ms := d.Milliseconds()
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.2fs", float64(ms)/1000)
}
