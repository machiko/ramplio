package dashboard

import (
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
)

// CompareSnap 是容量回歸比較結果的 GUI 快照。
// 標籤與格式化來自 baseline 套件(與 CLI 同一來源);
// Warnings 與 Regressed 是守門誠實性的一部分,一項不丟。
type CompareSnap struct {
	BaselineScenario  string         `json:"baseline_scenario,omitempty"`
	BaselineGitCommit string         `json:"baseline_git_commit,omitempty"`
	BaselineCreatedAt string         `json:"baseline_created_at,omitempty"`
	Deltas            []CompareDelta `json:"deltas"`
	Regressed         bool           `json:"regressed"`
	Warnings          []string       `json:"warnings,omitempty"`
}

// CompareDelta 是單一指標的比較快照;Before/After 以格式化字串輸出,
// 讓前端不必自備第二份單位規則。
type CompareDelta struct {
	Name       string  `json:"name"`
	Label      string  `json:"label"`
	BeforeText string  `json:"before_text"`
	AfterText  string  `json:"after_text"`
	DeltaPct   float64 `json:"delta_pct"`
	Verdict    string  `json:"verdict"`
}

// BaselineInfo 是已載入基準的摘要,供 GUI 顯示「跟誰比」。
type BaselineInfo struct {
	Scenario    string `json:"scenario,omitempty"`
	GitCommit   string `json:"git_commit,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	HasMetrics  bool   `json:"has_metrics"`
	HasDiscover bool   `json:"has_discover"`
}

func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// CompareSnapFrom 把比較結果轉為 GUI 快照;before 提供基準識別資訊。
func CompareSnapFrom(c baseline.Comparison, before baseline.Baseline) CompareSnap {
	snap := CompareSnap{
		BaselineScenario:  before.Scenario,
		BaselineGitCommit: before.GitCommit,
		BaselineCreatedAt: rfc3339OrEmpty(before.CreatedAt),
		Regressed:         c.Regressed,
		Warnings:          c.Warnings,
	}
	for _, d := range c.Deltas {
		snap.Deltas = append(snap.Deltas, CompareDelta{
			Name:       d.Name,
			Label:      baseline.MetricLabel(d.Name),
			BeforeText: baseline.FormatMetricValue(d.Name, d.Before),
			AfterText:  baseline.FormatMetricValue(d.Name, d.After),
			DeltaPct:   d.DeltaPct,
			Verdict:    string(d.Verdict),
		})
	}
	return snap
}

// BaselineInfoFrom 摘要一份 baseline 供 GUI 顯示。
func BaselineInfoFrom(b baseline.Baseline) BaselineInfo {
	return BaselineInfo{
		Scenario:    b.Scenario,
		GitCommit:   b.GitCommit,
		CreatedAt:   rfc3339OrEmpty(b.CreatedAt),
		HasMetrics:  b.Metrics != nil,
		HasDiscover: b.Discover != nil,
	}
}
