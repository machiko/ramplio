package dashboard

import (
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
)

// 轉換必須一項不丟:標籤/格式化與 CLI 同源(baseline.MetricLabel),
// Warnings 與 Regressed 是守門誠實性的一部分,不可在快照層失守。
func TestCompareSnapFromMapsEverything(t *testing.T) {
	cmp := baseline.Comparison{
		Deltas: []baseline.MetricDelta{
			{Name: "p99_ms", Before: 100, After: 250, DeltaPct: 150, Verdict: baseline.VerdictRegressed},
			{Name: "throughput_rps", Before: 1000, After: 1100, DeltaPct: 10, Verdict: baseline.VerdictImproved},
		},
		Regressed: true,
		Warnings:  []string{"場景識別不同"},
	}
	before := baseline.Baseline{
		Scenario:  "https://example.com/",
		GitCommit: "abc1234",
		CreatedAt: time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC),
	}

	snap := CompareSnapFrom(cmp, before)

	if snap.BaselineScenario != "https://example.com/" || snap.BaselineGitCommit != "abc1234" {
		t.Errorf("基準識別遺失: %+v", snap)
	}
	if snap.BaselineCreatedAt != "2026-07-08T10:00:00Z" {
		t.Errorf("CreatedAt 應為 RFC3339,得到 %q", snap.BaselineCreatedAt)
	}
	if !snap.Regressed {
		t.Error("Regressed 不可丟失")
	}
	if len(snap.Warnings) != 1 {
		t.Errorf("Warnings 不可丟失: %+v", snap.Warnings)
	}
	if len(snap.Deltas) != 2 {
		t.Fatalf("Deltas 應有 2 筆,得到 %d", len(snap.Deltas))
	}
	d := snap.Deltas[0]
	if d.Label != "p99（伺服器處理）" {
		t.Errorf("標籤應與 CLI 同源,得到 %q", d.Label)
	}
	if d.BeforeText != "100ms" || d.AfterText != "250ms" {
		t.Errorf("格式化應與 CLI 同源,得到 %q → %q", d.BeforeText, d.AfterText)
	}
	if d.Verdict != "regressed" || snap.Deltas[1].Verdict != "improved" {
		t.Errorf("verdict 對應錯誤: %+v", snap.Deltas)
	}
}

// 零值 CreatedAt 不可輸出成 0001-01-01 這種嚇人的字串。
func TestCompareSnapFromZeroCreatedAt(t *testing.T) {
	snap := CompareSnapFrom(baseline.Comparison{}, baseline.Baseline{})
	if snap.BaselineCreatedAt != "" {
		t.Errorf("零值 CreatedAt 應為空字串,得到 %q", snap.BaselineCreatedAt)
	}
}

func TestBaselineInfoFrom(t *testing.T) {
	b := baseline.Baseline{
		Scenario:  "s",
		GitCommit: "abc",
		CreatedAt: time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC),
		Metrics:   &baseline.MetricsSnapshot{Total: 1},
	}
	info := BaselineInfoFrom(b)
	if info.Scenario != "s" || info.GitCommit != "abc" || !info.HasMetrics || info.HasDiscover {
		t.Errorf("欄位對應錯誤: %+v", info)
	}
	if info.CreatedAt != "2026-07-08T10:00:00Z" {
		t.Errorf("CreatedAt 應為 RFC3339,得到 %q", info.CreatedAt)
	}
}
