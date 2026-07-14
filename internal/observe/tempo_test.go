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

// 假 Tempo search API:回傳固定 JSON,記下查詢參數。
func tempoStub(t *testing.T, status int, body string) (*httptest.Server, *map[string]string) {
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

// Tempo 官方現行欄位是 spanSets(複數);spanSet(單數)已棄用。
// uint64(nano)依 protobuf-JSON 慣例以字串編碼;matched 為該組實際命中數。
const tempoBody = `{
  "traces": [
    {"traceID": "abc", "spanSets": [{"matched": 2, "spans": [
      {"name": "SELECT orders", "startTimeUnixNano": "1700000000000000000", "durationNanos": "85000000"},
      {"name": "GET /api/users", "startTimeUnixNano": "1700000000100000000", "durationNanos": "12000000"}
    ]}]},
    {"traceID": "def", "spanSets": [{"matched": 1, "spans": [
      {"name": "SELECT orders", "startTimeUnixNano": "1700000000200000000", "durationNanos": "91000000"}
    ]}]}
  ]
}`

func TestTempoSourceFetchSpans(t *testing.T) {
	srv, captured := tempoStub(t, http.StatusOK, tempoBody)
	src, err := NewTempoSource(srv.URL, "checkout")
	if err != nil {
		t.Fatalf("NewTempoSource: %v", err)
	}

	start := time.Unix(1700000000, 0)
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
	if !res.Spans[0].StartTime.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("startTime 應以奈秒字串解析: %v", res.Spans[0].StartTime)
	}
	if res.TraceCount != 2 || res.Truncated {
		t.Fatalf("TraceCount/Truncated 錯誤: %+v", res)
	}

	// 查詢契約:unix 秒、TraceQL service 過濾、limit 與 spss 皆顯式
	// (Tempo 的 spss 預設僅 3,壓測場景會系統性欠採樣)
	if (*captured)["path"] != "/api/search" {
		t.Fatalf("應查 /api/search,實際 %q", (*captured)["path"])
	}
	if !strings.Contains((*captured)["q"], `resource.service.name`) || !strings.Contains((*captured)["q"], "checkout") {
		t.Fatalf("TraceQL 應以 service 過濾: %q", (*captured)["q"])
	}
	if (*captured)["start"] != strconv.FormatInt(start.Unix(), 10) {
		t.Fatalf("start 應為 unix 秒: %q", (*captured)["start"])
	}
	if (*captured)["end"] != strconv.FormatInt(end.Unix(), 10) {
		t.Fatalf("end 應為 unix 秒: %q", (*captured)["end"])
	}
	if (*captured)["limit"] != strconv.Itoa(defaultTraceLimit) {
		t.Fatalf("limit 應顯式設定: %q", (*captured)["limit"])
	}
	if (*captured)["spss"] != strconv.Itoa(defaultSpansPerSet) {
		t.Fatalf("spss 應顯式設定(後端預設僅 3): %q", (*captured)["spss"])
	}
}

// 相容性:舊版 Tempo 只回 spanSet(單數,已棄用)時仍須能解析。
func TestTempoSourceLegacySpanSetFallback(t *testing.T) {
	legacy := `{"traces": [{"traceID": "abc", "spanSet": {"matched": 1, "spans": [
		{"name": "op", "startTimeUnixNano": "1700000000000000000", "durationNanos": "5000000"}
	]}}]}`
	srv, _ := tempoStub(t, http.StatusOK, legacy)
	src, _ := NewTempoSource(srv.URL, "checkout")

	res, err := src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err != nil {
		t.Fatalf("FetchSpans: %v", err)
	}
	if len(res.Spans) != 1 || res.Spans[0].Operation != "op" {
		t.Fatalf("舊欄位 fallback 失效: %+v", res)
	}
}

