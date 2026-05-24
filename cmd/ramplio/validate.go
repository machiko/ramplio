package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ramplio/ramplio/internal/scenarios"
)

func newValidateCmd() *cobra.Command {
	var scenarioFile string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a scenario file without running it",
		Example: `  ramplio validate --scenario testdata/smoke.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sc, err := scenarios.ParseFile(scenarioFile)
			if err != nil {
				return fmt.Errorf("invalid scenario: %w", err)
			}
			fmt.Printf("✓ Scenario %q is valid\n", sc.Name)
			fmt.Printf("  %d stage(s), %d step(s)\n", len(sc.Stages), len(sc.Steps))
			return nil
		},
	}

	cmd.Flags().StringVarP(&scenarioFile, "scenario", "s", "", "Path to scenario YAML file (required)")
	_ = cmd.MarkFlagRequired("scenario")
	return cmd
}
