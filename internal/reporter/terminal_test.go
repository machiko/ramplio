package reporter_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/stretchr/testify/assert"
)

func makeSummary() metrics.Summary {
	s := metrics.Summary{
		Total:      100,
		Errors:     2,
		MinLatency: 10 * time.Millisecond,
		MaxLatency: 500 * time.Millisecond,
		BytesIn:    1024,
		WallTime:   10 * time.Second,
	}
	return s
}

func TestPrintSummary_ContainsKeyFields(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, makeSummary())
	out := buf.String()

	assert.Contains(t, out, "100")   // total requests
	assert.Contains(t, out, "10.0")  // req/sec
	assert.Contains(t, out, "10ms")  // min latency
	assert.Contains(t, out, "500ms") // max latency
	assert.Contains(t, out, "2.0%")  // error rate
	assert.True(t, strings.Contains(out, "測試結果"))
	assert.True(t, strings.Contains(out, "延遲分佈"))
	assert.True(t, strings.Contains(out, "回應狀態"))
}

func TestPrintSummary_ZeroTotal(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{})
	// 不應 panic，輸出結構正常
	assert.Contains(t, buf.String(), "測試結果")
}

func TestInterpretation_HealthyRun(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{
		Total: 20046, Errors: 0, WallTime: 5 * time.Second,
		P50: 5 * time.Millisecond, P99: 9 * time.Millisecond,
	})
	out := buf.String()

	assert.Contains(t, out, "測試結果說明")
	assert.Contains(t, out, "整體結論：✓")
	assert.Contains(t, out, "非常快")
	assert.Contains(t, out, "9 毫秒")   // humanized duration, not "9ms"
	assert.Contains(t, out, "完美")     // 0 errors
	assert.Contains(t, out, "20,046") // thousands separator
	assert.Contains(t, out, "又快又穩")   // one-line summary
	assert.Contains(t, out, "還有餘裕")
}

func TestInterpretation_SlowAndUnstableRun(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{
		Total: 1000, Errors: 100, WallTime: 10 * time.Second,
		P50: 800 * time.Millisecond, P99: 2 * time.Second,
	})
	out := buf.String()

	assert.Contains(t, out, "整體結論：✗") // 10% error rate → fail
	assert.Contains(t, out, "偏慢")     // p99 2s
	assert.Contains(t, out, "2.0 秒")  // humanized duration
	assert.Contains(t, out, "不穩定")    // 10% failures
	assert.Contains(t, out, "又慢又不穩")  // one-line summary
}

func TestInterpretation_FastButUnstable(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{
		Total: 1000, Errors: 30, WallTime: 5 * time.Second,
		P50: 10 * time.Millisecond, P99: 50 * time.Millisecond,
	})
	out := buf.String()

	assert.Contains(t, out, "整體結論：⚠")   // 3% errors → warning
	assert.Contains(t, out, "非常快")      // fast
	assert.Contains(t, out, "先解決穩定度問題") // one-line: fast but unstable
}

func TestHumanizeInt_ThousandsSeparator(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{Total: 1234567, WallTime: time.Second})
	assert.Contains(t, buf.String(), "1,234,567")
}

// rpsLine returns the trimmed value on the 每秒請求 line (the padded label is
// stripped) so tests assert on the rendered rps value alone.
func rpsLine(t *testing.T, sum metrics.Summary) string {
	t.Helper()
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, sum)
	for _, ln := range strings.Split(buf.String(), "\n") {
		if strings.Contains(ln, "每秒請求") {
			return strings.TrimSpace(strings.SplitN(ln, "：", 2)[1])
		}
	}
	t.Fatal("每秒請求 line not found")
	return ""
}

// The terminal 每秒請求 line must round the rps the same way the 承受能力卡片 does
// (math.Round via the shared source), so the same run never shows "0.2" here and
// "0.3" on the card. rps = 1/4s = 0.25 sits on the x.x5 boundary where fmt's
// round-half-to-even ("0.2") and the shared round-half-away ("0.3") diverge.
func TestPrintSummary_RPSMatchesCapacityRounding(t *testing.T) {
	got := rpsLine(t, metrics.Summary{Total: 1, WallTime: 4 * time.Second})
	assert.True(t, strings.HasPrefix(got, "0.3"), "terminal rps 應與承受能力卡片同一套捨入(0.3)，得到 %q", got)
}

