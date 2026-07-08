package baseline

import (
	"fmt"
	"math"
)

// Verdict 是單一指標的比較判定。
type Verdict string

const (
	VerdictImproved  Verdict = "improved"
	VerdictStable    Verdict = "stable"
	VerdictRegressed Verdict = "regressed"
)

// Tolerance 是回歸判定的雙門檻:相對容差與絕對下限**同時**超過才改變判定。
// 壓測數字天生有變異,守門寧鬆勿誤報——常態誤報會讓團隊把守門關掉。
//
// 雙門檻的分工(刻意設計):
//   - 絕對下限:小基準值時擋掉相對容差的過度敏感(2ms→3ms 是 +50% 但只是雜訊)
//   - 相對容差:大基準值時主導判定(500ms→549ms 的 9.8% 在常見執行間變異內)
//
// 因此大基準值下絕對下限「不生效」是預期行為,不是漏洞;
// 要更嚴的守門請調低 RelPct,而非期待絕對下限攔截。
// 假設所有指標值皆為非負(延遲/錯誤率/吞吐的天然域);負值輸入行為未定義。
type Tolerance struct {
	RelPct             float64 // 相對容差(%),對所有指標生效
	LatencyFloorMs     float64 // 延遲類指標的絕對下限(ms)
	ErrorFloorPct      float64 // 錯誤率的絕對下限(百分點)
	ThroughputFloorRPS float64 // 吞吐/容量類指標的絕對下限(RPS)
}

// DefaultTolerance 是經驗預設值;之後可由 CLI 旗標覆寫。
func DefaultTolerance() Tolerance {
	return Tolerance{
		RelPct:             10,
		LatencyFloorMs:     5,
		ErrorFloorPct:      1.0,
		ThroughputFloorRPS: 5,
	}
}

// MetricDelta 是單一指標的比較結果。
type MetricDelta struct {
	Name   string  `json:"name"`
	Before float64 `json:"before"`
	After  float64 `json:"after"`
	// DeltaPct 為相對變化(%);Before 為 0 時相對變化無意義,固定為 0。
	DeltaPct float64 `json:"delta_pct"`
	Verdict  Verdict `json:"verdict"`
}

// Comparison 是兩份 baseline 的完整比較結果。
type Comparison struct {
	Deltas []MetricDelta `json:"deltas"`
	// Regressed 為 true 表示至少一個指標判退步(守門 exit code 的依據)。
	Regressed bool `json:"regressed"`
	// Warnings 是不改變判定、但使用者必須知道的注意事項
	// (場景不一致、量測可信度存疑等)。
	Warnings []string `json:"warnings,omitempty"`
}

// judge 依雙門檻判定單一指標:相對與絕對皆超過容差才離開 stable;
// Before 為 0 時相對變化無意義,只看絕對門檻。
func judge(before, after float64, higherIsBetter bool, relPct, absFloor float64) Verdict {
	diff := after - before
	absOver := math.Abs(diff) > absFloor
	relOver := true
	if before != 0 {
		relOver = math.Abs(diff)/math.Abs(before)*100 > relPct
	}
	if !absOver || !relOver {
		return VerdictStable
	}
	isWorse := diff > 0
	if higherIsBetter {
		isWorse = diff < 0
	}
	if isWorse {
		return VerdictRegressed
	}
	return VerdictImproved
}

func newDelta(name string, before, after float64, higherIsBetter bool, relPct, absFloor float64) MetricDelta {
	deltaPct := 0.0
	if before != 0 {
		deltaPct = (after - before) / math.Abs(before) * 100
	}
	return MetricDelta{
		Name:     name,
		Before:   before,
		After:    after,
		DeltaPct: deltaPct,
		Verdict:  judge(before, after, higherIsBetter, relPct, absFloor),
	}
}

// trustWarnings 檢查單側 baseline 的量測可信度;不可信的基準拿來比較,
// 會把「量測劣化」誤判成「目標系統退化」,必須讓使用者知道。
func trustWarnings(label string, b Baseline) []string {
	if b.Metrics == nil {
		return nil
	}
	var ws []string
	if b.Metrics.DroppedSamples > 0 {
		ws = append(ws, fmt.Sprintf("%s的量測可信度存疑:收集器丟棄了 %d 筆樣本", label, b.Metrics.DroppedSamples))
	}
	if b.Metrics.GeneratorWorkerCapHit {
		ws = append(ws, fmt.Sprintf("%s的量測可信度存疑:產生器 worker 池曾達上限,延遲可能被低報", label))
	}
	return ws
}

// Compare 比較兩份 baseline。兩份必須有共同區段(同為 metrics 或同為 discover),
// 否則回傳錯誤——不可比較的東西默默比較,比不比較更危險。
func Compare(before, after Baseline, tol Tolerance) (Comparison, error) {
	hasMetrics := before.Metrics != nil && after.Metrics != nil
	hasDiscover := before.Discover != nil && after.Discover != nil
	if !hasMetrics && !hasDiscover {
		return Comparison{}, fmt.Errorf(
			"兩份 baseline 沒有共同區段可比較(before: metrics=%v discover=%v;after: metrics=%v discover=%v)",
			before.Metrics != nil, before.Discover != nil, after.Metrics != nil, after.Discover != nil)
	}

	var c Comparison
	if before.Scenario != after.Scenario {
		c.Warnings = append(c.Warnings, fmt.Sprintf(
			"兩份 baseline 的場景識別不同(%q vs %q),比較結果可能沒有意義", before.Scenario, after.Scenario))
	}
	c.Warnings = append(c.Warnings, trustWarnings("基準(before)", before)...)
	c.Warnings = append(c.Warnings, trustWarnings("本次(after)", after)...)

	if hasMetrics {
		b, a := before.Metrics, after.Metrics
		c.Deltas = append(c.Deltas,
			newDelta("p50_ms", b.P50Ms, a.P50Ms, false, tol.RelPct, tol.LatencyFloorMs),
			newDelta("p99_ms", b.P99Ms, a.P99Ms, false, tol.RelPct, tol.LatencyFloorMs),
			newDelta("error_rate_pct", b.ErrorRatePct, a.ErrorRatePct, false, tol.RelPct, tol.ErrorFloorPct),
			newDelta("throughput_rps", b.ThroughputRPS, a.ThroughputRPS, true, tol.RelPct, tol.ThroughputFloorRPS),
		)
		if b.HasCorrected && a.HasCorrected {
			c.Deltas = append(c.Deltas,
				newDelta("corrected_p99_ms", b.CorrectedP99Ms, a.CorrectedP99Ms, false, tol.RelPct, tol.LatencyFloorMs))
		}
	}
	if hasDiscover {
		b, a := before.Discover, after.Discover
		c.Deltas = append(c.Deltas,
			newDelta("safe_limit_rps", float64(b.SafeLimitRPS), float64(a.SafeLimitRPS), true, tol.RelPct, tol.ThroughputFloorRPS),
			newDelta("breaking_point_rps", float64(b.BreakingPointRPS), float64(a.BreakingPointRPS), true, tol.RelPct, tol.ThroughputFloorRPS),
		)
	}

	for _, d := range c.Deltas {
		if d.Verdict == VerdictRegressed {
			c.Regressed = true
			break
		}
	}
	return c, nil
}
