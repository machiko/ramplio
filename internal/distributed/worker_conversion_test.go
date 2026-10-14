package distributed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These drive a worker from scenario YAML to its exported result — the real
// distributed path — so step fields the worker's conversion drops show up as
// missing behaviour, not just as a struct field nobody reads.

// runOnWorker assigns yaml to a fresh worker, waits for the run to finish and
// returns the worker's result as a merged summary (what the coordinator sees).
func runOnWorker(t *testing.T, yaml string, vus int) metrics.Summary {
	t.Helper()
	w := NewWorker("conv-" + t.Name())
	require.NoError(t, w.Assign(context.Background(), &AssignRequest{ScenarioYAML: []byte(yaml), AssignedVUs: vus}))
	t.Cleanup(w.Stop)
	require.Eventually(t, func() bool { return w.GetState() == WorkerStateDone },
		10*time.Second, 20*time.Millisecond, "worker run did not finish")
	res := w.GetResult()
	require.NotNil(t, res)
	return metrics.MergeExports([]metrics.HistogramExport{res.Export})
}

func TestWorker_StreamStepRecordsTTFT(t *testing.T) {
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("data: first\n\n"))
		fl.Flush()
		time.Sleep(40 * time.Millisecond)
		_, _ = w.Write([]byte("data: done\n\n"))
	}))
	t.Cleanup(sse.Close)

	sum := runOnWorker(t, fmt.Sprintf(`name: worker-sse
stages:
  - duration: 10ms
    target: 2
  - duration: 500ms
    target: 2
steps:
  - name: sse
    method: GET
    url: %s
    stream: sse
`, sse.URL), 2)

	require.Positive(t, sum.Total)
	assert.Zero(t, sum.Errors)
	assert.True(t, sum.HasTTFT, "a stream: sse step run on a worker must record TTFT")
}

func TestWorker_WSPersistentReusesConnection(t *testing.T) {
	var handshakes atomic.Int32
	upgrader := websocket.Upgrader{}
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		handshakes.Add(1)
		defer func() { _ = c.Close() }()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	t.Cleanup(echo.Close)

	sum := runOnWorker(t, fmt.Sprintf(`name: worker-ws
stages:
  - duration: 10ms
    target: 1
  - duration: 300ms
    target: 1
steps:
  - name: ws echo
    method: GET
    url: %s
    protocol: websocket
    ws_message: ping
    ws_expect: ping
    ws_mode: persistent
`, "ws"+strings.TrimPrefix(echo.URL, "http")), 1)

	require.Greater(t, sum.Total, int64(2), "expected several exchanges")
	assert.Zero(t, sum.Errors)
	assert.Equal(t, int32(1), handshakes.Load(), "ws_mode: persistent must reuse one connection per VU")
}
