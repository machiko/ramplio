package protocols

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newSSEServer 模擬串流回應:先延遲 firstDelay 才送出第一個 event,
// 再延遲 restDelay 送出第二個 event 後結束——TTFT 與 TTLB 可分離驗證。
func newSSEServer(t *testing.T, firstDelay, restDelay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("httptest ResponseWriter 應支援 Flush")
			return
		}
		time.Sleep(firstDelay)
		_, _ = w.Write([]byte("data: first\n\n"))
		fl.Flush()
		time.Sleep(restDelay)
		_, _ = w.Write([]byte("data: second\n\n"))
		fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ground-truth 自證:注入已知 TTFT(50ms)與尾段延遲(100ms),
// 量到的 TTFT 必須 ≥ 注入值且遠小於總時長——低於注入值代表量測有 bug。
func TestHTTPExecutorStream_MeasuresTTFT(t *testing.T) {
	const firstDelay = 50 * time.Millisecond
	const restDelay = 100 * time.Millisecond
	srv := newSSEServer(t, firstDelay, restDelay)

	res := NewHTTPExecutor(DefaultHTTPConfig()).Execute(context.Background(), Request{
		Method: "GET", URL: srv.URL, Stream: true,
	})

	if res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if res.TTFT <= 0 {
		t.Fatal("stream 請求應記錄 TTFT")
	}
	if res.TTFT < firstDelay {
		t.Errorf("TTFT %v 低於注入的首 event 延遲 %v——量測有 bug", res.TTFT, firstDelay)
	}
	if res.TTFT > firstDelay+80*time.Millisecond {
		t.Errorf("TTFT %v 遠超注入值 %v,疑似量到了完整回應", res.TTFT, firstDelay)
	}
	if res.Latency < firstDelay+restDelay {
		t.Errorf("總時長 %v 應涵蓋兩段延遲(≥%v)", res.Latency, firstDelay+restDelay)
	}
	if !strings.Contains(string(res.Body), "first") || !strings.Contains(string(res.Body), "second") {
		t.Errorf("串流 body 應完整收齊,得到 %q", res.Body)
	}
}

// 非 stream 請求:行為與既有完全一致,TTFT 缺席(零值)。
func TestHTTPExecutorNonStream_NoTTFT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	res := NewHTTPExecutor(DefaultHTTPConfig()).Execute(context.Background(), Request{
		Method: "GET", URL: srv.URL,
	})

	if res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if res.TTFT != 0 {
		t.Errorf("非 stream 請求不應記錄 TTFT,得到 %v", res.TTFT)
	}
}

// stream 請求的 body 上限仍受 1MiB 保護(與非 stream 同一契約)。
func TestHTTPExecutorStream_RespectsBodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		big := strings.Repeat("x", 2<<20) // 2 MiB
		_, _ = w.Write([]byte(big))
	}))
	t.Cleanup(srv.Close)

	res := NewHTTPExecutor(DefaultHTTPConfig()).Execute(context.Background(), Request{
		Method: "GET", URL: srv.URL, Stream: true,
	})

	if res.Error != nil {
		t.Fatalf("Execute: %v", res.Error)
	}
	if res.BytesRead > 1<<20 {
		t.Errorf("串流讀取應受 1MiB 上限保護,實際讀了 %d bytes", res.BytesRead)
	}
}
