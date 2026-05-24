package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/ramplio/ramplio/internal/reporter"
)

func newReportCmd() *cobra.Command {
	var (
		inputFile  string
		outputFile string
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Generate an HTML report from a saved JSON result file",
		Example: `  ramplio report --input results.json
  ramplio report --input results.json --output /tmp/report.html`,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(inputFile)
			if err != nil {
				return fmt.Errorf("opening input file: %w", err)
			}
			defer f.Close()

			r, err := reporter.ReadJSON(f)
			if err != nil {
				return fmt.Errorf("reading JSON result: %w", err)
			}

			outPath := outputFile
			if outPath == "" {
				outPath = "report.html"
			}

			out, err := os.Create(outPath)
			if err != nil {
				return fmt.Errorf("creating output file: %w", err)
			}
			defer out.Close()

			if err := reporter.WriteHTMLFromReport(out, r); err != nil {
				return fmt.Errorf("generating HTML report: %w", err)
			}

			fmt.Printf("✓ Report saved to %s\n", outPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&inputFile, "input", "i", "", "Path to JSON result file (required)")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output path for HTML report (default: report.html)")
	_ = cmd.MarkFlagRequired("input")
	return cmd
}
