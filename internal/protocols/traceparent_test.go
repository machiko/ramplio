package protocols

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)

func captureServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("traceparent"))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// 開啟時每個壓測請求帶 W3C traceparent,APM 能把壓測流量與 trace 關聯。
// 預設關閉(hot path 零成本紅線,opt-in 理由見 http.go 的 TraceContext 註解)。
func TestTraceparentInjectedWhenEnabled(t *testing.T) {
	srv, seen := captureServer(t)
	cfg := DefaultHTTPConfig()
	cfg.TraceContext = true
	e := NewHTTPExecutor(cfg)

	for i := 0; i < 2; i++ {
		if res := e.Execute(context.Background(), Request{Method: "GET", URL: srv.URL}); res.Error != nil {
			t.Fatalf("Execute: %v", res.Error)
		}
	}

	if len(*seen) != 2 {
		t.Fatalf("應收到 2 個請求,得到 %d", len(*seen))
	}
	for i, tp := range *seen {
		if !traceparentRe.MatchString(tp) {
			t.Fatalf("請求 %d 的 traceparent 格式錯誤: %q", i, tp)
		}
	}
	if (*seen)[0] == (*seen)[1] {
		t.Fatalf("兩個請求的 traceparent 不可相同(trace ID 必須唯一): %q", (*seen)[0])
	}
}

func TestTraceparentOffByDefault(t *testing.T) {
	srv, seen := captureServer(t)
	e := NewHTTPExecutor(DefaultHTTPConfig())

	if res := e.Execute(context.Background(), Request{Method: "GET", URL: srv.URL}); res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if (*seen)[0] != "" {
		t.Fatalf("關閉時不應注入 traceparent,得到: %q", (*seen)[0])
	}
}

func TestTraceparentUserHeaderWins(t *testing.T) {
	srv, seen := captureServer(t)
	cfgOn := DefaultHTTPConfig()
	cfgOn.TraceContext = true
	e := NewHTTPExecutor(cfgOn)
	custom := "00-11111111111111111111111111111111-2222222222222222-01"
	res := e.Execute(context.Background(), Request{
		Method: "GET", URL: srv.URL,
		Headers: map[string]string{"traceparent": custom},
	})
	if res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if (*seen)[0] != custom {
		t.Fatalf("使用者自帶的 traceparent 不可被覆蓋: %q", (*seen)[0])
	}
}

// NewSession(每 VU 一個 session)必須繼承 trace context 設定。
func TestTraceparentSessionInherits(t *testing.T) {
	srv, seen := captureServer(t)
	cfg := DefaultHTTPConfig()
	cfg.TraceContext = true
	e := NewHTTPExecutor(cfg).NewSession()

	if res := e.Execute(context.Background(), Request{Method: "GET", URL: srv.URL}); res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if !traceparentRe.MatchString((*seen)[0]) {
		t.Fatalf("session 應繼承 TraceContext=true 並注入,得到: %q", (*seen)[0])
	}
}

func BenchmarkNewTraceparent(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = newTraceparent()
	}
}
