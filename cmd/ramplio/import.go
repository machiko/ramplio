package main

import (
	"fmt"
	"os"
	"time"

	"github.com/machiko/ramplio/v2/internal/importer"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var (
		outputFile string
		noFilter   bool
		duration   string
	)

	cmd := &cobra.Command{
		Use:   "import <recording.har>",
		Short: "Convert a HAR browser recording to a scenario YAML",
		Long: `Import parses a HAR (HTTP Archive) file exported from Chrome or Firefox DevTools
and converts it to a ramplio scenario YAML file.

How to record:
  Chrome/Edge: DevTools → Network → right-click → "Save all as HAR with content"
  Firefox:     DevTools → Network → right-click → "Save All As HAR"`,
		Example: `  ramplio import recording.har
  ramplio import recording.har -o scenario.yaml
  ramplio import recording.har --no-filter`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			harPath := args[0]

			opts := importer.DefaultOptions()
			if noFilter {
				opts.Filter = false
			}
			if duration != "" {
				d, err := time.ParseDuration(duration)
				if err != nil {
					return fmt.Errorf("invalid --duration %q: %w", duration, err)
				}
				opts.Duration = d
			}

			yamlBytes, err := importer.Convert(harPath, opts)
			if err != nil {
				return fmt.Errorf("import: %w", err)
			}

			if outputFile != "" {
				if err := os.WriteFile(outputFile, yamlBytes, 0o644); err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				fmt.Printf("Scenario written to %s\n", outputFile)
				fmt.Println("Next steps:")
				fmt.Printf("  ramplio validate --scenario %s\n", outputFile)
				fmt.Printf("  ramplio run --scenario %s\n", outputFile)
			} else {
				fmt.Print(string(yamlBytes))
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Write scenario YAML to file instead of stdout")
	cmd.Flags().BoolVar(&noFilter, "no-filter", false, "Include static assets (JS/CSS/images) in scenario")
	cmd.Flags().StringVarP(&duration, "duration", "d", "", "Override total test duration (e.g. 2m, 90s)")

	return cmd
}
