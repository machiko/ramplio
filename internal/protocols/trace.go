package protocols

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptrace"
	"time"
)

// Trace breaks one request's latency into connection phases. It is captured only
// on demand (pre-flight diagnostics) via ExecuteTraced — the measurement hot path
// Execute() does not pay for tracing, so phase capture never distorts load tests.
type Trace struct {
	DNS     time.Duration // DNS resolution
	Connect time.Duration // TCP connect
	TLS     time.Duration // TLS handshake
	TTFB    time.Duration // request start → first response byte
	Total   time.Duration // request start → response fully read
	Reused  bool          // served from a pooled keep-alive connection (no DNS/connect/TLS)
}

// ExecuteTraced runs a single request with httptrace instrumentation, returning
// the phase breakdown alongside the normal result. For diagnostics only.
func (e *HTTPExecutor) ExecuteTraced(ctx context.Context, req Request) (Result, Trace) {
	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bodyReader)
	if err != nil {
		return Result{Error: err}, Trace{}
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	var dnsStart, connectStart, tlsStart time.Time
	var tr Trace
	start := time.Now()
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { tr.DNS = time.Since(dnsStart) },
		ConnectStart:         func(_, _ string) { connectStart = time.Now() },
		ConnectDone:          func(_, _ string, _ error) { tr.Connect = time.Since(connectStart) },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { tr.TLS = time.Since(tlsStart) },
		GotConn:              func(info httptrace.GotConnInfo) { tr.Reused = info.Reused },
		GotFirstResponseByte: func() { tr.TTFB = time.Since(start) },
	}
	httpReq = httpReq.WithContext(httptrace.WithClientTrace(httpReq.Context(), trace))

	resp, err := e.client.Do(httpReq)
	if err != nil {
		tr.Total = time.Since(start)
		return Result{Error: err, Latency: tr.Total}, tr
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	tr.Total = time.Since(start)

	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			headers[http.CanonicalHeaderKey(k)] = vs[0]
		}
	}
	return Result{
		StatusCode:      resp.StatusCode,
		Latency:         tr.Total,
		BytesRead:       int64(len(body)),
		Body:            body,
		ResponseHeaders: headers,
		RawSetCookies:   resp.Header["Set-Cookie"],
	}, tr
}
