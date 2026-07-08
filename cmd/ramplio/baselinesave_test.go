package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/metrics"
)

// --save-baseline 的失敗處理契約:寫檔問題(權限/磁碟)與「壓測是否達標」無關,
// 只能警告、不可回傳錯誤——否則會污染 exit code、吞掉報告與 threshold 判定。
// helper 不回傳 error 使此契約成為結構保證。
func TestWriteBaselineFile(t *testing.T) {
	b := baseline.FromSummary(metrics.Summary{Total: 1}, "test")

	t.Run("路徑不可寫時警告進 errW,不 panic", func(t *testing.T) {
		var out, errW strings.Builder
		writeBaselineFile(&out, &errW, "/nonexistent-dir/x.json", b)

		if !strings.Contains(errW.String(), "warning") {
			t.Fatalf("stderr 應含 warning,實際: %q", errW.String())
		}
		if out.String() != "" {
			t.Fatalf("失敗時 stdout 不應有成功訊息,實際: %q", out.String())
		}
	})

	t.Run("成功時確認訊息進 outW 且檔案存在", func(t *testing.T) {
		var out, errW strings.Builder
		path := filepath.Join(t.TempDir(), "b.json")
		writeBaselineFile(&out, &errW, path, b)

		if !strings.Contains(out.String(), path) {
			t.Fatalf("stdout 應含儲存路徑,實際: %q", out.String())
		}
		if errW.String() != "" {
			t.Fatalf("成功時不應有警告,實際: %q", errW.String())
		}
		if _, err := baseline.Load(path); err != nil {
			t.Fatalf("存出的檔案應可 Load 回來: %v", err)
		}
	})
}
