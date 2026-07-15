package protocols

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 非 stream hot path 的守望 benchmark:stream 支援以 if/else 分支加入,
// 非 stream 請求維持原 io.ReadAll 路徑,僅多一次 bool 判斷——
// 此 benchmark 釘住 ns/op 與 allocs/op 供未來改動對照。
func BenchmarkHTTPExecutorNonStream(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	b.Cleanup(srv.Close)

	e := NewHTTPExecutor(DefaultHTTPConfig())
	req := Request{Method: "GET", URL: srv.URL}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if res := e.Execute(context.Background(), req); res.Error != nil {
			b.Fatal(res.Error)
		}
	}
}
