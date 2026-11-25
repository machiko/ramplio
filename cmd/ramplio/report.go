package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/machiko/ramplio/v3/internal/history"
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
		Short: "Generate an HTML report from the latest run record or a saved JSON result file",
		Example: `  ramplio report
  ramplio report --input results.json
  ramplio report --input results.json --output /tmp/report.html`,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := loadReportSource(cmd.OutOrStdout(), cmd.ErrOrStderr(), inputFile)
			if err != nil {
				return err
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

			fmt.Fprintf(cmd.OutOrStdout(), "✓ Report saved to %s\n", outPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&inputFile, "input", "i", "", "Path to JSON result file (default: the latest run record in .ramplio/)")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output path for HTML report (default: report.html)")
	return cmd
}

// loadReportSource reads --input when given; otherwise the newest run record
// that carries a report (discover records have none).
func loadReportSource(outW, errW io.Writer, inputFile string) (reporter.Report, error) {
	if inputFile != "" {
		f, err := os.Open(inputFile)
		if err != nil {
			return reporter.Report{}, fmt.Errorf("opening input file: %w", err)
		}
		defer func() { _ = f.Close() }() // 唯讀,close 錯誤無資料風險

		r, err := reporter.ReadJSON(f)
		if err != nil {
			return reporter.Report{}, fmt.Errorf("reading JSON result: %w", err)
		}
		return r, nil
	}

	dir := historyDir()
	entries, listErrs := history.List(dir)
	for _, err := range listErrs {
		fmt.Fprintf(errW, "warning: 略過無法讀取的執行紀錄: %v\n", err)
	}
	latest, ok := history.LatestWithReport(entries)
	if !ok {
		if len(entries) > 0 {
			return reporter.Report{}, errors.New("最近的執行紀錄都是容量探測（discover），它沒有 HTML 報告。" +
				"要看完整報告請先跑一次壓測，例如：ramplio run --url https://example.com")
		}
		return reporter.Report{}, fmt.Errorf("%s 裡還沒有執行紀錄。先跑一次壓測，例如：ramplio run --url https://example.com"+
			"（或用 --input 指定 run --output 存的 JSON）", dir)
	}
	fmt.Fprintf(outW, "使用執行紀錄：%s（目標 %s）\n",
		latest.Record.CreatedAt.Local().Format(historyTimeLayout), latest.Record.Scenario)
	return *latest.Record.Report, nil
}
