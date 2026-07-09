package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/protocols"
)

// 單機 headless fallback:go test 環境的 stdout 是 pipe(非 TTY),
// 正好重現 CI/pipeline 情境——修正前 TUI 在非 TTY 下立即退出並 cancel,
// run 提早結束;修正後改走 headless 進度輸出,場景必須跑好跑滿。
func TestRunScenarioHeadless_CompletesWithoutTTY(t *testing.T) {
	if isTerminal() {
		t.Skip("此測試需要非 TTY stdout(go test 正常執行即滿足)")
	}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	yaml := fmt.Sprintf(`name: headless smoke
stages:
  - duration: 2s
    target: 2
steps:
  - name: GET ok
    method: GET
    url: %s/
`, target.URL)
	path := filepath.Join(t.TempDir(), "headless.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("寫入場景: %v", err)
	}

	start := time.Now()
	sum, _, err := runScenario(path, "", protocols.DefaultHTTPConfig(), false)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("runScenario: %v", err)
	}

	// 提早結束的特徵:wall time 遠短於場景時長、請求數趨近於零。
	const minWall = 1800 * time.Millisecond
	if elapsed < minWall {
		t.Errorf("場景應跑滿 2s,實際 %v——非 TTY 下提早結束", elapsed)
	}
	if sum.Total == 0 {
		t.Error("場景跑滿時 Total 不應為 0")
	}
}
