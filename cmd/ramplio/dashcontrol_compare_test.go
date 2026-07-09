package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/protocols"
)

const validBaselineJSON = `{
  "schema_version": 1,
  "scenario": "https://example.com/",
  "git_commit": "abc1234",
  "metrics": {
    "total": 1000, "errors": 0, "error_rate_pct": 0,
    "throughput_rps": 100, "p50_ms": 5, "p90_ms": 8, "p95_ms": 9, "p99_ms": 10
  }
}`

// 已載入基準的 rate run:結果必須附比較快照,基準識別與標籤一項不丟。
func TestDashControllerCompare_AttachesSnapAfterRun(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	info, err := c.LoadBaseline([]byte(validBaselineJSON))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if info.Scenario != "https://example.com/" || !info.HasMetrics {
		t.Errorf("BaselineInfo 對應錯誤: %+v", info)
	}

	if err := c.Start(dashboard.RunRequest{URL: target.URL, RPS: 20, Duration: "3s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("run 結束後 Result 不應為 nil")
	}
	if res.Compare == nil {
		t.Fatal("已載入基準時結果應附比較快照")
	}
	if res.Compare.BaselineScenario != "https://example.com/" || res.Compare.BaselineGitCommit != "abc1234" {
		t.Errorf("基準識別遺失: %+v", res.Compare)
	}
	if len(res.Compare.Deltas) < 4 {
		t.Errorf("metrics 比較應至少有 4 個指標,得到 %d", len(res.Compare.Deltas))
	}
	for _, d := range res.Compare.Deltas {
		if d.Label == "" || d.BeforeText == "" || d.AfterText == "" {
			t.Errorf("delta 呈現欄位不可為空: %+v", d)
		}
	}
	// 基準仍保留:使用者可連跑多次與同一基準比較
	if c.BaselineMeta() == nil {
		t.Error("run 結束後基準應保留供下次比較")
	}
}

// 未載入基準:結果不附比較(缺席即未啟用)。
func TestDashControllerCompare_NoBaselineNoSnap(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.Start(dashboard.RunRequest{URL: target.URL, VUs: 2, Duration: "3s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	if res := c.Result(); res == nil || res.Compare != nil {
		t.Errorf("未載入基準不應附比較快照: %+v", res)
	}
}

// 壞 baseline 必須大聲失敗,且不得覆蓋已載入的合法基準。
func TestDashControllerCompare_RejectsInvalidKeepsExisting(t *testing.T) {
	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if _, err := c.LoadBaseline([]byte(validBaselineJSON)); err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if _, err := c.LoadBaseline([]byte("{not json")); err == nil {
		t.Fatal("壞 JSON 應回傳錯誤")
	}
	if c.BaselineMeta() == nil {
		t.Error("壞上傳不得清掉既有基準")
	}
}

// ClearBaseline 後:BaselineMeta 為 nil,之後的 run 不再比較。
func TestDashControllerCompare_ClearRemovesBaseline(t *testing.T) {
	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if _, err := c.LoadBaseline([]byte(validBaselineJSON)); err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	c.ClearBaseline()
	if c.BaselineMeta() != nil {
		t.Error("清除後 BaselineMeta 應為 nil")
	}
}
