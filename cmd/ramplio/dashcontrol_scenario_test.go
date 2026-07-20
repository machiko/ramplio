package main

import (
	"testing"

	"github.com/machiko/ramplio/v3/internal/protocols"
)

// td-2 審查前發現:LoadScenario 曾有一份與 scenarioStepsToRamp 漂移的
// 轉換實作,WS 欄位(ws_message/ws_expect)經 GUI 上傳會靜默遺失。
// 收斂為單一來源後,此測試釘住兩條路徑不可再分歧。
func TestLoadScenarioCarriesWSFields(t *testing.T) {
	yaml := []byte(`
name: ws via dashboard
stages:
  - duration: 10s
    target: 2
steps:
  - name: ws echo
    method: GET
    url: ws://localhost:8080/echo
    protocol: websocket
    ws_message: ping
    ws_expect: pong
`)
	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.LoadScenario(yaml, ""); err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.pendingSteps) != 1 {
		t.Fatalf("應載入 1 步,得到 %d", len(c.pendingSteps))
	}
	step := c.pendingSteps[0]
	if got := step.Request.Headers["X-WS-Expect"]; got != "pong" {
		t.Errorf("ws_expect 應注入 X-WS-Expect header,得到 %q", got)
	}
	if got := string(step.Request.Body); got != "ping" {
		t.Errorf("ws_message 應成為請求 body,得到 %q", got)
	}
}

// LoadScenarioWithData 把記憶體 CSV 解析成資料列並存入,mode 取自 YAML 的
// vars_from——瀏覽器產生的場景可直接開跑,資料檔不落磁碟。
func TestLoadScenarioWithData_InMemoryRows(t *testing.T) {
	yaml := []byte(`
name: gen via dashboard
vars_from:
  file: data.csv
  mode: random
stages:
  - duration: 10s
    target: 2
steps:
  - name: get user
    method: GET
    url: https://example.com/users/{{data.user_id}}
`)
	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.LoadScenarioWithData(yaml, "user_id\n1\n2\n3\n"); err != nil {
		t.Fatalf("LoadScenarioWithData: %v", err)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.pendingDataRows) != 3 {
		t.Fatalf("應載入 3 筆資料列,得到 %d", len(c.pendingDataRows))
	}
	if got := c.pendingDataRows[0]["user_id"]; got != "1" {
		t.Errorf("第一列 user_id 應為 1,得到 %q", got)
	}
	if c.pendingDataMode != "random" {
		t.Errorf("dataMode 應取自 YAML 為 random,得到 %q", c.pendingDataMode)
	}
}

// YAML 宣告了 vars_from 卻沒帶對應資料(例如 cookie 場景的 sessions.csv),
// 必須大聲失敗——否則載入成功後每個 request 都會模板解析失敗。
func TestLoadScenarioWithData_DeclaredButMissingDataErrors(t *testing.T) {
	yaml := []byte(`
name: cookie needs sessions
vars_from:
  file: sessions.csv
  mode: sequential
stages:
  - duration: 10s
    target: 2
steps:
  - name: dashboard
    method: GET
    url: https://example.com/dashboard
`)
	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.LoadScenarioWithData(yaml, ""); err == nil {
		t.Fatal("宣告 vars_from 卻無資料時應回錯,卻成功了")
	}
}

// 無變動參數時,空 CSV 不應產生資料列,場景仍可載入。
func TestLoadScenarioWithData_NoData(t *testing.T) {
	yaml := []byte(`
name: no data
stages:
  - duration: 10s
    target: 2
steps:
  - name: home
    method: GET
    url: https://example.com/
`)
	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.LoadScenarioWithData(yaml, ""); err != nil {
		t.Fatalf("LoadScenarioWithData: %v", err)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.pendingDataRows) != 0 {
		t.Errorf("無資料檔時 pendingDataRows 應為空,得到 %d", len(c.pendingDataRows))
	}
}
