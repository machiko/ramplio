package reporter_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/reporter"
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

	assert.Contains(t, out, "100")      // total requests
	assert.Contains(t, out, "10.0")     // req/sec
	assert.Contains(t, out, "10ms")     // min latency
	assert.Contains(t, out, "500ms")    // max latency
	assert.Contains(t, out, "2.0%")     // error rate
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
	assert.Contains(t, out, "9 毫秒")        // humanized duration, not "9ms"
	assert.Contains(t, out, "完美")          // 0 errors
	assert.Contains(t, out, "20,046")        // thousands separator
	assert.Contains(t, out, "又快又穩")       // one-line summary
	assert.Contains(t, out, "還有餘裕")
}

func TestInterpretation_SlowAndUnstableRun(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{
		Total: 1000, Errors: 100, WallTime: 10 * time.Second,
		P50: 800 * time.Millisecond, P99: 2 * time.Second,
	})
	out := buf.String()

	assert.Contains(t, out, "整體結論：✗")     // 10% error rate → fail
	assert.Contains(t, out, "偏慢")           // p99 2s
	assert.Contains(t, out, "2.0 秒")         // humanized duration
	assert.Contains(t, out, "不穩定")          // 10% failures
	assert.Contains(t, out, "又慢又不穩")       // one-line summary
}

func TestInterpretation_FastButUnstable(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{
		Total: 1000, Errors: 30, WallTime: 5 * time.Second,
		P50: 10 * time.Millisecond, P99: 50 * time.Millisecond,
	})
	out := buf.String()

	assert.Contains(t, out, "整體結論：⚠")            // 3% errors → warning
	assert.Contains(t, out, "非常快")                 // fast
	assert.Contains(t, out, "先解決穩定度問題")          // one-line: fast but unstable
}

func TestHumanizeInt_ThousandsSeparator(t *testing.T) {
	var buf bytes.Buffer
	reporter.PrintSummary(&buf, metrics.Summary{Total: 1234567, WallTime: time.Second})
	assert.Contains(t, buf.String(), "1,234,567")
}
