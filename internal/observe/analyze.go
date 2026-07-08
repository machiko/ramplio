package observe

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// AnalysisStatus 是分析結論的三態。誠實原則的型別化:
// 資料不足與找不到單點瓶頸都是合法結論,不可為了「有答案」而硬給歸因。
type AnalysisStatus string

const (
	// StatusOK 表示找到顯著的單點退化,Top 依倍率降冪排列。
	StatusOK AnalysisStatus = "ok"
	// StatusInsufficient 表示樣本不足,無法做有代表性的比較;Top 必為空。
	StatusInsufficient AnalysisStatus = "insufficient"
	// StatusNoCulprit 表示有足夠樣本、整體變慢但無特定瓶頸(疑似資源飽和)。
	StatusNoCulprit AnalysisStatus = "no_culprit"
)

// Degradation 是單一 operation 在兩個時間窗之間的退化。
type Degradation struct {
	Operation   string
	BaselineP95 time.Duration
	StressedP95 time.Duration
	// Factor = StressedP95 / max(BaselineP95, floor),越大退化越嚴重。
	Factor         float64
	SampleBaseline int
	SampleStressed int
}

// Analysis 是雙時間窗比較的完整結論。
//
// 已知限制(刻意揭露):比較基於 p95,只偵測「p95 尾端本身的退化」;
// 慢路徑「佔比」上升但峰值延遲不變的退化型態(如快取命中率下降)
// 不會反映在 p95 上,本引擎偵測不到——這是 p95 方法的固有盲點。
type Analysis struct {
	Status AnalysisStatus
	// Reason 以白話說明結論的原因、限制與改善方向;StatusOK 時亦可能
	// 攜帶保留註記(樣本排除、可比較 op 過少)。
	Reason string
	// Top 依 Factor 降冪;僅 StatusOK / StatusNoCulprit 時有內容。
	Top []Degradation
	// ExcludedOps 列出「因樣本不足或僅出現於單一時間窗」而未納入比較的
	// operation。排除必須可見:被排除者可能正是真實瓶頸,
	// 沒有這個欄位,no_culprit 會變成「已窮盡搜尋」的假斷言。
	ExcludedOps []string
}

// AnalyzeConfig 的門檻皆為經驗預設,可由呼叫端覆寫。
type AnalyzeConfig struct {
	// MinSamplesPerOp:單一 operation 在「兩個」時間窗都需達此樣本數才納入比較。
	// 注意:p95 索引在 n<20 時退化為 max(單一離群 trace 即可偽造瓶頸),
	// 預設值 20 是 p95 有統計意義的最低樣本數,調低前務必理解此風險。
	MinSamplesPerOp int
	// CulpritFactor:Top1 倍率至少要達此值才可能判定單點瓶頸。
	CulpritFactor float64
	// CulpritVsRestMedian:Top1 倍率需達「其餘 operation 倍率中位數」的此倍數,
	// 否則視為全面等幅變慢(資源飽和),不指認單點瓶頸。
	CulpritVsRestMedian float64
	// BaselineFloor:倍率分母下限,防止趨近零的基準延遲讓倍率爆表。
	BaselineFloor time.Duration
}

func DefaultAnalyzeConfig() AnalyzeConfig {
	return AnalyzeConfig{
		MinSamplesPerOp:     20,
		CulpritFactor:       2.0,
		CulpritVsRestMedian: 1.5,
		BaselineFloor:       time.Millisecond,
	}
}

