package main

import (
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

func f64(v float64) *float64 { return &v }

// ttft_p95_ms 守門三態:超標失敗、達標通過、
// 設了門檻但場景無 stream 步驟 = 設定錯誤,大聲失敗不可靜默通過。
func TestCheckThresholds_TTFT(t *testing.T) {
	over := metrics.Summary{HasTTFT: true, TTFTP95: 800 * time.Millisecond}
	if msg := checkThresholds(over, &scenarios.Thresholds{TTFTP95Ms: f64(500)}); msg == "" {
		t.Error("TTFT p95 超標應回報失敗")
	}

	ok := metrics.Summary{HasTTFT: true, TTFTP95: 300 * time.Millisecond}
	if msg := checkThresholds(ok, &scenarios.Thresholds{TTFTP95Ms: f64(500)}); msg != "" {
		t.Errorf("TTFT p95 達標不應失敗,得到 %q", msg)
	}

	noStream := metrics.Summary{HasTTFT: false}
	msg := checkThresholds(noStream, &scenarios.Thresholds{TTFTP95Ms: f64(500)})
	if msg == "" {
		t.Fatal("設了 ttft 門檻但無 stream 樣本:靜默通過是危險的假陰性,應回報")
	}
	if !strings.Contains(msg, "stream") {
		t.Errorf("訊息應指出缺 stream 步驟,得到 %q", msg)
	}
}
