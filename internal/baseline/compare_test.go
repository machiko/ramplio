package baseline

import (
	"strings"
	"testing"
)

func metricsBaseline(p99Ms, errPct, rps float64) Baseline {
	return Baseline{
		SchemaVersion: SchemaVersion,
		Scenario:      "s",
		Metrics: &MetricsSnapshot{
			Total: 1000, ErrorRatePct: errPct, ThroughputRPS: rps,
			P50Ms: p99Ms / 2, P99Ms: p99Ms,
		},
	}
}

func discoverBaseline(safeRPS int) Baseline {
	return Baseline{
		SchemaVersion: SchemaVersion,
		Scenario:      "s",
		Discover:      &DiscoverSnapshot{SafeLimitRPS: safeRPS, BreakingPointRPS: safeRPS + 50},
	}
}

// 容差哲學:相對容差(預設 10%)與絕對下限雙門檻,兩者皆超才判退步。
// 壓測數字天生有變異,守門寧鬆勿誤報——誤報會讓團隊把守門關掉。
func TestCompareToleranceJudgement(t *testing.T) {
	tests := []struct {
		name   string
		before Baseline
		after  Baseline
		want   Verdict // 針對 p99 的判定
	}{
		{"劣化在相對容差內判持平", metricsBaseline(100, 0, 200), metricsBaseline(108, 0, 200), VerdictStable},
		{"劣化同時超過相對與絕對門檻判退步", metricsBaseline(100, 0, 200), metricsBaseline(150, 0, 200), VerdictRegressed},
		{"小延遲的大百分比但小絕對量判持平", metricsBaseline(2, 0, 200), metricsBaseline(3, 0, 200), VerdictStable},
		{"顯著改善判進步", metricsBaseline(100, 0, 200), metricsBaseline(60, 0, 200), VerdictImproved},
		{"改善在容差內判持平", metricsBaseline(100, 0, 200), metricsBaseline(95, 0, 200), VerdictStable},
		// ---- 高基準值行為(刻意設計,非疏漏)----
		// 大基準值時由相對容差主導判定:±10% 是壓測的常見執行間變異,
		// 500→549(9.8%)judged stable 是「寧鬆勿誤報」的本意;
		// 絕對下限的職責只在小基準值擋掉相對容差的過度敏感,兩者分工。
		// 若要更嚴的守門,呼叫端可自訂 Tolerance.RelPct。
		{"高基準值劣化在相對容差內判持平(刻意設計)", metricsBaseline(500, 0, 200), metricsBaseline(549, 0, 200), VerdictStable},
		{"高基準值劣化超過相對容差判退步", metricsBaseline(500, 0, 200), metricsBaseline(560, 0, 200), VerdictRegressed},
		// ---- 臨界值語意:「超過」門檻採嚴格大於,恰好等於視為容差內 ----
		{"恰好等於絕對下限判持平", metricsBaseline(20, 0, 200), metricsBaseline(25, 0, 200), VerdictStable},   // diff=5ms=floor、25%>10%
		{"恰好等於相對容差判持平", metricsBaseline(100, 0, 200), metricsBaseline(110, 0, 200), VerdictStable}, // 10%=RelPct、10ms>floor
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp, err := Compare(tt.before, tt.after, DefaultTolerance())
			if err != nil {
				t.Fatalf("Compare: %v", err)
			}
			d := findDelta(t, cmp, "p99_ms")
			if d.Verdict != tt.want {
				t.Fatalf("p99 verdict = %s, want %s(before=%v after=%v)", d.Verdict, tt.want, d.Before, d.After)
			}
		})
	}
}

