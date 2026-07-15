package dashboard

import "github.com/machiko/ramplio/v3/internal/metrics"

// TTFTSnap 是串流回應量測的 GUI 快照(stream: sse 場景才有)。
// FullP50Ms/FullP99Ms 是「配對好基準」的完整回應數字:rate 模式下
// TTFT 是 CO 修正值(含排隊等待),完整回應必須同用使用者實感值
// (CorrectedP*)——前端不可拿 RunResult 的原始 p50_ms/p99_ms 對照,
// 否則會出現「開始回應比完整回應慢」的倒掛矛盾(terminal 同一教訓)。
type TTFTSnap struct {
	P50Ms     int64 `json:"p50_ms"`
	P99Ms     int64 `json:"p99_ms"`
	FullP50Ms int64 `json:"full_p50_ms"`
	FullP99Ms int64 `json:"full_p99_ms"`
}

// TTFTSnapFrom 由 Summary 建立快照;無 TTFT 樣本(非串流場景)回 nil,
// 卡片以缺席表達不適用。
func TTFTSnapFrom(sum metrics.Summary) *TTFTSnap {
	if !sum.HasTTFT {
		return nil
	}
	fullP50, fullP99 := sum.P50, sum.P99
	if sum.HasCorrected {
		fullP50, fullP99 = sum.CorrectedP50, sum.CorrectedP99
	}
	return &TTFTSnap{
		P50Ms:     sum.TTFTP50.Milliseconds(),
		P99Ms:     sum.TTFTP99.Milliseconds(),
		FullP50Ms: fullP50.Milliseconds(),
		FullP99Ms: fullP99.Milliseconds(),
	}
}
