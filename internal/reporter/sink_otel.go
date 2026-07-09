package reporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
)

// OtelSink 以 OTLP/HTTP(JSON 編碼)把最終 Summary 推送到 OpenTelemetry collector。
// DSN 格式:otel://host:4318(otels:// 走 https),端點固定 /v1/metrics。
//
// 刻意不引入 go.opentelemetry.io SDK:一次性匯出 ~10 個 gauge 不需要整棵
// SDK 依賴樹(binary 增重數 MB),手刻 OTLP/JSON 與本套件既有 sink
// (Influx line protocol、Loki JSON)的零重依賴風格一致。
// OTLP/JSON 規範要點:欄位 camelCase、fixed64(timeUnixNano)以字串表示。
type OtelSink struct {
	endpoint string
	client   *http.Client
}

func NewOtelSink(dsn string) (*OtelSink, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("otel sink: invalid DSN: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("otel sink: host required (e.g. otel://localhost:4318)")
	}
	scheme := "http"
	if u.Scheme == "otels" {
		scheme = "https"
	}
	return &OtelSink{
		endpoint: fmt.Sprintf("%s://%s/v1/metrics", scheme, u.Host),
		client:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// otlp JSON 骨架(僅本 sink 需要的子集)
type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

type otlpDataPoint struct {
	AsDouble     float64    `json:"asDouble"`
	TimeUnixNano string     `json:"timeUnixNano"`
	Attributes   []otlpAttr `json:"attributes,omitempty"`
}

type otlpMetric struct {
	Name  string `json:"name"`
	Unit  string `json:"unit,omitempty"`
	Gauge struct {
		DataPoints []otlpDataPoint `json:"dataPoints"`
	} `json:"gauge"`
}

func strAttr(key, val string) otlpAttr {
	a := otlpAttr{Key: key}
	a.Value.StringValue = val
	return a
}

// Write 匯出最終彙總指標。遵循 Sink 契約:單一 goroutine、測後 flush 一次。
func (s *OtelSink) Write(sum metrics.Summary, scenarioName string) error {
	now := strconv.FormatInt(time.Now().UnixNano(), 10)
	attrs := []otlpAttr{strAttr("scenario", scenarioName)}
	ms := float64(time.Millisecond)

	gauges := []struct {
		name string
		unit string
		val  float64
	}{
		// 一次性快照全部用 gauge;命名刻意避開 .total——該尾綴在 OTel 慣例
		// 暗示 Sum/Counter,gauge 配 .total 會讓 Prometheus 轉譯層語意衝突。
		{"ramplio.requests.count", "1", float64(sum.Total)},
		{"ramplio.errors.count", "1", float64(sum.Errors)},
		{"ramplio.error_rate_pct", "%", sum.ErrorRate()},
		{"ramplio.throughput_rps", "1/s", sum.RPS()},
		{"ramplio.bytes_received", "By", float64(sum.BytesIn)},
		{"ramplio.latency.p50_ms", "ms", float64(sum.P50) / ms},
		{"ramplio.latency.p90_ms", "ms", float64(sum.P90) / ms},
		{"ramplio.latency.p95_ms", "ms", float64(sum.P95) / ms},
		{"ramplio.latency.p99_ms", "ms", float64(sum.P99) / ms},
	}
	if sum.HasCorrected {
		gauges = append(gauges, struct {
			name string
			unit string
			val  float64
		}{"ramplio.latency.corrected_p99_ms", "ms", float64(sum.CorrectedP99) / ms})
	}

	otlpMetrics := make([]otlpMetric, 0, len(gauges))
	for _, g := range gauges {
		m := otlpMetric{Name: g.name, Unit: g.unit}
		m.Gauge.DataPoints = []otlpDataPoint{{AsDouble: g.val, TimeUnixNano: now, Attributes: attrs}}
		otlpMetrics = append(otlpMetrics, m)
	}

	payload := map[string]any{
		"resourceMetrics": []map[string]any{{
			"resource": map[string]any{
				"attributes": []otlpAttr{strAttr("service.name", "ramplio")},
			},
			"scopeMetrics": []map[string]any{{
				"scope":   map[string]string{"name": "ramplio"},
				"metrics": otlpMetrics,
			}},
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("otel sink: 序列化失敗: %w", err)
	}
	resp, err := s.client.Post(s.endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("otel sink: 推送到 %s 失敗: %w", s.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("otel sink: collector 回應 %d: %s", resp.StatusCode, snippet)
	}
	return nil
}

func (s *OtelSink) Close() error {
	s.client.CloseIdleConnections()
	return nil
}
