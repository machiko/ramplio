package main

import (
	"fmt"
	"os"

	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/spf13/cobra"
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
			defer func() { _ = f.Close() }() // 唯讀,close 錯誤無資料風險

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

			if err := reporter.WriteHTMLFromReport(out, r); err != nil {
				_ = out.Close()
				return fmt.Errorf("generating HTML report: %w", err)
			}
			// 寫入側的 close 錯誤 = 報告可能不完整,必須回報而非吞掉
			if err := out.Close(); err != nil {
				return fmt.Errorf("關閉報告檔失敗(內容可能不完整): %w", err)
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
