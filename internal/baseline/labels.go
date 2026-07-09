package baseline

import (
	"fmt"
	"strings"
)

// metricLabels 把指標鍵翻成白話;CLI(compare)與 dashboard 卡片的
// 唯一用語來源——新指標沒對到時退回原鍵名,不會漏印。
// 用語與 terminal/interpret 對齊:服務端延遲=「伺服器處理」、
// CO 修正延遲=「使用者實感」,不自創同義詞。
var metricLabels = map[string]string{
	"p50_ms":             "p50（伺服器處理）",
	"p99_ms":             "p99（伺服器處理）",
	"corrected_p99_ms":   "p99（使用者實感）",
	"error_rate_pct":     "錯誤率",
	"throughput_rps":     "每秒請求",
	"safe_limit_rps":     "安全上限",
	"breaking_point_rps": "臨界點",
}

// MetricLabel 回傳指標鍵的白話標籤;未知鍵退回原鍵名。
func MetricLabel(name string) string {
	if l, ok := metricLabels[name]; ok {
		return l
	}
	return name
}

// FormatMetricValue 依指標鍵的單位字尾格式化數值,呈現層共用。
func FormatMetricValue(name string, v float64) string {
	switch {
	case strings.HasSuffix(name, "_ms"):
		return fmt.Sprintf("%.0fms", v)
	case strings.HasSuffix(name, "_pct"):
		return fmt.Sprintf("%.1f%%", v)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}
