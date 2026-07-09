package protocols

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// WSExecutor implements Executor for WebSocket endpoints.
// Each Execute call opens a new connection, sends one text frame, reads one
// frame back, then closes the connection.  This matches the request/response
// model used by the rest of the engine and keeps the VU loop simple.
type WSExecutor struct{}

func NewWSExecutor() *WSExecutor { return &WSExecutor{} }

func (e *WSExecutor) Execute(ctx context.Context, req Request) Result {
	start := time.Now()

	// Forward request headers to the WebSocket handshake, excluding internal engine headers.
	reqHeader := make(http.Header, len(req.Headers))
	for k, v := range req.Headers {
		if k != "X-WS-Expect" {
			reqHeader.Set(k, v)
		}
	}
	dialer := &websocket.Dialer{
		Proxy:            websocket.DefaultDialer.Proxy,
		HandshakeTimeout: websocket.DefaultDialer.HandshakeTimeout,
		TLSClientConfig:  websocket.DefaultDialer.TLSClientConfig,
	}
	conn, resp, err := dialer.DialContext(ctx, req.URL, reqHeader)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return Result{StatusCode: status, Latency: time.Since(start), Error: err}
	}
	defer func() { _ = conn.Close() }()

	statusCode := resp.StatusCode
	if statusCode == 0 {
		statusCode = 101 // Switching Protocols
	}

	// Send the message body as a text frame if provided.
	if len(req.Body) > 0 {
		if err := conn.WriteMessage(websocket.TextMessage, req.Body); err != nil {
			return Result{StatusCode: statusCode, Latency: time.Since(start), Error: err}
		}
	}

	// Read one response frame.
	_, msg, err := conn.ReadMessage()
	latency := time.Since(start)
	if err != nil {
		return Result{StatusCode: statusCode, Latency: latency, Error: err}
	}

	// Validate ws_expect if set via the WSExpect request header (engine injects it).
	if expect, ok := req.Headers["X-WS-Expect"]; ok && expect != "" {
		if !strings.Contains(string(msg), expect) {
			return Result{
				StatusCode: statusCode,
				Latency:    latency,
				BytesRead:  int64(len(msg)),
				Body:       msg,
				Error:      wsExpectError(expect),
			}
		}
	}

	return Result{
		StatusCode: statusCode,
		Latency:    latency,
		BytesRead:  int64(len(msg)),
		Body:       msg,
	}
}

type wsExpectError string

func (e wsExpectError) Error() string {
	return "websocket: response did not contain expected string: " + string(e)
}
