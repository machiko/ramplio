package dashboard

import (
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/observe"
)

// GUI 的 observe 卡片與 CLI 白話同源:三態、排除名單、截斷警示
// 一項都不能在轉換層丟失——排除可見化在呈現層失守的教訓(p3-3)不重演。
func TestObserveSnapFrom(t *testing.T) {
	a := observe.Analysis{
		Status: observe.StatusOK,
		Reason: "可比較的 operation 僅 2 個,單點瓶頸判定的統計把握度較低。",
		Top: []observe.Degradation{{
			Operation:   "SELECT orders",
			BaselineP95: 10 * time.Millisecond,
			StressedP95: 90 * time.Millisecond,
			Factor:      9.0,
		}},
		ExcludedOps: []string{"low-traffic-op"},
	}

	snap := ObserveSnapFrom(a, true)

	if snap.Status != "ok" {
		t.Fatalf("Status = %q, want ok", snap.Status)
	}
	if !snap.Truncated {
		t.Fatal("截斷旗標不可丟失")
	}
	if snap.Reason == "" {
		t.Fatal("Reason(含把握度註記)不可丟失")
	}
	if len(snap.ExcludedOps) != 1 || snap.ExcludedOps[0] != "low-traffic-op" {
		t.Fatalf("排除名單不可丟失: %v", snap.ExcludedOps)
	}
	if len(snap.Top) != 1 {
		t.Fatalf("Top 應有 1 筆: %+v", snap.Top)
	}
	d := snap.Top[0]
	if d.Operation != "SELECT orders" || d.BaselineP95Ms != 10 || d.StressedP95Ms != 90 || d.Factor != 9.0 {
		t.Fatalf("退化欄位轉換錯誤: %+v", d)
	}
}

func TestObserveSnapFromInsufficient(t *testing.T) {
	a := observe.Analysis{
		Status: observe.StatusInsufficient,
		Reason: "沒有任何 operation 在兩個時間窗都達到 20 筆樣本。",
	}
	snap := ObserveSnapFrom(a, false)
	if snap.Status != "insufficient" || len(snap.Top) != 0 {
		t.Fatalf("insufficient 轉換錯誤: %+v", snap)
	}
}
