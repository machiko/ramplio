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
// For connection reuse across a VU's lifetime, see NewSession / WSSession.
type WSExecutor struct{}

func NewWSExecutor() *WSExecutor { return &WSExecutor{} }

// NewSession returns a per-VU connection holder for ws_mode: persistent.
// Mirrors HTTPExecutor.NewSession (per-VU cookie jar) in spirit: the engine
// creates one session per VU and closes it when the VU exits.
func (e *WSExecutor) NewSession() *WSSession {
	return &WSSession{conns: make(map[string]*websocket.Conn)}
}

func (e *WSExecutor) Execute(ctx context.Context, req Request) Result {
	start := time.Now()
	conn, statusCode, err := dialWS(ctx, req)
	if err != nil {
		return Result{StatusCode: statusCode, Latency: time.Since(start), Error: err}
	}
	defer func() { _ = conn.Close() }()
	res, _ := exchangeWS(ctx, conn, req, start, statusCode)
	return res
}

// WSSession reuses one connection per URL for a VU's lifetime (ws_mode: persistent).
// Ownership contract: a session belongs to exactly one VU goroutine and must not
// be shared across goroutines — gorilla/websocket connections do not support
// concurrent writers, and per-VU isolation is what the mode is measuring.
// Transport-level failures surface as errors (a dropped connection is a real
// event the load test must record) and evict the dead connection so the next
// exchange redials; application-level failures (ws_expect mismatch) keep the
// healthy connection open.
type WSSession struct {
	conns map[string]*websocket.Conn
}

func (s *WSSession) Execute(ctx context.Context, req Request) Result {
	start := time.Now()
	conn, ok := s.conns[req.URL]
	statusCode := 101 // reused connection: handshake already done
	if !ok {
		var err error
		conn, statusCode, err = dialWS(ctx, req)
		if err != nil {
			return Result{StatusCode: statusCode, Latency: time.Since(start), Error: err}
		}
		s.conns[req.URL] = conn
	}
	res, transportDead := exchangeWS(ctx, conn, req, start, statusCode)
	if transportDead {
		_ = conn.Close()
		delete(s.conns, req.URL)
	}
	return res
}

// Close closes all held connections. The session may be reused afterwards;
// the next Execute simply redials.
func (s *WSSession) Close() error {
	for url, conn := range s.conns {
		_ = conn.Close()
		delete(s.conns, url)
	}
	return nil
}

// dialWS performs the WebSocket handshake, forwarding request headers except
// internal engine headers. Returns the handshake HTTP status (101 on success).
func dialWS(ctx context.Context, req Request) (*websocket.Conn, int, error) {
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
		return nil, status, err
	}
	statusCode := resp.StatusCode
	if statusCode == 0 {
		statusCode = 101 // Switching Protocols
	}
	return conn, statusCode, nil
}

// exchangeWS sends one text frame (if req.Body is set), reads one frame back
// and validates ws_expect. transportDead reports whether the connection is no
// longer usable (write/read failure) — expect mismatches are application-level
// and leave the connection healthy.
//
// 傳輸失敗一律回報 StatusCode 0:executor 契約以「status>0 = 回應已完整
// 到達」區分斷言失敗與連線層失敗;握手成功後斷線若保留 101,ClassifyError
// 會把真正的斷線誤分類為「斷言失敗」。
//
// gorilla 的 Read/WriteMessage 不吃 ctx 且此處不設 deadline(壓測不該替
// 使用者決定逾時);改以 AfterFunc 在 ctx 取消時關閉連線中斷阻塞 I/O——
// 否則對端握手後沉默(黑洞/掛起)會讓 VU 永久卡住,engine 無法收工。
func exchangeWS(ctx context.Context, conn *websocket.Conn, req Request, start time.Time, statusCode int) (res Result, transportDead bool) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	// 取消中斷的 exchange 以 ctx 錯誤回報:AfterFunc 關 conn 產生的是
	// websocket close error,上游的取消判定認不得,會被誤記為目標系統錯誤。
	ctxErrOr := func(err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}

	if len(req.Body) > 0 {
		if wErr := conn.WriteMessage(websocket.TextMessage, req.Body); wErr != nil {
			return Result{Latency: time.Since(start), Error: ctxErrOr(wErr)}, true
		}
	}

	_, msg, err := conn.ReadMessage()
	latency := time.Since(start)
	if err != nil {
		return Result{Latency: latency, Error: ctxErrOr(err)}, true
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
			}, false
		}
	}

	return Result{
		StatusCode: statusCode,
		Latency:    latency,
		BytesRead:  int64(len(msg)),
		Body:       msg,
	}, false
}

type wsExpectError string

func (e wsExpectError) Error() string {
	return "websocket: response did not contain expected string: " + string(e)
}
