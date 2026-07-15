package protocols

import (
	"context"
	"time"
)

type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
	// Stream 為 true 時以串流方式讀取回應並量測 TTFT(step 宣告 stream: sse)。
	Stream bool
}

type Result struct {
	StatusCode      int
	Latency         time.Duration
	BytesRead       int64
	Error           error
	Body            []byte
	ResponseHeaders map[string]string
	RawSetCookies   []string
	// TTFT(time to first token)是首個 body chunk 到達的時刻——串流回應
	// 的使用者體感由它決定。僅 Stream 請求記錄;0 = 不適用。
	// 注意 TTFT ≠ TTFB:TTFB 是 HTTP header 到達,TTFT 是第一段內容。
	TTFT time.Duration
}

type Executor interface {
	Execute(ctx context.Context, req Request) Result
}
