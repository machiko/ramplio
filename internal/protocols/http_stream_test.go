package protocols

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// dropAfter 送出 prefix(並 flush)後直接切斷連線,模擬串流中途斷線。
// contentLength > 0 時宣告該長度(非串流的截斷情境)。
func dropAfter(t *testing.T, prefix string, contentLength int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentLength > 0 {
			w.Header().Set("Content-Length", strconv.Itoa(contentLength))
		} else {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		_, _ = w.Write([]byte(prefix))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 串流中途斷線是量測事件,不是一次「偏快的 200 成功」:必須回報錯誤,
// 狀態碼歸零走連線層分類(保留 200 會被歸成斷言失敗),TTFT 不記。
func TestHTTPExecutorStream_DisconnectMidStreamIsError(t *testing.T) {
	srv := dropAfter(t, "data: first\n\n", 0)

	res := NewHTTPExecutor(DefaultHTTPConfig()).Execute(context.Background(), Request{
		Method: "GET", URL: srv.URL, Stream: true,
	})

	if res.Error == nil {
		t.Fatal("串流中途斷線應回報錯誤,卻被記為成功")
	}
	if res.StatusCode != 0 {
		t.Errorf("傳輸失敗的狀態碼應歸零走連線層分類,得到 %d", res.StatusCode)
	}
	if res.TTFT == 0 {
		t.Error("首段已到達是量到的事實:截斷的串流仍應回報 TTFT(錯誤另由 Error 計)")
	}
}

// 中止壓測時正在進行的串流必須以 context.Canceled 回報,
// engine 才會依既有規則不計入樣本(而不是記成一筆成功)。
func TestHTTPExecutorStream_CancelMidStreamIsCanceled(t *testing.T) {
	srv := newSSEServer(t, 10*time.Millisecond, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)

	res := NewHTTPExecutor(DefaultHTTPConfig()).Execute(ctx, Request{
		Method: "GET", URL: srv.URL, Stream: true,
	})

	if !errors.Is(res.Error, context.Canceled) {
		t.Fatalf("中止中的串流應回報 context.Canceled,得到 %v", res.Error)
	}
	if res.Latency > time.Second {
		t.Errorf("取消後應立即返回,卻等了 %v", res.Latency)
	}
}

// 非串流回應 body 被截斷(宣告 100 bytes 只收到 10)同樣是錯誤。
func TestHTTPExecutorNonStream_TruncatedBodyIsError(t *testing.T) {
	srv := dropAfter(t, "0123456789", 100)

	res := NewHTTPExecutor(DefaultHTTPConfig()).Execute(context.Background(), Request{
		Method: "GET", URL: srv.URL,
	})

	if res.Error == nil {
		t.Fatal("body 截斷應回報錯誤,卻被記為成功")
	}
	if res.StatusCode != 0 {
		t.Errorf("狀態碼應歸零,得到 %d", res.StatusCode)
	}
}
