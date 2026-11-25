package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/history"
	"github.com/machiko/ramplio/v3/internal/protocols"
)

const (
	// historyDirEnv overrides where run records are kept (CI, tests).
	historyDirEnv = "RAMPLIO_HISTORY_DIR"
	// defaultHistoryDir lives in the working directory so records follow the project.
	defaultHistoryDir = ".ramplio"
	// historyTimeLayout is how a record's time is shown to the user (local time).
	historyTimeLayout = "2006-01-02 15:04:05"
)

// historyDir is where automatic run records are kept.
func historyDir() string {
	if dir := os.Getenv(historyDirEnv); dir != "" {
		return dir
	}
	return defaultHistoryDir
}

// runLoad is the part of a run's settings that decides whether two runs are
// comparable: 50 VUs and 10 VUs naturally differ in throughput.
type runLoad struct {
	ScenarioFile string
	Method       string
	URL          string
	VUs          int
	RPS          int
	Duration     string
	Headers      map[string]string
	Body         string
}

// runTargetKey identifies "the same target with the same load". The URL is
// masked so the key never stores credentials.
func runTargetKey(l runLoad) string {
	if l.ScenarioFile != "" {
		return "run|scenario|" + filepath.Clean(l.ScenarioFile)
	}
	method := strings.ToUpper(l.Method)
	target := maskURLForDisplay(l.URL)
	req := history.RequestFingerprint(l.Headers, []byte(l.Body))
	if l.RPS > 0 {
		return fmt.Sprintf("run|rps|%s %s|req=%s|rps=%d|d=%s", method, target, req, l.RPS, l.Duration)
	}
	return fmt.Sprintf("run|vus|%s %s|req=%s|vus=%d|d=%s", method, target, req, l.VUs, l.Duration)
}

// discoverTargetKey is runTargetKey for capacity probes (also the smart entry).
// Headers and body only enter the key as a hash, never as plain text.
func discoverTargetKey(req protocols.Request, tolerance time.Duration, maxRPS int, probeDuration time.Duration) string {
	return fmt.Sprintf("discover|%s %s|req=%s|tolerance=%s|max-rps=%d|probe=%s",
		strings.ToUpper(req.Method), maskURLForDisplay(req.URL), history.RequestFingerprint(req.Headers, req.Body),
		tolerance, maxRPS, probeDuration)
}

// recordAndDiff saves rec and prints how it compares with the previous run of
// the same target. It never returns an error: saving and comparing are a
// convenience and must not change the run's result or exit code.
func recordAndDiff(outW, errW io.Writer, dir string, rec history.Record) {
	entries, listErrs, dirErr := history.List(dir)
	if dirErr != nil {
		// The directory itself is unusable (a file, no permission): saving
		// would fail for the same reason, so warn once and stop here.
		fmt.Fprintf(errW, "warning: 無法存執行紀錄（不影響本次結果）: %v\n", dirErr)
		return
	}
	for _, err := range listErrs {
		fmt.Fprintf(errW, "warning: 略過無法讀取的執行紀錄: %v\n", err)
	}

	res, saveErr := history.Save(dir, rec, history.DefaultKeep)
	if saveErr != nil {
		fmt.Fprintf(errW, "warning: 無法存執行紀錄（不影響本次結果）: %v\n", saveErr)
	} else {
		printSaved(outW, errW, dir, res)
	}

	prev, ok := history.PreviousSameTarget(entries, rec.TargetKey, res.Path)
	if !ok {
		if saveErr == nil {
			fmt.Fprintln(outW, "已存為這個目標的第一筆紀錄，下次用同樣設定跑同一個目標會自動比較。")
		}
		return
	}
	c, err := baseline.Compare(prev.Record.Baseline, rec.Baseline, baseline.DefaultTolerance())
	if err != nil {
		fmt.Fprintf(errW, "warning: 無法跟上一次比較: %v\n", err)
		return
	}
	renderAutoDiff(outW, prev.Record.CreatedAt, c)
}

func printSaved(outW, errW io.Writer, dir string, res history.SaveResult) {
	if res.WroteGitignore {
		fmt.Fprintf(outW, "執行紀錄放在 %s/（已在裡面自動放 .gitignore，不會被 commit）。\n", dir)
	}
	fmt.Fprintf(outW, "執行紀錄已存至 %s\n", res.Path)
	if res.PruneErr != nil {
		fmt.Fprintf(errW, "warning: 清理舊執行紀錄失敗: %v\n", res.PruneErr)
	}
}

// renderAutoDiff prints a short plain-language change summary. Unlike
// renderComparison it is informational, so it has no gatekeeping verdict.
func renderAutoDiff(w io.Writer, prevAt time.Time, c baseline.Comparison) {
	fmt.Fprintf(w, "\n跟上一次（%s）比：\n", prevAt.Local().Format(historyTimeLayout))
	for _, d := range c.Deltas {
		mark, word := verdictMarkWord(d.Verdict)
		fmt.Fprintf(w, "  %s %-14s %s → %s（%+.1f%%，%s）\n",
			mark, baseline.MetricLabel(d.Name),
			baseline.FormatMetricValue(d.Name, d.Before), baseline.FormatMetricValue(d.Name, d.After),
			d.DeltaPct, word)
	}
	for _, warning := range c.Warnings {
		fmt.Fprintf(w, "  ⚠ %s\n", warning)
	}
	fmt.Fprintln(w, "  （只是參考，不影響結束碼；要當守門請用 ramplio compare）")
}
