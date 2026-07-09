package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/spf13/cobra"
)

// metricLabels 把指標鍵翻成白話;新指標沒對到時退回原鍵名,不會漏印。
// 用語與 terminal/interpret 單一來源對齊:服務端延遲=「伺服器處理」、
// CO 修正延遲=「使用者實感」,不自創同義詞。
var metricLabels = map[string]string{
	"p50_ms":             "p50（伺服器處理）",
	"p99_ms":             "p99（伺服器處理）",
	"corrected_p99_ms":   "p99（使用者實感）",
	"error_rate_pct":     "錯誤率",
	"throughput_rps":     "每秒請求",
	"safe_limit_rps":     "安全上限",
	"breaking_point_rps": "臨界點",
}

func metricLabel(name string) string {
	if l, ok := metricLabels[name]; ok {
		return l
	}
	return name
}

func formatMetricValue(name string, v float64) string {
	switch {
	case strings.HasSuffix(name, "_ms"):
		return fmt.Sprintf("%.0fms", v)
	case strings.HasSuffix(name, "_pct"):
		return fmt.Sprintf("%.1f%%", v)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// renderComparison 輸出白話比較報告。Warnings 一律完整印出——
// 不可信的比較結果默默通過,是守門工具最危險的假陽性。
func renderComparison(w io.Writer, c baseline.Comparison) {
	fmt.Fprintln(w, "\n容量回歸比較(本次 vs 基準)")
	fmt.Fprintln(w, "────────────────────────────")

	regressedCount := 0
	for _, d := range c.Deltas {
		mark, word := "✓", "持平"
		switch d.Verdict {
		case baseline.VerdictRegressed:
			mark, word = "✗", "退步"
			regressedCount++
		case baseline.VerdictImproved:
			mark, word = "↑", "改善"
		}
		fmt.Fprintf(w, "  %s %-14s %s → %s(%+.1f%%,%s)\n",
			mark, metricLabel(d.Name),
			formatMetricValue(d.Name, d.Before), formatMetricValue(d.Name, d.After),
			d.DeltaPct, word)
	}

	if len(c.Warnings) > 0 {
		fmt.Fprintln(w, "\n⚠ 注意")
		for _, warning := range c.Warnings {
			fmt.Fprintf(w, "  - %s\n", warning)
		}
	}

	fmt.Fprintln(w)
	if c.Regressed {
		fmt.Fprintf(w, "  結論:✗ 有 %d 項指標退步——這次改動讓你的服務更不能扛了,建議先查再上。\n", regressedCount)
	} else {
		fmt.Fprintln(w, "  結論:✓ 沒有超出容差的退步,整體持平或更好。")
	}
}

// compareVerdictErr 是守門契約:退步 → 錯誤(CLI 以 exit 1 結束,CI 據此擋合併)。
// strictTrust 開啟時,量測可信度存疑(Warnings 非空)也視同失敗——
// CI 無人讀 stderr 警告,不可信的「通過」是危險的假陽性。
func compareVerdictErr(c baseline.Comparison, strictTrust bool) error {
	if c.Regressed {
		return fmt.Errorf("容量回歸:有指標超出容差退步")
	}
	if strictTrust && len(c.Warnings) > 0 {
		return fmt.Errorf("嚴格信任模式:量測可信度存疑(%d 項警告),視同失敗", len(c.Warnings))
	}
	return nil
}

// currentGitCommit 供 --save-baseline 填 Baseline.GitCommit;非 git 環境回空字串。
// 帶逾時保護:異常 git 環境(網路檔案系統、壞掉的 hook)不可掛住整次壓測。
func currentGitCommit() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func newCompareCmd() *cobra.Command {
	var relPct float64
	var strictTrust bool

	cmd := &cobra.Command{
		Use:   "compare <基準.json> <本次.json>",
		Short: "比較兩份 baseline,回答「這次改動讓你撐的人數變多還是變少」",
		Long: "讀取兩份由 --save-baseline 產生的快照,以雙門檻容差(相對 % + 絕對下限)判定每個指標的" +
			"持平/改善/退步。有任何退步時以 exit code 1 結束,可放進 CI 當容量守門。",
		Args: cobra.ExactArgs(2),
		// 退步是業務結論不是用法錯誤:不印 Usage、錯誤交由 main 統一輸出一次
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			before, err := baseline.Load(args[0])
			if err != nil {
				return err
			}
			after, err := baseline.Load(args[1])
			if err != nil {
				return err
			}

			tol := baseline.DefaultTolerance()
			if relPct > 0 {
				tol.RelPct = relPct
			}
			result, err := baseline.Compare(before, after, tol)
			if err != nil {
				return err
			}
			renderComparison(os.Stdout, result)
			return compareVerdictErr(result, strictTrust)
		},
	}
	cmd.Flags().Float64Var(&relPct, "rel-tolerance-pct", 0,
		"相對容差百分比(預設 10;調低守得更嚴)")
	cmd.Flags().BoolVar(&strictTrust, "strict-trust", false,
		"量測可信度存疑時視同失敗(CI 場景:不可信的通過是假陽性)")
	return cmd
}
