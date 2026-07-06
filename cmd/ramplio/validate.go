package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/machiko/ramplio/internal/scenarios"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	var scenarioFile string

	cmd := &cobra.Command{
		Use:     "validate",
		Short:   "Validate a scenario file without running it",
		Example: `  ramplio validate --scenario testdata/smoke.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sc, err := scenarios.ParseFile(scenarioFile)
			if err != nil {
				return fmt.Errorf("invalid scenario: %w", err)
			}

			fmt.Printf("✓ Scenario %q is valid\n\n", sc.Name)

			// Stages
			fmt.Printf("  Stages (%d)\n", len(sc.Stages))
			totalDur := time.Duration(0)
			for i, s := range sc.Stages {
				totalDur += s.Duration
				if s.TargetRPS > 0 {
					fmt.Printf("    [%d] %s → %d RPS\n", i+1, s.Duration, s.TargetRPS)
				} else {
					fmt.Printf("    [%d] %s → %d VU\n", i+1, s.Duration, s.Target)
				}
			}
			fmt.Printf("    Total duration: %s\n\n", totalDur)

			// Setup / Teardown
			if len(sc.Setup) > 0 {
				fmt.Printf("  Setup steps (%d)\n", len(sc.Setup))
				for _, s := range sc.Setup {
					fmt.Printf("    • %s %s\n", strings.ToUpper(s.Method), s.URL)
				}
				fmt.Println()
			}

			// Steps
			fmt.Printf("  Steps (%d)\n", len(sc.Steps))
			for _, s := range sc.Steps {
				name := s.Name
				if name == "" {
					name = strings.ToUpper(s.Method) + " " + s.URL
				}
				extras := []string{}
				if s.Group != "" {
					extras = append(extras, "group="+s.Group)
				}
				if s.Capture != nil && len(s.Capture.Values) > 0 {
					keys := make([]string, 0, len(s.Capture.Values))
					for k := range s.Capture.Values {
						keys = append(keys, k)
					}
					extras = append(extras, "captures=["+strings.Join(keys, ",")+"]")
				}
				if s.If != "" {
					extras = append(extras, "if="+s.If)
				}
				suffix := ""
				if len(extras) > 0 {
					suffix = "  (" + strings.Join(extras, ", ") + ")"
				}
				fmt.Printf("    • %s%s\n", name, suffix)
			}

			if len(sc.Teardown) > 0 {
				fmt.Printf("\n  Teardown steps (%d)\n", len(sc.Teardown))
				for _, s := range sc.Teardown {
					fmt.Printf("    • %s %s\n", strings.ToUpper(s.Method), s.URL)
				}
			}

			// Vars
			if len(sc.Vars) > 0 {
				fmt.Printf("\n  Vars (%d)\n", len(sc.Vars))
				for k, v := range sc.Vars {
					fmt.Printf("    %s = %q\n", k, v)
				}
			}

			// VarsFrom
			if sc.VarsFrom != nil && sc.VarsFrom.File != "" {
				fmt.Printf("\n  Data file: %s (mode=%s)\n", sc.VarsFrom.File, sc.VarsFrom.Mode)
			}

			// Thresholds
			if sc.Thresholds != nil {
				fmt.Printf("\n  Thresholds\n")
				if sc.Thresholds.ErrorRatePct != nil {
					fmt.Printf("    error_rate_pct ≤ %.1f%%\n", *sc.Thresholds.ErrorRatePct)
				}
				if sc.Thresholds.P95Ms != nil {
					fmt.Printf("    p95_ms ≤ %.0fms\n", *sc.Thresholds.P95Ms)
				}
				if sc.Thresholds.P99Ms != nil {
					fmt.Printf("    p99_ms ≤ %.0fms\n", *sc.Thresholds.P99Ms)
				}
			}

			// Circuit Breaker
			if sc.CircuitBreaker != nil {
				win := sc.CircuitBreaker.WindowSeconds
				if win == 0 {
					win = 1
				}
				fmt.Printf("\n  Circuit breaker: trip after %d failures within %ds\n",
					sc.CircuitBreaker.ConsecutiveFailures, win)
			}

			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVarP(&scenarioFile, "scenario", "s", "", "Path to scenario YAML file (required)")
	_ = cmd.MarkFlagRequired("scenario")
	return cmd
}
