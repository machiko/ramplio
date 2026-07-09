package dashboard

import (
	"time"

	"github.com/machiko/ramplio/v3/internal/observe"
)

// ObserveSnap 是 --observe 瓶頸關聯結果的 GUI 快照。
// 語意與 CLI 白話同源:三態(ok/insufficient/no_culprit)、Reason 保留註記、
// 排除名單與截斷警示一項不丟——誠實資訊不可在呈現層失守。
type ObserveSnap struct {
	Status      string               `json:"status"`
	Reason      string               `json:"reason,omitempty"`
	Truncated   bool                 `json:"truncated,omitempty"`
	ExcludedOps []string             `json:"excluded_ops,omitempty"`
	Top         []ObserveDegradation `json:"top,omitempty"`
}

// ObserveDegradation 是單一 operation 的退化摘要(延遲以毫秒浮點數表示)。
type ObserveDegradation struct {
	Operation     string  `json:"operation"`
	BaselineP95Ms float64 `json:"baseline_p95_ms"`
	StressedP95Ms float64 `json:"stressed_p95_ms"`
	Factor        float64 `json:"factor"`
}

// ObserveSnapFrom 把分析結果轉為 GUI 快照。
func ObserveSnapFrom(a observe.Analysis, truncated bool) ObserveSnap {
	ms := float64(time.Millisecond)
	snap := ObserveSnap{
		Status:      string(a.Status),
		Reason:      a.Reason,
		Truncated:   truncated,
		ExcludedOps: a.ExcludedOps,
	}
	for _, d := range a.Top {
		snap.Top = append(snap.Top, ObserveDegradation{
			Operation:     d.Operation,
			BaselineP95Ms: float64(d.BaselineP95) / ms,
			StressedP95Ms: float64(d.StressedP95) / ms,
			Factor:        d.Factor,
		})
	}
	return snap
}
