package main

import (
	"testing"

	"github.com/machiko/ramplio/v3/internal/scenarios"
)

// ws_mode 必須從 YAML 一路傳到 engine——轉換層漏傳欄位時,
// persistent 會靜默退化為 per_request,測試全綠卻沒有任何效果。
func TestScenarioStepsToRamp_CarriesWSMode(t *testing.T) {
	steps := scenarioStepsToRamp([]scenarios.Step{{
		Name:     "ws",
		Method:   "GET",
		URL:      "ws://localhost/echo",
		Protocol: "websocket",
		WSMode:   "persistent",
	}})
	if len(steps) != 1 {
		t.Fatalf("應轉出 1 步,得到 %d", len(steps))
	}
	if steps[0].WSMode != "persistent" {
		t.Errorf("WSMode 遺失:得到 %q", steps[0].WSMode)
	}
}

func TestScenarioStepsToRamp_CarriesStream(t *testing.T) {
	steps := scenarioStepsToRamp([]scenarios.Step{{
		Name: "sse", Method: "POST", URL: "https://x/chat", Stream: "sse",
	}})
	if steps[0].Stream != "sse" {
		t.Errorf("Stream 遺失:得到 %q", steps[0].Stream)
	}
}
