package observe

import (
	"strings"
	"testing"
	"time"
)

// spansOf 產生 n 個指定 operation 與延遲的 span(帶少量遞增抖動避免完全相同)。
func spansOf(op string, n int, base time.Duration) []Span {
	out := make([]Span, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Span{
			Operation: op,
			Duration:  base + time.Duration(i)*time.Microsecond,
		})
	}
	return out
}

func merge(groups ...[]Span) []Span {
	var all []Span
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}

// Ground-truth 自證(單元版):注入已知瓶頸,分析必須指向它。
// 這是本 Phase 公信力的底線——歸因準不準是可驗證的,不是猜的。
func TestAnalyzeIdentifiesInjectedCulprit(t *testing.T) {
	baseline := merge(
		spansOf("SELECT orders", 30, 10*time.Millisecond),
		spansOf("GET /api/users", 30, 12*time.Millisecond),
		spansOf("redis GET", 30, 2*time.Millisecond),
	)
	// 臨界窗:SELECT orders 惡化 8 倍,其餘輕微變慢
	stressed := merge(
		spansOf("SELECT orders", 30, 80*time.Millisecond),
		spansOf("GET /api/users", 30, 14*time.Millisecond),
		spansOf("redis GET", 30, 3*time.Millisecond),
	)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	if a.Status != StatusOK {
		t.Fatalf("狀態應為 ok,得到 %s(%s)", a.Status, a.Reason)
	}
	if len(a.Top) == 0 || a.Top[0].Operation != "SELECT orders" {
		t.Fatalf("Top1 必須指向注入的瓶頸 SELECT orders,得到 %+v", a.Top)
	}
	if a.Top[0].Factor < 6 || a.Top[0].Factor > 10 {
		t.Fatalf("退化倍率應約 8x,得到 %.1f", a.Top[0].Factor)
	}
}

// 誠實原則的型別化:樣本不足時回傳 insufficient 狀態,絕不硬給答案。
func TestAnalyzeInsufficientSamples(t *testing.T) {
	baseline := spansOf("A", 3, 10*time.Millisecond) // 遠低於 MinSamplesPerOp
	stressed := spansOf("A", 3, 80*time.Millisecond)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	if a.Status != StatusInsufficient {
		t.Fatalf("樣本不足應回 insufficient,得到 %s", a.Status)
	}
	if len(a.Top) != 0 {
		t.Fatalf("insufficient 時不可附任何歸因結果(硬給答案),得到 %+v", a.Top)
	}
	if a.Reason == "" {
		t.Fatal("insufficient 必須附白話原因")
	}
}

func TestAnalyzeEmptyWindows(t *testing.T) {
	a := AnalyzeWindows(nil, nil, DefaultAnalyzeConfig())
	if a.Status != StatusInsufficient {
		t.Fatalf("兩窗皆空應回 insufficient,得到 %s", a.Status)
	}
}

// 全面等幅變慢 = 系統性飽和,沒有單點瓶頸——說「找不到特定瓶頸」
// 比硬指一個最慢的 operation 誠實。
func TestAnalyzeUniformSlowdownNoCulprit(t *testing.T) {
	baseline := merge(
		spansOf("A", 30, 10*time.Millisecond),
		spansOf("B", 30, 20*time.Millisecond),
		spansOf("C", 30, 5*time.Millisecond),
	)
	stressed := merge(
		spansOf("A", 30, 30*time.Millisecond), // 全部 3x
		spansOf("B", 30, 60*time.Millisecond),
		spansOf("C", 30, 15*time.Millisecond),
	)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	if a.Status != StatusNoCulprit {
		t.Fatalf("等幅變慢應回 no_culprit,得到 %s(top=%+v)", a.Status, a.Top)
	}
	if a.Reason == "" {
		t.Fatal("no_culprit 必須附白話說明(整體變慢、疑似資源飽和)")
	}
}

// 只出現在單一窗的 operation 無從比較,應被跳過而非讓分析崩潰。
func TestAnalyzeSkipsOneSidedOperations(t *testing.T) {
	baseline := merge(
		spansOf("A", 30, 10*time.Millisecond),
		spansOf("only-baseline", 30, 10*time.Millisecond),
	)
	stressed := merge(
		spansOf("A", 30, 80*time.Millisecond),
		spansOf("only-stressed", 30, 5*time.Millisecond),
	)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	if a.Status != StatusOK {
		t.Fatalf("狀態應為 ok,得到 %s(%s)", a.Status, a.Reason)
	}
	for _, d := range a.Top {
		if d.Operation == "only-baseline" || d.Operation == "only-stressed" {
			t.Fatalf("單邊 operation 不可進入比較結果: %+v", d)
		}
	}
}

// 基準延遲趨近零時不可除零爆表;倍率計算需有下限保護。
func TestAnalyzeZeroBaselineGuard(t *testing.T) {
	baseline := merge(
		spansOf("instant", 30, 0),
		spansOf("normal", 30, 10*time.Millisecond),
	)
	stressed := merge(
		spansOf("instant", 30, 5*time.Millisecond),
		spansOf("normal", 30, 12*time.Millisecond),
	)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	for _, d := range a.Top {
		if d.Factor > 1000 {
			t.Fatalf("零基準的倍率未受下限保護,爆表為 %.0f: %+v", d.Factor, d)
		}
	}
}

