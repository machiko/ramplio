package main

import (
	"fmt"
	"io"

	"github.com/machiko/ramplio/v2/internal/baseline"
)

// writeBaselineFile 儲存 baseline 快照。失敗只警告、不回傳錯誤(結構保證):
// 寫檔問題(權限/磁碟)與「壓測是否達標」無關,不可污染 exit code
// 或吞掉後面的報告與 threshold 判定(比照 outputFile 慣例)。
func writeBaselineFile(outW, errW io.Writer, path string, b baseline.Baseline) {
	if err := baseline.Save(path, b); err != nil {
		fmt.Fprintf(errW, "warning: could not save baseline: %v\n", err)
		return
	}
	fmt.Fprintf(outW, "Baseline 已存至 %s(之後用 ramplio compare 比較)\n", path)
}