// The consistency fix must not change the fast-endpoint format: rps ≥ 10 keeps one
// decimal in the terminal (承受能力卡片 drops it, by design — different surfaces).
func TestPrintSummary_HighRPSKeepsOneDecimal(t *testing.T) {
	got := rpsLine(t, metrics.Summary{Total: 14253, WallTime: 100 * time.Second})
	assert.Equal(t, "142.5", got)
}

// rpm-1's no-contradiction boundary, pinned at the terminal surface: an rps that
// rounds up to 1.0 must display "1.0" with no RPM annotation (a "1.0（≈ 58 RPM）"
// would contradict the ×60 conversion). rps = 96/100s = 0.96 → roundRate → 1.0.
func TestPrintSummary_RoundsToOneNoRPM(t *testing.T) {
	got := rpsLine(t, metrics.Summary{Total: 96, WallTime: 100 * time.Second})
	assert.Equal(t, "1.0", got, "顯示為 1.0 時不得再附 RPM")
}

// 串流場景:TTFT(開始回應)與完整回應並陳——串流體感由 TTFT 決定,
// 但兩個數字都要給,不可只留一個。非串流場景該段落缺席。
func TestPrintSummary_TTFTShownWhenPresent(t *testing.T) {
	sum := metrics.Summary{
		Total: 100, WallTime: 10 * time.Second,
		P50: 400 * time.Millisecond, P99: 900 * time.Millisecond,
		HasTTFT: true,
		TTFTP50: 80 * time.Millisecond, TTFTP99: 200 * time.Millisecond,
	}
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, sum)
	out := buf.String()

	for _, want := range []string{"開始回應", "80ms", "200ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("輸出應包含 %q\n%s", want, out)
		}
	}
}

func TestPrintSummary_NoTTFTNoSection(t *testing.T) {
	sum := metrics.Summary{Total: 100, WallTime: 10 * time.Second, P99: 100 * time.Millisecond}
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, sum)

	if strings.Contains(buf.String(), "開始回應") {
		t.Error("無 TTFT 樣本時不應顯示串流段落")
	}
}

// 複審發現(HIGH):rate 模式下 TTFT 是 CO 修正值(含排隊),
// 「完整回應」若用原始 P50/P99 會出現「開始回應比完整回應慢」的倒掛
// 矛盾——兩邊基準必須一致:有 CO 修正時完整回應也用使用者實感值。
func TestPrintSummary_TTFTBaselineConsistentUnderCO(t *testing.T) {
	sum := metrics.Summary{
		Total: 100, WallTime: 10 * time.Second,
		P50: 100 * time.Millisecond, P99: 150 * time.Millisecond,
		HasCorrected: true,
		CorrectedP50: 320 * time.Millisecond, CorrectedP99: 480 * time.Millisecond,
		HasTTFT: true,
		TTFTP50: 250 * time.Millisecond, TTFTP99: 300 * time.Millisecond,
	}
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, sum)
	out := buf.String()

	// 完整回應應顯示 corrected 值(320/480ms),不可顯示原始 100/150ms
	// 造成「開始回應 250ms > 完整回應 100ms」的矛盾。
	streamSection := out[strings.Index(out, "串流回應"):]
	if end := strings.Index(streamSection, "回應狀態"); end > 0 {
		streamSection = streamSection[:end]
	}
	if !strings.Contains(streamSection, "320ms") || !strings.Contains(streamSection, "480ms") {
		t.Errorf("rate 模式下完整回應應用使用者實感值(320/480ms)\n%s", streamSection)
	}
	if strings.Contains(streamSection, "100ms") {
		t.Errorf("完整回應不應顯示原始值 100ms(與修正後 TTFT 基準不一致)\n%s", streamSection)
	}
}
