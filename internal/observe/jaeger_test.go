package observe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 假 Jaeger query API:回傳固定 JSON,並記下收到的查詢參數。
func jaegerStub(t *testing.T, status int, body string) (*httptest.Server, *map[string]string) {
	t.Helper()
	captured := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range r.URL.Query() {
			captured[k] = v[0]
		}
		captured["path"] = r.URL.Path
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

const jaegerBody = `{
  "data": [
    {"spans": [
      {"operationName": "SELECT orders", "startTime": 1700000000000000, "duration": 85000},
      {"operationName": "GET /api/users", "startTime": 1700000000100000, "duration": 12000}
    ]},
    {"spans": [
      {"operationName": "SELECT orders", "startTime": 1700000000200000, "duration": 91000}
    ]}
  ]
}`

func TestJaegerSourceFetchSpans(t *testing.T) {
	srv, captured := jaegerStub(t, http.StatusOK, jaegerBody)
	src, err := NewJaegerSource(srv.URL, "checkout")
	if err != nil {
		t.Fatalf("NewJaegerSource: %v", err)
	}

	start := time.UnixMicro(1700000000000000)
	end := start.Add(5 * time.Minute)
	res, err := src.FetchSpans(context.Background(), start, end)
	if err != nil {
		t.Fatalf("FetchSpans: %v", err)
	}

	if len(res.Spans) != 3 {
		t.Fatalf("應攤平回 3 個 span,得到 %d", len(res.Spans))
	}
	if res.Spans[0].Operation != "SELECT orders" || res.Spans[0].Duration != 85*time.Millisecond {
		t.Fatalf("span[0] 解析錯誤: %+v", res.Spans[0])
	}
	if !res.Spans[0].StartTime.Equal(time.UnixMicro(1700000000000000)) {
		t.Fatalf("startTime 應以微秒解析: %v", res.Spans[0].StartTime)
	}
	if res.TraceCount != 2 {
		t.Fatalf("TraceCount 應為 2,得到 %d", res.TraceCount)
	}
	if res.Truncated {
		t.Fatal("2 條 trace 遠低於預設 limit,不應標記截斷")
	}

	// 查詢參數契約:Jaeger API 的 start/end 以微秒 epoch 表示;
	// limit 必須顯式設定——Jaeger 預設 20 條對統計分析嚴重欠採樣。
	if (*captured)["path"] != "/api/traces" {
		t.Fatalf("應查 /api/traces,實際 %q", (*captured)["path"])
	}
	if (*captured)["service"] != "checkout" {
		t.Fatalf("service 參數錯誤: %q", (*captured)["service"])
	}
	if (*captured)["start"] != strconv.FormatInt(start.UnixMicro(), 10) {
		t.Fatalf("start 應為微秒 epoch: %q", (*captured)["start"])
	}
	if (*captured)["end"] != strconv.FormatInt(end.UnixMicro(), 10) {
		t.Fatalf("end 應為微秒 epoch: %q", (*captured)["end"])
	}
	if (*captured)["limit"] != strconv.Itoa(defaultTraceLimit) {
		t.Fatalf("limit 應顯式設為預設值 %d,實際 %q", defaultTraceLimit, (*captured)["limit"])
	}
}

// 截斷可見化:回傳的 trace 數達到 limit 時必須標記 Truncated,
// 讓下游知道樣本可能不完整——這是「資料不足誠實回報」原則的介面層落實。
func TestJaegerSourceTruncationVisible(t *testing.T) {
	srv, captured := jaegerStub(t, http.StatusOK, jaegerBody) // 2 條 trace
	src, err := NewJaegerSource(srv.URL, "checkout", WithTraceLimit(2))
	if err != nil {
		t.Fatalf("NewJaegerSource: %v", err)
	}

	res, err := src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err != nil {
		t.Fatalf("FetchSpans: %v", err)
	}
	if !res.Truncated {
		t.Fatal("trace 數 == limit 時應標記 Truncated(可能還有更多資料)")
	}
	if (*captured)["limit"] != "2" {
		t.Fatalf("自訂 limit 應傳給 API,實際 %q", (*captured)["limit"])
	}
}

func TestJaegerSourceEmptyResult(t *testing.T) {
	srv, _ := jaegerStub(t, http.StatusOK, `{"data": []}`)
	src, _ := NewJaegerSource(srv.URL, "checkout")

	res, err := src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err != nil {
		t.Fatalf("空結果不是錯誤: %v", err)
	}
	if len(res.Spans) != 0 || res.Truncated {
		t.Fatalf("應回空結果且不標截斷,得到 %+v", res)
	}
}

func TestJaegerSourceMalformedJSON(t *testing.T) {
	srv, _ := jaegerStub(t, http.StatusOK, `{not json`)
	src, _ := NewJaegerSource(srv.URL, "checkout")

	_, err := src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err == nil {
		t.Fatal("畸形 JSON 應回傳錯誤")
	}
}

func TestJaegerSourceHTTPError(t *testing.T) {
	srv, _ := jaegerStub(t, http.StatusInternalServerError, `boom`)
	src, _ := NewJaegerSource(srv.URL, "checkout")

	_, err := src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err == nil {
		t.Fatal("HTTP 500 應回傳錯誤,不可默默當成無資料")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("錯誤應含狀態碼供除錯: %v", err)
	}
}

// 壓測工具自身不可成為記憶體瓶頸:超過回應上限要明確報錯,不可吃滿記憶體。
func TestJaegerSourceOversizedResponse(t *testing.T) {
	big := `{"data": [{"spans": [` + strings.Repeat(`{"operationName":"x","startTime":1,"duration":1},`, 200)
	big = strings.TrimSuffix(big, ",") + `]}]}`
	srv, _ := jaegerStub(t, http.StatusOK, big)
	src, err := NewJaegerSource(srv.URL, "checkout", WithMaxResponseBytes(64))
	if err != nil {
		t.Fatalf("NewJaegerSource: %v", err)
	}

	_, err = src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err == nil {
		t.Fatal("超過回應大小上限應回傳錯誤")
	}
}

func TestJaegerSourceContextCancelled(t *testing.T) {
	srv, _ := jaegerStub(t, http.StatusOK, jaegerBody)
	src, _ := NewJaegerSource(srv.URL, "checkout")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.FetchSpans(ctx, time.Now().Add(-time.Minute), time.Now()); err == nil {
		t.Fatal("已取消的 context 應回傳錯誤")
	}
}

func TestNewJaegerSourceValidation(t *testing.T) {
	if _, err := NewJaegerSource("http://host:16686", ""); err == nil {
		t.Fatal("缺 service 名稱應回傳錯誤(Jaeger 查詢必要參數)")
	}
	if _, err := NewJaegerSource("", "svc"); err == nil {
		t.Fatal("缺 base URL 應回傳錯誤")
	}
}
