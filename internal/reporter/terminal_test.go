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
