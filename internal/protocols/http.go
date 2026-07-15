package protocols

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

type HTTPConfig struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
	RequestTimeout      time.Duration
	DNSCache            bool
	DNSCacheTTL         time.Duration
	// TraceContext 讓每個請求帶 W3C traceparent header,APM 可標記壓測流量。
	// 預設關閉(opt-in):逐請求成本約 63ns + 1 次配置(microbenchmark);
	// 引擎層影響小於本機 benchmark 噪音、無法定論,依「hot path 零額外成本」
	// 紅線採保守預設。使用者自帶 traceparent header 時不覆蓋。
	TraceContext bool
}

func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 1000,
		IdleConnTimeout:     90 * time.Second,
		RequestTimeout:      30 * time.Second,
		DNSCacheTTL:         60 * time.Second,
	}
}

type HTTPExecutor struct {
	client       *http.Client
	traceContext bool
}

func NewHTTPExecutor(cfg HTTPConfig) *HTTPExecutor {
	transport := &http.Transport{
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     cfg.IdleConnTimeout,
		DisableKeepAlives:   false,
	}
	if cfg.DNSCache {
		d := newDNSCacheDialer(cfg.DNSCacheTTL)
		transport.DialContext = d.DialContext
	}
	return &HTTPExecutor{
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.RequestTimeout,
		},
		traceContext: cfg.TraceContext,
	}
}

// CloseIdleConnections closes all idle HTTP keep-alive connections in the pool.
// Call this after a test run to allow goroutines associated with idle
// connections to exit promptly instead of waiting for IdleConnTimeout.
func (e *HTTPExecutor) CloseIdleConnections() {
	e.client.CloseIdleConnections()
}

// NewSession returns an Executor with an isolated cookie jar that shares the
// same underlying TCP connection pool and timeout settings. Use one session per
// virtual user so cookies are not shared across VUs.
func (e *HTTPExecutor) NewSession() *HTTPExecutor {
	jar, _ := cookiejar.New(nil)
	clone := &http.Client{
		Transport:     e.client.Transport,
		Timeout:       e.client.Timeout,
		CheckRedirect: e.client.CheckRedirect,
		Jar:           jar,
	}
	return &HTTPExecutor{client: clone, traceContext: e.traceContext}
}

func (e *HTTPExecutor) Execute(ctx context.Context, req Request) Result {
	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bodyReader)
	if err != nil {
		return Result{Error: err}
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	// 直接用正規化後的 key 操作 map,避開 Get/Set 每請求各一次的 canonicalize 成本(hot path)。
	if e.traceContext {
		if _, exists := httpReq.Header["Traceparent"]; !exists {
			httpReq.Header["Traceparent"] = []string{newTraceparent()}
		}
	}

	start := time.Now()
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return Result{Error: err, Latency: time.Since(start)}
	}
	defer func() { _ = resp.Body.Close() }()

	// 非 stream 走既有整讀路徑(hot path 零改動);stream 分塊讀以量測
	// 首 chunk 到達時刻(TTFT),兩者共用同一個 1MiB body 上限契約。
	var body []byte
	var ttft time.Duration
	if req.Stream {
		body, ttft = readStreaming(io.LimitReader(resp.Body, 1<<20), start)
	} else {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	latency := time.Since(start)

	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			headers[http.CanonicalHeaderKey(k)] = vs[0]
		}
	}

	return Result{
		StatusCode:      resp.StatusCode,
		Latency:         latency,
		BytesRead:       int64(len(body)),
		Body:            body,
		ResponseHeaders: headers,
		RawSetCookies:   resp.Header["Set-Cookie"],
		TTFT:            ttft,
	}
}

// readStreaming 分塊讀取回應:第一個非空 chunk 到達即記下 TTFT,
// 之後繼續讀到結束(或上限)。回傳完整 body 與 TTFT。
func readStreaming(r io.Reader, start time.Time) ([]byte, time.Duration) {
	var body []byte
	var ttft time.Duration
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if ttft == 0 {
				ttft = time.Since(start)
			}
			body = append(body, buf[:n]...)
		}
		if err != nil {
			return body, ttft
		}
	}
}