// ---- 對抗案例(審查關構造,防錯誤歸因)----

// CRITICAL 防護:樣本不足的真實瓶頸被排除時,排除必須可見——
// 否則 no_culprit 會變成「已窮盡搜尋」的假斷言。
func TestAnalyzeExcludedOpsVisible(t *testing.T) {
	baseline := merge(
		spansOf("high-traffic", 30, 10*time.Millisecond),
		spansOf("low-traffic-real-bottleneck", 5, 10*time.Millisecond), // 低於門檻
	)
	stressed := merge(
		spansOf("high-traffic", 30, 11*time.Millisecond),                // 幾乎沒變慢
		spansOf("low-traffic-real-bottleneck", 5, 500*time.Millisecond), // 50x 退化但樣本不足
	)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	if a.Status != StatusNoCulprit {
		t.Fatalf("高流量 op 無退化,狀態應為 no_culprit,得到 %s", a.Status)
	}
	found := false
	for _, op := range a.ExcludedOps {
		if op == "low-traffic-real-bottleneck" {
			found = true
		}
	}
	if !found {
		t.Fatalf("被排除的 op 必須列在 ExcludedOps,得到 %v", a.ExcludedOps)
	}
	if !strings.Contains(a.Reason, "未納入") {
		t.Fatalf("Reason 必須揭露有 op 未納入比較(可能遺漏真實瓶頸),實際: %s", a.Reason)
	}
}

// HIGH 防護:預設樣本門檻必須讓 p95 有統計意義(n<20 時 p95 退化為 max,
// 單一離群 trace 就能偽造出「單點瓶頸」)。
func TestAnalyzeDefaultMinSamplesMakesP95Meaningful(t *testing.T) {
	if DefaultAnalyzeConfig().MinSamplesPerOp < 20 {
		t.Fatalf("MinSamplesPerOp 預設 %d < 20:p95 索引會退化為 max,離群值可偽造瓶頸",
			DefaultAnalyzeConfig().MinSamplesPerOp)
	}
	// n=20 時單一離群值不可讓 p95 爆表(p95 = 第 19 大,非 max)
	base := spansOf("op", 20, 10*time.Millisecond)
	stress := spansOf("op", 19, 10*time.Millisecond)
	stress = append(stress, Span{Operation: "op", Duration: 500 * time.Millisecond}) // 1 離群

	a := AnalyzeWindows(base, stress, DefaultAnalyzeConfig())
	if a.Status == StatusOK {
		t.Fatalf("單一離群 trace 不應被判為單點瓶頸: %+v", a.Top)
	}
}

// HIGH 防護:可比較 op 過少時,統計把握度低必須在 Reason 揭露。
func TestAnalyzeFewOpsCautionDisclosed(t *testing.T) {
	baseline := merge(
		spansOf("A", 30, 10*time.Millisecond),
		spansOf("B", 30, 10*time.Millisecond),
	)
	stressed := merge(
		spansOf("A", 30, 80*time.Millisecond),
		spansOf("B", 30, 11*time.Millisecond),
	)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	if a.Status != StatusOK {
		t.Fatalf("明確的 8x 單點退化應為 ok,得到 %s(%s)", a.Status, a.Reason)
	}
	if !strings.Contains(a.Reason, "把握度") {
		t.Fatalf("僅 2 個可比較 op 時 Reason 應揭露統計把握度較低,實際: %q", a.Reason)
	}
}

// spansConst 產生無抖動的固定延遲 span(邊界測試需要精確倍率)。
func spansConst(op string, n int, d time.Duration) []Span {
	out := make([]Span, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Span{Operation: op, Duration: d})
	}
	return out
}

// 門檻邊界語意:恰好等於 CulpritFactor 視為達標(≥)。
func TestAnalyzeCulpritFactorBoundary(t *testing.T) {
	baseline := merge(
		spansConst("A", 30, 10*time.Millisecond),
		spansConst("B", 30, 10*time.Millisecond),
		spansConst("C", 30, 10*time.Millisecond),
	)
	stressed := merge(
		spansConst("A", 30, 20*time.Millisecond), // 恰好 2.0x
		spansConst("B", 30, 10*time.Millisecond),
		spansConst("C", 30, 10*time.Millisecond),
	)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	if a.Status != StatusOK {
		t.Fatalf("恰好 2.0x 且遠高於其餘中位數,應判 ok,得到 %s(%s)", a.Status, a.Reason)
	}
}

func TestAnalyzeResultsSortedByFactor(t *testing.T) {
	baseline := merge(
		spansOf("worst", 30, 10*time.Millisecond),
		spansOf("bad", 30, 10*time.Millisecond),
		spansOf("fine", 30, 10*time.Millisecond),
	)
	stressed := merge(
		spansOf("worst", 30, 100*time.Millisecond),
		spansOf("bad", 30, 40*time.Millisecond),
		spansOf("fine", 30, 11*time.Millisecond),
	)

	a := AnalyzeWindows(baseline, stressed, DefaultAnalyzeConfig())
	if a.Status != StatusOK {
		t.Fatalf("狀態應為 ok,得到 %s", a.Status)
	}
	if len(a.Top) < 2 || a.Top[0].Operation != "worst" || a.Top[1].Operation != "bad" {
		t.Fatalf("結果應依退化倍率降冪排序,得到 %+v", a.Top)
	}
}
