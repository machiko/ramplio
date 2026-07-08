package reporter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
)

func otelSummary() metrics.Summary {
	return metrics.Summary{
		Total:        1000,
		Errors:       10,
		BytesIn:      2048,
		WallTime:     10 * time.Second,
		P50:          50 * time.Millisecond,
		P90:          90 * time.Millisecond,
		P95:          95 * time.Millisecond,
		P99:          120 * time.Millisecond,
		CorrectedP99: 200 * time.Millisecond,
		HasCorrected: true,
	}
}

// collectorStub 收下 OTLP/HTTP 請求供斷言。
func collectorStub(t *testing.T, status int) (*httptest.Server, *[]byte, *[]string) {
	t.Helper()
	var body []byte
	var meta []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		meta = append(meta, r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &meta
}

func TestOtelSinkWritesOTLPMetrics(t *testing.T) {
	srv, body, meta := collectorStub(t, http.StatusOK)
	dsn := "otel://" + strings.TrimPrefix(srv.URL, "http://")

	sink, err := NewOtelSink(dsn)
	if err != nil {
		t.Fatalf("NewOtelSink: %v", err)
	}
	defer sink.Close()

	if err := sink.Write(otelSummary(), "testdata/example.yaml"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if (*meta)[0] != "POST" || (*meta)[1] != "/v1/metrics" {
		t.Fatalf("應 POST 到 /v1/metrics,實際 %v", *meta)
	}
	if !strings.Contains((*meta)[2], "application/json") {
		t.Fatalf("Content-Type 應為 application/json,實際 %q", (*meta)[2])
	}

	// OTLP/JSON 結構性斷言:resourceMetrics → scopeMetrics → metrics
	var payload struct {
		ResourceMetrics []struct {
			ScopeMetrics []struct {
				Metrics []struct {
					Name  string `json:"name"`
					Gauge *struct {
						DataPoints []struct {
							AsDouble     float64 `json:"asDouble"`
							TimeUnixNano string  `json:"timeUnixNano"`
						} `json:"dataPoints"`
					} `json:"gauge"`
				} `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(*body, &payload); err != nil {
		t.Fatalf("payload 非合法 JSON: %v\n%s", err, *body)
	}
	if len(payload.ResourceMetrics) == 0 || len(payload.ResourceMetrics[0].ScopeMetrics) == 0 {
		t.Fatalf("OTLP 結構缺層: %s", *body)
	}

	found := map[string]float64{}
	for _, m := range payload.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		if m.Gauge != nil && len(m.Gauge.DataPoints) > 0 {
			found[m.Name] = m.Gauge.DataPoints[0].AsDouble
			if m.Gauge.DataPoints[0].TimeUnixNano == "" || m.Gauge.DataPoints[0].TimeUnixNano == "0" {
				t.Fatalf("指標 %s 的 timeUnixNano 不可為空/零(OTLP/JSON 以字串表示)", m.Name)
			}
		}
	}
	for name, want := range map[string]float64{
		"ramplio.latency.p99_ms":           120,
		"ramplio.latency.corrected_p99_ms": 200,
		"ramplio.error_rate_pct":           1.0,
		"ramplio.throughput_rps":           100,
		// 一次性快照用 gauge;命名避開 .total(該尾綴在 OTel 慣例暗示
		// Sum/Counter 型別,gauge 配 .total 會讓 Prometheus 轉譯層語意衝突)
		"ramplio.requests.count": 1000,
		"ramplio.errors.count":   10,
	} {
		got, ok := found[name]
		if !ok {
			t.Fatalf("缺指標 %s,收到: %v", name, found)
		}
		if got != want {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
	// scenario 應以 attribute 附在資料點或 resource 上
	if !strings.Contains(string(*body), "testdata/example.yaml") {
		t.Fatalf("payload 應含 scenario 識別,實際: %s", *body)
	}
}

func TestOtelSinkSkipsCorrectedWhenAbsent(t *testing.T) {
	srv, body, _ := collectorStub(t, http.StatusOK)
	sink, err := NewOtelSink("otel://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("NewOtelSink: %v", err)
	}
	s := otelSummary()
	s.HasCorrected = false
	if err := sink.Write(s, "x"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.Contains(string(*body), "corrected_p99") {
		t.Fatal("VU 模式(無 corrected)不應匯出 corrected 指標")
	}
}

func TestOtelSinkCollectorErrorSurfaced(t *testing.T) {
	srv, _, _ := collectorStub(t, http.StatusInternalServerError)
	sink, err := NewOtelSink("otel://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("NewOtelSink: %v", err)
	}
	if err := sink.Write(otelSummary(), "x"); err == nil {
		t.Fatal("collector 回 500 時 Write 應回傳錯誤,不可默默吞掉")
	}
}

func TestOtelSinkDSNValidation(t *testing.T) {
	if _, err := NewOtelSink("otel://"); err == nil {
		t.Fatal("缺 host 的 DSN 應回傳錯誤")
	}
}

func TestOtelSinkSchemeSelectsHTTPS(t *testing.T) {
	s, err := NewOtelSink("otels://collector:4318")
	if err != nil {
		t.Fatalf("NewOtelSink: %v", err)
	}
	if !strings.HasPrefix(s.endpoint, "https://") {
		t.Fatalf("otels:// 應走 https,endpoint = %q", s.endpoint)
	}
	s2, err := NewOtelSink("otel://collector:4318")
	if err != nil {
		t.Fatalf("NewOtelSink: %v", err)
	}
	if !strings.HasPrefix(s2.endpoint, "http://") {
		t.Fatalf("otel:// 應走 http,endpoint = %q", s2.endpoint)
	}
}

func TestParseSinkRecognizesOtel(t *testing.T) {
	s, err := ParseSink("otel://localhost:4318")
	if err != nil {
		t.Fatalf("ParseSink(otel://...): %v", err)
	}
	if _, ok := s.(*OtelSink); !ok {
		t.Fatalf("應回傳 *OtelSink,得到 %T", s)
	}
}
