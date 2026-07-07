package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v2/internal/discover"
)

// englishLeftovers are phrases from the old English report that must NOT appear
// once the report is fully localised — the capacity-answer positioning depends
// on a consistent plain-Chinese surface.
var englishLeftovers = []string{
	"Capacity Report",
	"Safe limit",
	"Breaking point",
	"What this means",
	"req/sec",
}

func TestWriteDiscoverReport_Default(t *testing.T) {
	var buf bytes.Buffer
	result := discover.DiscoverResult{
		SafeLimit:     200,
		BreakingPoint: 300,
		Probes:        make([]discover.ProbeResult, 9),
	}
	writeDiscoverReport(&buf, result, 2*time.Second)
	out := buf.String()

	for _, want := range []string{
		"容量報告",
		"安全上限",
		"每秒約 200 個請求",
		"臨界點",
		"每秒約 300 個請求",
		"這代表什麼",
		"穩定處理 200 個請求",
		"ramplio verify", // ground-truth trust badge → one-command self-proof
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n--- got ---\n%s", want, out)
		}
	}
	assertNoEnglishLeftovers(t, out)
}

func TestWriteDiscoverReport_Exhausted(t *testing.T) {
	var buf bytes.Buffer
	result := discover.DiscoverResult{
		SafeLimit: 2000,
		Exhausted: true,
		Probes:    make([]discover.ProbeResult, 13),
	}
	writeDiscoverReport(&buf, result, 2*time.Second)
	out := buf.String()

	for _, want := range []string{
		"通過了全部 13 個測試等級",
		"超過每秒 2000 個請求",
		"--max-rps",
		"測試範圍內未觸及",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exhausted report missing %q\n--- got ---\n%s", want, out)
		}
	}
	assertNoEnglishLeftovers(t, out)
}

func TestWriteDiscoverReport_Struggling(t *testing.T) {
	var buf bytes.Buffer
	result := discover.DiscoverResult{
		SafeLimit:     0,
		BreakingPoint: 5,
		Probes:        make([]discover.ProbeResult, 1),
	}
	writeDiscoverReport(&buf, result, 2*time.Second)
	out := buf.String()

	for _, want := range []string{
		"每秒不到 5 個請求",
		"很低的流量下就吃力",
		"檢查伺服器健康狀態",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("struggling report missing %q\n--- got ---\n%s", want, out)
		}
	}
	assertNoEnglishLeftovers(t, out)
}

// TestWriteDiscoverReport_Alignment verifies the box right border stays straight
// when Chinese text (display width 2 per rune) is mixed in: every content row's
// display width must not exceed the inner box width.
func TestWriteDiscoverReport_Alignment(t *testing.T) {
	var buf bytes.Buffer
	result := discover.DiscoverResult{SafeLimit: 200, BreakingPoint: 300, Probes: make([]discover.ProbeResult, 9)}
	writeDiscoverReport(&buf, result, 2*time.Second)

	for _, line := range strings.Split(buf.String(), "\n") {
		// Only inspect content rows (they start with the "  │  " prefix).
		if !strings.HasPrefix(line, "  │  ") {
			continue
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(line, "  │  "), "│")
		if w := displayWidth(inner); w != reportWidth-2 {
			t.Errorf("row display width = %d, want %d\nrow: %q", w, reportWidth-2, line)
		}
	}
}

func TestWriteDiscoverProbe_Chinese(t *testing.T) {
	var buf bytes.Buffer
	writeDiscoverProbe(&buf, discover.ProbeResult{RPS: 200, P99: 42 * time.Millisecond, ErrorRate: 0.1, Status: discover.ProbePass})
	out := buf.String()

	for _, want := range []string{"每秒", "200", "p99=42ms", "錯誤=0.1%"} {
		if !strings.Contains(out, want) {
			t.Errorf("probe line missing %q\n--- got ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "rps") || strings.Contains(out, "errors") {
		t.Errorf("probe line still contains English: %q", out)
	}
}

func assertNoEnglishLeftovers(t *testing.T, out string) {
	t.Helper()
	for _, bad := range englishLeftovers {
		if strings.Contains(out, bad) {
			t.Errorf("report still contains English leftover %q\n--- got ---\n%s", bad, out)
		}
	}
}
