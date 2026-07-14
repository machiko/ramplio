package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCountingEchoWS 啟動計數握手的多輪 echo 伺服器。
func newCountingEchoWS(t *testing.T, handshakes *atomic.Int32) *httptest.Server {
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
			if wErr := conn.WriteMessage(websocket.TextMessage, msg); wErr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsRampStep(url, mode string) engine.RampStep {
	return engine.RampStep{
		Name:     "ws step",
		Request:  protocols.Request{Method: "GET", URL: url, Body: []byte("ping"), Headers: map[string]string{}},
		Protocol: "websocket",
		WSMode:   mode,
	}
}

// persistent:每個 VU 只握手一次,連線在 VU 生命週期內重用——
// 這是本模式的核心量測差異(去除逐請求握手開銷)。
func TestRampEngine_WSPersistentHandshakesOncePerVU(t *testing.T) {
	var handshakes atomic.Int32
	srv := newCountingEchoWS(t, &handshakes)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	const vus = 2
	col := metrics.NewCollector(vus)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:     []scenarios.Stage{{Duration: 1200 * time.Millisecond, Target: vus}},
		Steps:      []engine.RampStep{wsRampStep(url, "persistent")},
		Executor:   protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
		WSExecutor: protocols.NewWSExecutor(),
	}, col)

	sum := eng.Run(context.Background())

	require.Greater(t, sum.Total, int64(vus*2), "VU 迴圈應完成多次 exchange")
	assert.Equal(t, 0.0, sum.ErrorRate())
	assert.Equal(t, int32(vus), handshakes.Load(),
		"persistent 模式每個 VU 應只握手一次,實際 %d 次(共 %d 次 exchange)", handshakes.Load(), sum.Total)
}

// 對照組:預設 per_request 每次 exchange 都握手——證明差異真的來自 ws_mode。
func TestRampEngine_WSPerRequestHandshakesEveryExchange(t *testing.T) {
	var handshakes atomic.Int32
	srv := newCountingEchoWS(t, &handshakes)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	col := metrics.NewCollector(2)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:     []scenarios.Stage{{Duration: 600 * time.Millisecond, Target: 2}},
		Steps:      []engine.RampStep{wsRampStep(url, "")},
		Executor:   protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
		WSExecutor: protocols.NewWSExecutor(),
	}, col)

	sum := eng.Run(context.Background())

	require.Greater(t, sum.Total, int64(0))
	// 測試結束的取消可能中斷正在進行的 exchange:已握手但依取消慣例
	// 不記入 Total,允許每個 VU 至多一次的差額。
	delta := handshakes.Load() - int32(sum.Total)
	assert.GreaterOrEqual(t, delta, int32(0), "per_request 模式每次 exchange 都應握手")
	assert.LessOrEqual(t, delta, int32(2), "握手數與 exchange 數的差額不應超過 VU 數")
}

// 注入非 *WSExecutor 的假執行器時,persistent 應安全退化為逐請求呼叫
// (fake 無連線概念;測試注入不可因 ws_mode 而炸掉)。
type fakeWSExec struct{ calls atomic.Int32 }

func (f *fakeWSExec) Execute(_ context.Context, _ protocols.Request) protocols.Result {
	f.calls.Add(1)
	// 減速:零延遲狂噴會塞爆 collector channel 丟樣本,calls==Total 就不成立
	time.Sleep(time.Millisecond)
	return protocols.Result{StatusCode: 101, Latency: time.Millisecond}
}

func TestRampEngine_WSPersistentFallsBackForFakeExecutor(t *testing.T) {
	fake := &fakeWSExec{}
	col := metrics.NewCollector(1)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:     []scenarios.Stage{{Duration: 300 * time.Millisecond, Target: 1}},
		Steps:      []engine.RampStep{wsRampStep("ws://unused.invalid/", "persistent")},
		Executor:   protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
		WSExecutor: fake,
	}, col)

	sum := eng.Run(context.Background())

	require.Greater(t, sum.Total, int64(0))
	assert.Equal(t, int32(sum.Total), fake.calls.Load(), "fake 執行器應照常收到每次呼叫")
	assert.Equal(t, 0.0, sum.ErrorRate())
}
