package reporter_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/reporter"
	"github.com/stretchr/testify/assert"
)

func findingByTitle(findings []reporter.Finding, substr string) (reporter.Finding, bool) {
	for _, f := range findings {
		if strings.Contains(f.Title, substr) {
			return f, true
		}
	}
	return reporter.Finding{}, false
}

func TestDiagnose_TailLatency(t *testing.T) {
	// 多數人 90ms，最慢的 1% 1.8s → 尾端延遲（比例 20×、且超過 500ms 門檻）
	sum := metrics.Summary{
		Total: 1000, Errors: 0, WallTime: 10 * time.Second,
		P50: 90 * time.Millisecond, P99: 1800 * time.Millisecond,
	}
	f, ok := findingByTitle(reporter.Diagnose(sum), "尾端延遲")
	assert.True(t, ok, "應偵測到尾端延遲")
	assert.Equal(t, "warn", f.Severity)
	assert.Contains(t, f.Evidence, "1.8 秒")
}

func TestDiagnose_TailLatencySkippedWhenFast(t *testing.T) {
	// 比例雖高（8/5=1.6×）但絕對值極小，不應誤報；應回單一 good
	sum := metrics.Summary{
		Total: 1000, Errors: 0, WallTime: 5 * time.Second,
		P50: 5 * time.Millisecond, P99: 8 * time.Millisecond,
	}
	findings := reporter.Diagnose(sum)
	_, ok := findingByTitle(findings, "尾端延遲")
	assert.False(t, ok, "快速測試不應觸發尾端延遲")
	assert.Len(t, findings, 1)
	assert.Equal(t, "good", findings[0].Severity)
}

func TestDiagnose_ErrorConcentration(t *testing.T) {
	// 錯誤幾乎都在 B：整體 1%，B 步驟 10%
	sum := metrics.Summary{
		Total: 1000, Errors: 10, WallTime: 5 * time.Second,
		P50: 10 * time.Millisecond, P99: 50 * time.Millisecond,
		Steps: []metrics.StepSummary{
			{Name: "GET A", Total: 900, Errors: 0, P99: 40 * time.Millisecond},
			{Name: "POST B", Total: 100, Errors: 10, P99: 45 * time.Millisecond},
		},
	}
	f, ok := findingByTitle(reporter.Diagnose(sum), "POST B")
	assert.True(t, ok, "應指出錯誤集中於 POST B")
	assert.Equal(t, "warn", f.Severity)
}

func TestDiagnose_BottleneckStep(t *testing.T) {
	// B 的 p99 遠高於 A（400ms vs 20ms）
	sum := metrics.Summary{
		Total: 1000, Errors: 0, WallTime: 5 * time.Second,
		P50: 20 * time.Millisecond, P99: 400 * time.Millisecond,
		Steps: []metrics.StepSummary{
			{Name: "GET A", Total: 500, Errors: 0, P99: 20 * time.Millisecond},
			{Name: "GET B", Total: 500, Errors: 0, P99: 400 * time.Millisecond},
		},
	}
	f, ok := findingByTitle(reporter.Diagnose(sum), "GET B")
	assert.True(t, ok, "應指出瓶頸步驟 GET B")
	assert.Equal(t, "info", f.Severity)
}

func TestDiagnose_GroupDivergence(t *testing.T) {
	sum := metrics.Summary{
		Total: 1000, Errors: 0, WallTime: 5 * time.Second,
		P50: 900 * time.Millisecond, P99: 900 * time.Millisecond, // 高 p50 → 避免尾端延遲干擾
		Groups: []metrics.GroupSummary{
			{Name: "登入", Total: 500, Errors: 0, P99: 30 * time.Millisecond},
			{Name: "查詢", Total: 500, Errors: 0, P99: 900 * time.Millisecond},
		},
	}
	f, ok := findingByTitle(reporter.Diagnose(sum), "查詢")
	assert.True(t, ok, "應指出群組分化（查詢較慢）")
	assert.Equal(t, "info", f.Severity)
}

func TestDiagnose_DroppedSamples(t *testing.T) {
	sum := metrics.Summary{
		Total: 1000, Errors: 0, WallTime: 5 * time.Second,
		P50: 10 * time.Millisecond, P99: 40 * time.Millisecond,
		DroppedSamples: 120,
	}
	f, ok := findingByTitle(reporter.Diagnose(sum), "沒收集到")
	assert.True(t, ok, "應提醒量測不完整")
	assert.Equal(t, "warn", f.Severity)
	assert.Contains(t, f.Evidence, "120")
}

func TestDiagnose_Healthy(t *testing.T) {
	sum := metrics.Summary{
		Total: 1000, Errors: 0, WallTime: 5 * time.Second,
		P50: 10 * time.Millisecond, P99: 40 * time.Millisecond,
	}
	findings := reporter.Diagnose(sum)
	assert.Len(t, findings, 1)
	assert.Equal(t, "good", findings[0].Severity)
}

func TestDiagnose_OverloadSortsCriticalFirst(t *testing.T) {
	// 高錯誤 + 高延遲 → critical 過載，且應排在最前
	sum := metrics.Summary{
		Total: 1000, Errors: 80, WallTime: 5 * time.Second,
		P50: 100 * time.Millisecond, P99: 4 * time.Second,
	}
	findings := reporter.Diagnose(sum)
	assert.Equal(t, "critical", findings[0].Severity, "critical 應排最前")
	_, ok := findingByTitle(findings, "超出負荷")
	assert.True(t, ok)
}

func TestDiagnose_EmptySummaryNoPanic(t *testing.T) {
	findings := reporter.Diagnose(metrics.Summary{})
	assert.Len(t, findings, 1)
	assert.Equal(t, "good", findings[0].Severity)
}

func TestInterpret_AttachesDiagnosis(t *testing.T) {
	sum := metrics.Summary{Total: 1000, Errors: 0, WallTime: 5 * time.Second, P99: 40 * time.Millisecond}
	in := reporter.Interpret(sum)
	assert.NotEmpty(t, in.Diagnosis, "Interpret 應附上診斷")
}

func TestReadingsFor_OmitsDiagnosis(t *testing.T) {
	// dashboard live 路徑不帶診斷
	in := reporter.ReadingsFor(40*time.Millisecond, 0, 100, 1000, 0, "")
	assert.Empty(t, in.Diagnosis)
}
