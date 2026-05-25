package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ramplio/ramplio/internal/discover"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/spf13/cobra"
)

func newDiscoverCmd() *cobra.Command {
	var (
		url           string
		tolerance     string
		maxRPS        int
		probeDuration string
	)

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Find your site's maximum throughput automatically",
		Long: `Automatically probes your site at increasing request rates to discover
its capacity limit. No load-testing knowledge required.

Run this when you want to answer: "How much traffic can my site handle?"`,
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

			fmt.Printf("  Target:    %s\n", url)
			fmt.Printf("  Tolerance: p99 < %s, error rate < 1%%\n", tolerance)
			fmt.Printf("  Probes:    up to %d levels (est. %.0f–%.0f min)\n\n",
				probeCount, estMinutes*0.5, estMinutes)
			fmt.Print("  Probing throughput capacity...\n\n")

			prober := discover.New(cfg)
			result := prober.Run(ctx, nil, func(pr discover.ProbeResult) {
				printDiscoverProbe(pr)
			})

			fmt.Println()
			printDiscoverReport(result, tol)
			return nil
		},
	}

	cmd.Flags().StringVarP(&url, "url", "u", "", "Target URL (required)")
	_ = cmd.MarkFlagRequired("url")
	cmd.Flags().StringVar(&tolerance, "tolerance", "2s", "Acceptable p99 response time (e.g. 1s, 500ms, 2s)")
	cmd.Flags().IntVar(&maxRPS, "max-rps", 500, "Stop probing above this request rate")
	cmd.Flags().StringVar(&probeDuration, "probe-duration", "15s", "Duration of each probe (e.g. 10s, 30s)")

	return cmd
}

func printDiscoverProbe(pr discover.ProbeResult) {
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

	fmt.Printf("  %5d rps  %s  p99=%-8s  errors=%.1f%%\n",
		pr.RPS, icon, p99str, pr.ErrorRate)
}

const reportWidth = 46

func printDiscoverReport(result discover.DiscoverResult, tolerance time.Duration) {
	top := "  ┌" + strings.Repeat("─", reportWidth) + "┐"
	bot := "  └" + strings.Repeat("─", reportWidth) + "┘"
	sep := "  ├" + strings.Repeat("─", reportWidth) + "┤"
	row := func(s string) {
		fmt.Printf("  │  %-*s│\n", reportWidth-2, s)
	}

	fmt.Println(top)
	row("Capacity Report")
	fmt.Println(sep)

	if result.SafeLimit > 0 {
		row(fmt.Sprintf("Safe limit:     ~%d req/sec", result.SafeLimit))
	} else {
		row("Safe limit:     < 5 req/sec")
	}

	if result.BreakingPoint > 0 {
		row(fmt.Sprintf("Breaking point: ~%d req/sec", result.BreakingPoint))
	} else if result.Exhausted {
		row("Breaking point: not reached")
	} else {
		row("Breaking point: test cancelled")
	}

	fmt.Println(sep)
	row("What this means:")
	row("")

	switch {
	case result.SafeLimit == 0:
		row("Your site is struggling at very low traffic.")
		row("Check server health before load testing.")
	case result.Exhausted:
		row(fmt.Sprintf("Your site handled all %d tested levels.", len(result.Probes)))
		row(fmt.Sprintf("Maximum safe throughput exceeds %d req/sec.", result.SafeLimit))
		row("Run again with --max-rps to probe higher.")
	default:
		row(fmt.Sprintf("Your site handles about %d requests per", result.SafeLimit))
		row(fmt.Sprintf("second comfortably. Above that, response"))
		row(fmt.Sprintf("times climb beyond %s.", tolerance))
	}

	fmt.Println(bot)
}
