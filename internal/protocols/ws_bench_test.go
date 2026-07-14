package protocols

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

// newBenchEchoWS 是 benchmark 用多輪 echo 伺服器(無 testing.T 依賴)。
func newBenchEchoWS(b *testing.B) *httptest.Server {
	b.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			_, msg, rErr := conn.ReadMessage()
			if rErr != nil {
				return
			}
			if wErr := conn.WriteMessage(websocket.TextMessage, msg); wErr != nil {
				return
			}
		}
	}))
	b.Cleanup(srv.Close)
	return srv
}

// A/B 對照:per_request(每次握手)vs persistent(連線重用)。
// 差異即握手(TCP+HTTP upgrade)成本,是 ws_mode: persistent 的存在理由。
func BenchmarkWSPerRequestExchange(b *testing.B) {
	// per-request 每次 dial+close 都留下 TIME_WAIT socket:macOS 預設
	// loopback ephemeral port 僅 ~16k,標準校準流程秒級就會耗盡而失敗
	// (審查關實測)。僅供固定次數 A/B 對照:-benchtime=2000x 以內執行。
	if b.N > 2000 {
		b.Skip("per-request 大 N 會耗盡 ephemeral ports;請以 -benchtime=2000x(或更小)執行")
	}
	srv := newBenchEchoWS(b)
	req := Request{URL: "ws" + srv.URL[4:], Body: []byte("ping")}
	e := NewWSExecutor()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if res := e.Execute(context.Background(), req); res.Error != nil {
			b.Fatal(res.Error)
		}
	}
}

func BenchmarkWSPersistentExchange(b *testing.B) {
	srv := newBenchEchoWS(b)
	req := Request{URL: "ws" + srv.URL[4:], Body: []byte("ping")}
	sess := NewWSExecutor().NewSession()
	defer func() { _ = sess.Close() }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if res := sess.Execute(context.Background(), req); res.Error != nil {
			b.Fatal(res.Error)
		}
	}
}