// p95 回傳排序後的第 95 百分位;呼叫端保證 durations 非空。
func p95(durations []time.Duration) time.Duration {
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (len(sorted)*95+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

func groupByOp(spans []Span) map[string][]time.Duration {
	m := map[string][]time.Duration{}
	for _, s := range spans {
		m[s.Operation] = append(m[s.Operation], s.Duration)
	}
	return m
}

// AnalyzeWindows 比較基準窗與臨界窗的 per-operation 延遲分佈。
// 只比較「兩窗皆有足夠樣本」的 operation;單邊出現的無從比較,跳過。
func AnalyzeWindows(baseline, stressed []Span, cfg AnalyzeConfig) Analysis {
	base := groupByOp(baseline)
	stress := groupByOp(stressed)

	var degs []Degradation
	var excluded []string
	for op := range stress {
		if _, ok := base[op]; !ok {
			excluded = append(excluded, op) // 僅出現於臨界窗
		}
	}
	for op, baseDurs := range base {
		stressDurs, ok := stress[op]
		if !ok {
			excluded = append(excluded, op) // 僅出現於基準窗
			continue
		}
		if len(baseDurs) < cfg.MinSamplesPerOp || len(stressDurs) < cfg.MinSamplesPerOp {
			excluded = append(excluded, op) // 樣本不足——可能正是真實瓶頸,必須可見
			continue
		}
		bp95, sp95 := p95(baseDurs), p95(stressDurs)
		denom := bp95
		if denom < cfg.BaselineFloor {
			denom = cfg.BaselineFloor
		}
		degs = append(degs, Degradation{
			Operation:      op,
			BaselineP95:    bp95,
			StressedP95:    sp95,
			Factor:         float64(sp95) / float64(denom),
			SampleBaseline: len(baseDurs),
			SampleStressed: len(stressDurs),
		})
	}

	sort.Strings(excluded)

	// caveats 是不改變 Status、但必須讓使用者知道的保留註記。
	var caveats []string
	if len(excluded) > 0 {
		caveats = append(caveats, fmt.Sprintf(
			"另有 %d 個 operation 因樣本不足或僅出現於單一時間窗而未納入比較,可能遺漏真實瓶頸(見 ExcludedOps)。",
			len(excluded)))
	}
	if n := len(degs); n >= 1 && n < 3 {
		caveats = append(caveats, fmt.Sprintf(
			"可比較的 operation 僅 %d 個,單點瓶頸判定的統計把握度較低。", n))
	}
	withCaveats := func(primary string) string {
		parts := caveats
		if primary != "" {
			parts = append([]string{primary}, caveats...)
		}
		return strings.Join(parts, "")
	}

	if len(degs) == 0 {
		return Analysis{
			Status: StatusInsufficient,
			Reason: withCaveats(fmt.Sprintf(
				"沒有任何 operation 在兩個時間窗都達到 %d 筆樣本——無法做有代表性的比較。"+
					"可嘗試:拉長壓測時間、提高 APM 取樣率、確認 service 名稱正確。",
				cfg.MinSamplesPerOp)),
			ExcludedOps: excluded,
		}
	}

	sort.Slice(degs, func(i, j int) bool { return degs[i].Factor > degs[j].Factor })

	top := degs[0].Factor
	if top < cfg.CulpritFactor {
		return Analysis{
			Status: StatusNoCulprit,
			Reason: withCaveats(fmt.Sprintf(
				"最大退化倍率僅 %.1fx(門檻 %.1fx)——在已納入比較的 operation 中沒有明顯的單點瓶頸。",
				top, cfg.CulpritFactor)),
			Top:         degs,
			ExcludedOps: excluded,
		}
	}
	// 與「其餘 operation」的中位數比:全體等幅變慢代表資源飽和,
	// 硬指一個最慢的 operation 會是錯誤歸因。
	if len(degs) > 1 {
		rest := make([]float64, 0, len(degs)-1)
		for _, d := range degs[1:] {
			rest = append(rest, d.Factor)
		}
		sort.Float64s(rest)
		median := rest[len(rest)/2]
		if top < cfg.CulpritVsRestMedian*median {
			return Analysis{
				Status: StatusNoCulprit,
				Reason: withCaveats(fmt.Sprintf(
					"所有 operation 大致等幅變慢(Top %.1fx vs 其餘中位數 %.1fx)——"+
						"疑似整體資源飽和(CPU/連線池/GC),而非單點瓶頸。", top, median)),
				Top:         degs,
				ExcludedOps: excluded,
			}
		}
	}
	return Analysis{
		Status:      StatusOK,
		Reason:      withCaveats(""),
		Top:         degs,
		ExcludedOps: excluded,
	}
}