func TestCompareRegressionFlagsAndDirection(t *testing.T) {
	// 錯誤率上升與吞吐下降都是退步(方向相反的兩種指標)
	before := metricsBaseline(100, 0.1, 200)
	after := metricsBaseline(100, 5.0, 120)

	cmp, err := Compare(before, after, DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !cmp.Regressed {
		t.Fatal("錯誤率暴增+吞吐大跌,整體必須判退步")
	}
	if d := findDelta(t, cmp, "error_rate_pct"); d.Verdict != VerdictRegressed {
		t.Fatalf("error_rate verdict = %s, want regressed", d.Verdict)
	}
	if d := findDelta(t, cmp, "throughput_rps"); d.Verdict != VerdictRegressed {
		t.Fatalf("throughput verdict = %s, want regressed", d.Verdict)
	}
}

func TestCompareDiscoverCapacity(t *testing.T) {
	cmp, err := Compare(discoverBaseline(1000), discoverBaseline(700), DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	d := findDelta(t, cmp, "safe_limit_rps")
	if d.Verdict != VerdictRegressed {
		t.Fatalf("容量 1000→700 應判退步,得到 %s", d.Verdict)
	}
	if !cmp.Regressed {
		t.Fatal("容量退步時整體 Regressed 應為 true")
	}
}

func TestCompareZeroBeforeDoesNotDivideByZero(t *testing.T) {
	// before 錯誤率 0 → 相對變化無意義,只看絕對門檻
	before := metricsBaseline(100, 0, 200)
	small := metricsBaseline(100, 0.2, 200) // +0.2 個百分點,絕對門檻內
	big := metricsBaseline(100, 3.0, 200)   // +3 個百分點,超過絕對門檻

	cmpSmall, err := Compare(before, small, DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if d := findDelta(t, cmpSmall, "error_rate_pct"); d.Verdict != VerdictStable {
		t.Fatalf("0→0.2%% 應判持平(絕對門檻內),得到 %s", d.Verdict)
	}
	cmpBig, err := Compare(before, big, DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if d := findDelta(t, cmpBig, "error_rate_pct"); d.Verdict != VerdictRegressed {
		t.Fatalf("0→3%% 應判退步,得到 %s", d.Verdict)
	}
}

func TestCompareNoCommonSectionIsError(t *testing.T) {
	_, err := Compare(metricsBaseline(100, 0, 200), discoverBaseline(1000), DefaultTolerance())
	if err == nil {
		t.Fatal("兩份 baseline 無共同區段(metrics vs discover)不可比較,應回傳錯誤")
	}
}

func TestCompareScenarioMismatchWarns(t *testing.T) {
	a := metricsBaseline(100, 0, 200)
	b := metricsBaseline(100, 0, 200)
	b.Scenario = "different"

	cmp, err := Compare(a, b, DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !hasWarningContaining(cmp, "場景") {
		t.Fatalf("場景識別不同應產生警告,warnings=%v", cmp.Warnings)
	}
}

func TestCompareUntrustworthyBaselineWarns(t *testing.T) {
	a := metricsBaseline(100, 0, 200)
	b := metricsBaseline(100, 0, 200)
	b.Metrics.DroppedSamples = 500
	b.Metrics.GeneratorWorkerCapHit = true

	cmp, err := Compare(a, b, DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	// 不可信的量測不該默默拿來比較——把量測劣化誤判成系統退化會傷公信力
	if !hasWarningContaining(cmp, "可信") {
		t.Fatalf("量測可信度存疑應產生警告,warnings=%v", cmp.Warnings)
	}
}

func TestCompareDeltaPctComputation(t *testing.T) {
	cmp, err := Compare(metricsBaseline(100, 0, 200), metricsBaseline(150, 0, 200), DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if d := findDelta(t, cmp, "p99_ms"); d.DeltaPct != 50 {
		t.Fatalf("DeltaPct = %v, want 50(100→150)", d.DeltaPct)
	}
	// before=0 時相對變化無意義,固定為 0
	cmp2, err := Compare(metricsBaseline(100, 0, 200), metricsBaseline(100, 2, 200), DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if d := findDelta(t, cmp2, "error_rate_pct"); d.DeltaPct != 0 {
		t.Fatalf("before=0 時 DeltaPct 應為 0,得到 %v", d.DeltaPct)
	}
}

func TestCompareCorrectedP99OnlyWhenBothHave(t *testing.T) {
	withCorrected := metricsBaseline(100, 0, 200)
	withCorrected.Metrics.HasCorrected = true
	withCorrected.Metrics.CorrectedP99Ms = 130
	withoutCorrected := metricsBaseline(100, 0, 200)

	// 單邊有 corrected(rate 模式 vs VU 模式的 baseline 互比)→ 該指標跳過
	cmp, err := Compare(withCorrected, withoutCorrected, DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	for _, d := range cmp.Deltas {
		if d.Name == "corrected_p99_ms" {
			t.Fatal("單邊缺 HasCorrected 時不應比較 corrected_p99_ms")
		}
	}
	// 雙邊都有 → 納入比較
	both := metricsBaseline(100, 0, 200)
	both.Metrics.HasCorrected = true
	both.Metrics.CorrectedP99Ms = 135
	cmp2, err := Compare(withCorrected, both, DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	findDelta(t, cmp2, "corrected_p99_ms")
}

func TestCompareWarningsDoNotAffectRegressed(t *testing.T) {
	a := metricsBaseline(100, 0, 200)
	b := metricsBaseline(100, 0, 200)
	b.Scenario = "different"
	b.Metrics.DroppedSamples = 500

	cmp, err := Compare(a, b, DefaultTolerance())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(cmp.Warnings) == 0 {
		t.Fatal("應有警告")
	}
	if cmp.Regressed {
		t.Fatal("Warnings 不改變判定:指標全持平時 Regressed 必須為 false")
	}
}

func findDelta(t *testing.T, c Comparison, name string) MetricDelta {
	t.Helper()
	for _, d := range c.Deltas {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("找不到指標 %q,deltas=%+v", name, c.Deltas)
	return MetricDelta{}
}

func hasWarningContaining(c Comparison, substr string) bool {
	for _, w := range c.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
