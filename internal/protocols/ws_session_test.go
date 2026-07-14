package protocols

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWSSessionEchoServer 啟動可服務多次 exchange 的 echo 伺服器:
// 每次握手計數 +1,連線內迴圈 read→write 直到客戶端關閉;
// 收到 "die" 時伺服器主動斷線(模擬連線中途死亡)。
func newWSSessionEchoServer(t *testing.T, handshakes *atomic.Int32) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		handshakes.Add(1)
		defer func() { _ = conn.Close() }()
		for {
			_, msg, rErr := conn.ReadMessage()
			if rErr != nil {
				return
			}
			if string(msg) == "die" {
				return // 不回覆直接斷線
			}
			if wErr := conn.WriteMessage(websocket.TextMessage, msg); wErr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// persistent 的核心價值:同一 VU 的多次 exchange 重用一條連線,只握手一次。
func TestWSSessionReusesConnection(t *testing.T) {
	var handshakes atomic.Int32
	srv := newWSSessionEchoServer(t, &handshakes)

	sess := NewWSExecutor().NewSession()
	defer func() { _ = sess.Close() }()

	for i := 0; i < 3; i++ {
		res := sess.Execute(context.Background(), Request{URL: wsURL(srv), Body: []byte("ping")})
		require.NoError(t, res.Error, "exchange %d", i)
		assert.Equal(t, "ping", string(res.Body))
	}
	assert.Equal(t, int32(1), handshakes.Load(), "三次 exchange 應只握手一次")
}

// 連線歸屬單一 VU:不同 session 各自握手,絕不共用。
func TestWSSessionIsolatedPerSession(t *testing.T) {
	var handshakes atomic.Int32
	srv := newWSSessionEchoServer(t, &handshakes)

	e := NewWSExecutor()
	s1, s2 := e.NewSession(), e.NewSession()
	defer func() { _ = s1.Close() }()
	defer func() { _ = s2.Close() }()

	require.NoError(t, s1.Execute(context.Background(), Request{URL: wsURL(srv), Body: []byte("a")}).Error)
	require.NoError(t, s2.Execute(context.Background(), Request{URL: wsURL(srv), Body: []byte("b")}).Error)
	assert.Equal(t, int32(2), handshakes.Load(), "兩個 session 應各自握手")
}

// 傳輸層錯誤 = 連線已死:回報錯誤、棄置連線,下次 exchange 自動重撥。
// 錯誤不可被靜默重連吞掉——斷線是壓測要量測的真實事件。
func TestWSSessionRedialsAfterTransportError(t *testing.T) {
	var handshakes atomic.Int32
	srv := newWSSessionEchoServer(t, &handshakes)

	sess := NewWSExecutor().NewSession()
	defer func() { _ = sess.Close() }()

	res := sess.Execute(context.Background(), Request{URL: wsURL(srv), Body: []byte("die")})
	require.Error(t, res.Error, "伺服器斷線必須以錯誤回報,不可靜默重連")
	assert.Equal(t, 0, res.StatusCode, "斷線屬連線層失敗,狀態碼應歸零供錯誤分類")

	res = sess.Execute(context.Background(), Request{URL: wsURL(srv), Body: []byte("ping")})
	require.NoError(t, res.Error, "斷線後的下一次 exchange 應自動重撥")
	assert.Equal(t, "ping", string(res.Body))
	assert.Equal(t, int32(2), handshakes.Load(), "斷線重撥應產生第二次握手")
}

// expect 不符是應用層失敗,連線本身健康:回報錯誤但保留連線,不重撥。
func TestWSSessionExpectMismatchKeepsConnection(t *testing.T) {
	var handshakes atomic.Int32
	srv := newWSSessionEchoServer(t, &handshakes)

	sess := NewWSExecutor().NewSession()
	defer func() { _ = sess.Close() }()

	res := sess.Execute(context.Background(), Request{
		URL:     wsURL(srv),
		Body:    []byte("ping"),
		Headers: map[string]string{"X-WS-Expect": "pong"},
	})
	require.Error(t, res.Error)

	res = sess.Execute(context.Background(), Request{URL: wsURL(srv), Body: []byte("ping")})
	require.NoError(t, res.Error)
	assert.Equal(t, int32(1), handshakes.Load(), "expect 不符不應棄置健康連線")
}

// Close 關閉全部連線;之後再 Execute 重撥(session 可安全重啟)。
func TestWSSessionCloseThenRedial(t *testing.T) {
	var handshakes atomic.Int32
	srv := newWSSessionEchoServer(t, &handshakes)

	sess := NewWSExecutor().NewSession()
	require.NoError(t, sess.Execute(context.Background(), Request{URL: wsURL(srv), Body: []byte("a")}).Error)
	require.NoError(t, sess.Close())

	require.NoError(t, sess.Execute(context.Background(), Request{URL: wsURL(srv), Body: []byte("b")}).Error)
	assert.Equal(t, int32(2), handshakes.Load())
	require.NoError(t, sess.Close())
}

// 審查關發現(HIGH):對端握手後沉默(黑洞/掛起)時,gorilla 的阻塞
// Read 不吃 ctx——ctx 取消必須能中斷 exchange,否則 VU 永久卡住、
// engine 收不了工。
func TestWSSessionCtxCancelInterruptsSilentPeer(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// 握手成功後沉默:不讀不回,等到測試結束
		select {}
	}))
	t.Cleanup(srv.Close)

	sess := NewWSExecutor().NewSession()
	defer func() { _ = sess.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan Result, 1)
	go func() { done <- sess.Execute(ctx, Request{URL: wsURL(srv), Body: []byte("ping")}) }()

	select {
	case res := <-done:
		require.Error(t, res.Error, "沉默對端 + ctx 取消應以錯誤返回")
		assert.Equal(t, 0, res.StatusCode)
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消後 Execute 仍卡住——阻塞 I/O 未被中斷")
	}
}
