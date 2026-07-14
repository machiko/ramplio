package protocols

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWSEchoServer 啟動一個 echo WebSocket 伺服器;onHandshake 可檢視握手請求。
// 每個連線讀一則訊息並原樣回覆;客戶端沒發訊息時回覆 greeting。
func newWSEchoServer(t *testing.T, greeting string, onHandshake func(r *http.Request)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onHandshake != nil {
			onHandshake(r)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		if greeting != "" {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(greeting))
			return
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, msg)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wsURL 把 httptest 的 http:// 位址轉為 ws://。
func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestWSExecutorEchoRoundTrip(t *testing.T) {
	srv := newWSEchoServer(t, "", nil)

	res := NewWSExecutor().Execute(context.Background(), Request{
		URL:  wsURL(srv),
		Body: []byte("ping"),
	})

	require.NoError(t, res.Error)
	assert.Equal(t, 101, res.StatusCode, "握手成功應回報 Switching Protocols")
	assert.Equal(t, "ping", string(res.Body), "echo 伺服器應原樣回覆")
	assert.Equal(t, int64(4), res.BytesRead)
	assert.Greater(t, res.Latency, time.Duration(0))
}

func TestWSExecutorNoBodyStillReadsOneFrame(t *testing.T) {
	srv := newWSEchoServer(t, "hello", nil)

	res := NewWSExecutor().Execute(context.Background(), Request{URL: wsURL(srv)})

	require.NoError(t, res.Error)
	assert.Equal(t, "hello", string(res.Body), "未送 body 也應讀回一則伺服器訊息")
}

func TestWSExecutorForwardsHeadersExceptInternal(t *testing.T) {
	var gotAuth, gotExpect string
	srv := newWSEchoServer(t, "", func(r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotExpect = r.Header.Get("X-WS-Expect")
	})

	res := NewWSExecutor().Execute(context.Background(), Request{
		URL:  wsURL(srv),
		Body: []byte("hi"),
		Headers: map[string]string{
			"Authorization": "Bearer token123",
			"X-WS-Expect":   "hi", // 引擎內部 header,不可洩漏到握手
		},
	})

	require.NoError(t, res.Error)
	assert.Equal(t, "Bearer token123", gotAuth, "自訂 header 應轉發到握手")
	assert.Empty(t, gotExpect, "X-WS-Expect 是內部 header,不應轉發")
}

func TestWSExecutorExpectMatch(t *testing.T) {
	srv := newWSEchoServer(t, "", nil)

	res := NewWSExecutor().Execute(context.Background(), Request{
		URL:     wsURL(srv),
		Body:    []byte("pong wanted"),
		Headers: map[string]string{"X-WS-Expect": "pong"},
	})

	assert.NoError(t, res.Error, "回覆含期望子字串應通過")
}

func TestWSExecutorExpectMismatch(t *testing.T) {
	srv := newWSEchoServer(t, "", nil)

	res := NewWSExecutor().Execute(context.Background(), Request{
		URL:     wsURL(srv),
		Body:    []byte("actual reply"),
		Headers: map[string]string{"X-WS-Expect": "missing-token"},
	})

	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "missing-token", "錯誤訊息應點名期望字串")
	assert.Equal(t, "actual reply", string(res.Body), "不符時仍應保留回覆內容供除錯")
	assert.Equal(t, 101, res.StatusCode)
}

func TestWSExecutorDialRefusedByHTTPServer(t *testing.T) {
	// 純 HTTP 伺服器不升級連線 → 握手失敗,回報 HTTP 狀態碼。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	res := NewWSExecutor().Execute(context.Background(), Request{URL: wsURL(srv)})

	require.Error(t, res.Error)
	assert.Equal(t, http.StatusForbidden, res.StatusCode, "握手被拒應回報伺服器狀態碼")
}

func TestWSExecutorDialUnreachable(t *testing.T) {
	// 先開再關,拿到一個保證沒人監聽的 port。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := wsURL(srv)
	srv.Close()

	res := NewWSExecutor().Execute(context.Background(), Request{URL: url})

	require.Error(t, res.Error)
	assert.Equal(t, 0, res.StatusCode, "連線層失敗沒有 HTTP 狀態碼")
}

func TestWSExecutorServerClosesWithoutReply(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close() // 升級後立刻關閉,不回任何訊息
	}))
	defer srv.Close()

	res := NewWSExecutor().Execute(context.Background(), Request{URL: wsURL(srv)})

	require.Error(t, res.Error, "讀不到回覆訊息應回報錯誤")
	// 審查關裁決:握手後的傳輸失敗回報 status 0——executor 契約以
	// 「status>0 = 回應已完整到達」區分斷言失敗與連線層失敗,保留 101
	// 會讓 ClassifyError 把真正的斷線誤分類為「斷言失敗」誤導診斷。
	assert.Equal(t, 0, res.StatusCode, "exchange 未完成一輪,狀態碼應歸零走連線層分類")
}

func TestWSExpectErrorMessage(t *testing.T) {
	err := wsExpectError("needle")
	assert.Equal(t, "websocket: response did not contain expected string: needle", err.Error())
}