// spss 截斷可見化:matched > 實際回傳 span 數 = 該 trace 有 span 被截斷,
// 必須反映在 Truncated——欠採樣不可隱形(與 limit 同級的誠實要求)。
func TestTempoSourceSpssTruncationVisible(t *testing.T) {
	truncated := `{"traces": [{"traceID": "abc", "spanSets": [{"matched": 50, "spans": [
		{"name": "op", "startTimeUnixNano": "1700000000000000000", "durationNanos": "5000000"}
	]}]}]}`
	srv, _ := tempoStub(t, http.StatusOK, truncated)
	src, _ := NewTempoSource(srv.URL, "checkout")

	res, err := src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err != nil {
		t.Fatalf("FetchSpans: %v", err)
	}
	if !res.Truncated {
		t.Fatal("matched(50)> 回傳 span 數(1)應標記 Truncated")
	}
}

func TestTempoSourceTraceLimitTruncation(t *testing.T) {
	srv, captured := tempoStub(t, http.StatusOK, tempoBody) // 2 條 trace
	src, _ := NewTempoSource(srv.URL, "checkout", WithTempoTraceLimit(2))

	res, err := src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err != nil {
		t.Fatalf("FetchSpans: %v", err)
	}
	if !res.Truncated {
		t.Fatal("trace 數達 limit 應標記 Truncated")
	}
	if (*captured)["limit"] != "2" {
		t.Fatalf("自訂 limit 應傳給 API: %q", (*captured)["limit"])
	}
}

func TestTempoSourceEmptyAndErrors(t *testing.T) {
	srv, _ := tempoStub(t, http.StatusOK, `{"traces": []}`)
	src, _ := NewTempoSource(srv.URL, "checkout")
	res, err := src.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now())
	if err != nil || len(res.Spans) != 0 {
		t.Fatalf("空結果應為合法: res=%+v err=%v", res, err)
	}

	srvBad, _ := tempoStub(t, http.StatusOK, `{not json`)
	srcBad, _ := NewTempoSource(srvBad.URL, "checkout")
	if _, err := srcBad.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now()); err == nil {
		t.Fatal("畸形 JSON 應回傳錯誤")
	}

	srv500, _ := tempoStub(t, http.StatusInternalServerError, `boom`)
	src500, _ := NewTempoSource(srv500.URL, "checkout")
	if _, err := src500.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now()); err == nil {
		t.Fatal("HTTP 500 應回傳錯誤")
	}

	// 字串奈秒解析錯誤路徑:非數字必須報錯,不可默默略過
	srvNaN, _ := tempoStub(t, http.StatusOK, `{"traces": [{"spanSets": [{"matched": 1, "spans": [
		{"name": "op", "startTimeUnixNano": "abc", "durationNanos": "5"}
	]}]}]}`)
	srcNaN, _ := NewTempoSource(srvNaN.URL, "checkout")
	if _, err := srcNaN.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now()); err == nil {
		t.Fatal("非數字奈秒字串應回傳錯誤")
	}

	// 回應大小上限
	big := `{"traces": [` + strings.Repeat(`{"spanSets":[]},`, 100)
	big = strings.TrimSuffix(big, ",") + `]}`
	srvBig, _ := tempoStub(t, http.StatusOK, big)
	srcBig, _ := NewTempoSource(srvBig.URL, "checkout", WithTempoMaxResponseBytes(64))
	if _, err := srcBig.FetchSpans(context.Background(), time.Now().Add(-time.Minute), time.Now()); err == nil {
		t.Fatal("超過回應上限應回傳錯誤")
	}
}

func TestNewTempoSourceValidation(t *testing.T) {
	if _, err := NewTempoSource("http://host:3200", ""); err == nil {
		t.Fatal("缺 service 應報錯")
	}
	if _, err := NewTempoSource("", "svc"); err == nil {
		t.Fatal("缺 base URL 應報錯")
	}
	// TraceQL 手刻查詢:service 含特殊字元會破壞查詢語法,必須拒絕
	for _, bad := range []string{`svc"x`, "svc{y", "svc}z", `svc\w`} {
		if _, err := NewTempoSource("http://host:3200", bad); err == nil {
			t.Fatalf("service %q 含 TraceQL 特殊字元應報錯", bad)
		}
	}
}
