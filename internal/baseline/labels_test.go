package baseline

import "testing"

// 標籤是 CLI 與 dashboard 的共同單一來源;全形括號等用字逐字釘死,
// 兩個呈現層不可各自演化出同義詞。
func TestMetricLabelKnownKeys(t *testing.T) {
	cases := map[string]string{
		"p50_ms":             "p50（伺服器處理）",
		"p99_ms":             "p99（伺服器處理）",
		"corrected_p99_ms":   "p99（使用者實感）",
		"error_rate_pct":     "錯誤率",
		"throughput_rps":     "每秒請求",
		"safe_limit_rps":     "安全上限",
		"breaking_point_rps": "臨界點",
	}
	for name, want := range cases {
		if got := MetricLabel(name); got != want {
			t.Errorf("MetricLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

// 新指標沒對到標籤時退回原鍵名——寧可醜也不可漏印。
func TestMetricLabelUnknownFallsBack(t *testing.T) {
	if got := MetricLabel("future_metric_xyz"); got != "future_metric_xyz" {
		t.Errorf("未知鍵應退回原名,得到 %q", got)
	}
}

// Parse 是上傳情境的入口(dashboard 收 bytes,沒有檔案路徑),
// 與 Load 共用驗證:壞資料必須大聲失敗,不可默默收下。
func TestParseValidBytes(t *testing.T) {
	raw := []byte(`{"schema_version":1,"scenario":"s","metrics":{"total":10,"p50_ms":5,"p99_ms":9,"error_rate_pct":0,"throughput_rps":100}}`)
	b, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if b.Scenario != "s" || b.Metrics == nil || b.Metrics.P99Ms != 9 {
		t.Errorf("欄位對應錯誤: %+v", b)
	}
}

func TestParseRejectsCorruptJSON(t *testing.T) {
	if _, err := Parse([]byte("{not json")); err == nil {
		t.Fatal("壞 JSON 應回傳錯誤")
	}
}

func TestParseRejectsEmptySections(t *testing.T) {
	if _, err := Parse([]byte(`{"schema_version":1}`)); err == nil {
		t.Fatal("Metrics 與 Discover 皆空應回傳錯誤")
	}
}

func TestParseRejectsNewerSchema(t *testing.T) {
	if _, err := Parse([]byte(`{"schema_version":999,"metrics":{"total":1}}`)); err == nil {
		t.Fatal("schema 過新應回傳錯誤")
	}
}

func TestFormatMetricValue(t *testing.T) {
	cases := []struct {
		name string
		v    float64
		want string
	}{
		{"p99_ms", 123.4, "123ms"},
		{"error_rate_pct", 1.25, "1.2%"},
		{"throughput_rps", 1500.7, "1501"},
	}
	for _, c := range cases {
		if got := FormatMetricValue(c.name, c.v); got != c.want {
			t.Errorf("FormatMetricValue(%q, %v) = %q, want %q", c.name, c.v, got, c.want)
		}
	}
}
