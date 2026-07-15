package dashboard

import (
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
)

// TTFT 卡片快照必須自帶「配對好基準」的完整回應數字(sse-1 倒掛教訓):
// rate 模式下 TTFT 是 CO 修正值,完整回應必須同用使用者實感值,
// 不可讓前端拿 result.p50_ms(原始值)自行對照。
func TestTTFTSnapFromPairsCorrectedBaseline(t *testing.T) {
	sum := metrics.Summary{
		P50: 100 * time.Millisecond, P99: 150 * time.Millisecond,
		HasCorrected: true,
		CorrectedP50: 320 * time.Millisecond, CorrectedP99: 480 * time.Millisecond,
		HasTTFT: true,
		TTFTP50: 250 * time.Millisecond, TTFTP99: 300 * time.Millisecond,
	}
	snap := TTFTSnapFrom(sum)
	if snap == nil {
		t.Fatal("HasTTFT 時不應為 nil")
	}
	if snap.P50Ms != 250 || snap.P99Ms != 300 {
		t.Errorf("TTFT 數字錯誤: %+v", snap)
	}
	if snap.FullP50Ms != 320 || snap.FullP99Ms != 480 {
		t.Errorf("rate 模式完整回應應為使用者實感值(320/480),得到 %d/%d——原始值會重演倒掛", snap.FullP50Ms, snap.FullP99Ms)
	}
	if snap.P50Ms > snap.FullP50Ms || snap.P99Ms > snap.FullP99Ms {
		t.Error("開始回應不可大於完整回應(基準一致時數學上不可能)")
	}
}

// VU 模式(無 CO 修正):完整回應用原始值,天然同基準。
func TestTTFTSnapFromVUModeRawBaseline(t *testing.T) {
	sum := metrics.Summary{
		P50: 200 * time.Millisecond, P99: 400 * time.Millisecond,
		HasTTFT: true,
		TTFTP50: 80 * time.Millisecond, TTFTP99: 120 * time.Millisecond,
	}
	snap := TTFTSnapFrom(sum)
	if snap == nil {
		t.Fatal("HasTTFT 時不應為 nil")
	}
	if snap.FullP50Ms != 200 || snap.FullP99Ms != 400 {
		t.Errorf("VU 模式完整回應應為原始值(200/400),得到 %d/%d", snap.FullP50Ms, snap.FullP99Ms)
	}
}

// 非串流場景:nil,卡片以缺席表達不適用。
func TestTTFTSnapFromNilWhenNoTTFT(t *testing.T) {
	if snap := TTFTSnapFrom(metrics.Summary{P50: time.Millisecond}); snap != nil {
		t.Errorf("無 TTFT 樣本應回 nil,得到 %+v", snap)
	}
}
