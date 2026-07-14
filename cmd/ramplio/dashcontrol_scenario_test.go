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
