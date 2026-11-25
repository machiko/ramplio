package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/history"
	"github.com/spf13/cobra"
)

// 指標標籤與數值格式化已上移到 internal/baseline(labels.go):
// CLI 與 dashboard 卡片共用同一份用語來源,不各自演化同義詞。

// renderComparison 輸出白話比較報告。Warnings 一律完整印出——
// 不可信的比較結果默默通過,是守門工具最危險的假陽性。
func renderComparison(w io.Writer, c baseline.Comparison) {
	fmt.Fprintln(w, "\n容量回歸比較(本次 vs 基準)")
	fmt.Fprintln(w, "────────────────────────────")

	regressedCount := 0
	for _, d := range c.Deltas {
		mark, word := verdictMarkWord(d.Verdict)
		if d.Verdict == baseline.VerdictRegressed {
			regressedCount++
		}
		fmt.Fprintf(w, "  %s %-14s %s → %s(%+.1f%%,%s)\n",
			mark, baseline.MetricLabel(d.Name),
			baseline.FormatMetricValue(d.Name, d.Before), baseline.FormatMetricValue(d.Name, d.After),
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

// verdictMarkWord is the shared symbol and plain word for a metric verdict.
func verdictMarkWord(v baseline.Verdict) (mark, word string) {
	switch v {
	case baseline.VerdictRegressed:
		return "✗", "退步"
	case baseline.VerdictImproved:
		return "↑", "改善"
	default:
		return "✓", "持平"
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
		Use:   "compare [<基準.json> <本次.json>]",
		Short: "比較兩次結果,回答「這次改動讓你撐的人數變多還是變少」",
		Long: "不帶參數時,比較 .ramplio/ 執行紀錄裡最近一筆與同一目標的前一筆;" +
			"帶兩個參數時,讀取兩份由 --save-baseline 產生的快照(或執行紀錄檔)。" +
			"以雙門檻容差(相對 % + 絕對下限)判定每個指標的持平/改善/退步。" +
			"有任何退步時以 exit code 1 結束,可放進 CI 當容量守門。",
		Args: compareArgs,
		// 退步是業務結論不是用法錯誤:不印 Usage、錯誤交由 main 統一輸出一次
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			before, after, err := loadComparePair(cmd.OutOrStdout(), cmd.ErrOrStderr(), args)
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
			renderComparison(cmd.OutOrStdout(), result)
			return compareVerdictErr(result, strictTrust)
		},
	}
	cmd.Flags().Float64Var(&relPct, "rel-tolerance-pct", 0,
		"相對容差百分比(預設 10;調低守得更嚴)")
	cmd.Flags().BoolVar(&strictTrust, "strict-trust", false,
		"量測可信度存疑時視同失敗(CI 場景:不可信的通過是假陽性)")
	return cmd
}

// compareArgs accepts zero arguments (use the run history) or exactly two files.
func compareArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 || len(args) == 2 {
		return nil
	}
	return fmt.Errorf("compare 要 0 個參數（比較最近兩次同目標的執行紀錄）或 2 個參數（<基準.json> <本次.json>），收到 %d 個", len(args))
}

// loadComparePair reads the two files, or with no arguments picks the newest
// record and the previous record of the same target from the run history.
func loadComparePair(outW, errW io.Writer, args []string) (baseline.Baseline, baseline.Baseline, error) {
	if len(args) == 2 {
		before, err := baseline.Load(args[0])
		if err != nil {
			return baseline.Baseline{}, baseline.Baseline{}, err
		}
		after, err := baseline.Load(args[1])
		return before, after, err
	}

	dir := historyDir()
	entries, listErrs := history.List(dir)
	for _, err := range listErrs {
		fmt.Fprintf(errW, "warning: 略過無法讀取的執行紀錄: %v\n", err)
	}
	if len(entries) == 0 {
		return baseline.Baseline{}, baseline.Baseline{}, fmt.Errorf(
			"%s 裡還沒有執行紀錄。先對同一個目標跑兩次，例如：ramplio run --url https://example.com（每次跑完會自動留存）", dir)
	}
	latest := entries[0]
	prev, ok := history.PreviousSameTarget(entries, latest.Record.TargetKey, latest.Path)
	if !ok {
		return baseline.Baseline{}, baseline.Baseline{}, fmt.Errorf(
			"最近一筆紀錄（%s）還沒有同目標、同設定的前一筆可比。用同樣的參數再跑一次，例如：ramplio run --url https://example.com；"+
				"或直接指定兩個檔：ramplio compare <基準.json> <本次.json>", latest.Record.Scenario)
	}
	fmt.Fprintf(outW, "比較執行紀錄：%s → %s（目標 %s）\n",
		prev.Record.CreatedAt.Local().Format(historyTimeLayout),
		latest.Record.CreatedAt.Local().Format(historyTimeLayout),
		latest.Record.Scenario)
	return prev.Record.Baseline, latest.Record.Baseline, nil
}
