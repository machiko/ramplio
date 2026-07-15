package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stream 步驟全鏈:engine 驅動 → executor 串流量測 → collector 匯總。
// 注入首段 30ms + 尾段 60ms:TTFT p50 應貼近 30ms 且顯著小於總延遲 p50。
func TestRampEngine_StreamStepRecordsTTFT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("data: first\n\n"))
		fl.Flush()
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte("data: done\n\n"))
	}))
	t.Cleanup(srv.Close)

	col := metrics.NewCollector(2)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{{Duration: 800 * time.Millisecond, Target: 2}},
		Steps: []engine.RampStep{{
			Name:    "sse step",
			Request: protocols.Request{Method: "GET", URL: srv.URL},
			Stream:  "sse",
		}},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	require.Greater(t, sum.Total, int64(0))
	assert.Equal(t, 0.0, sum.ErrorRate())
	require.True(t, sum.HasTTFT, "stream 步驟應產出 TTFT 指標")
	assert.GreaterOrEqual(t, sum.TTFTP50, 30*time.Millisecond, "TTFT 不可低於注入的首段延遲")
	assert.Less(t, sum.TTFTP50, 70*time.Millisecond, "TTFT 疑似量到完整回應")
	assert.Less(t, sum.TTFTP50, sum.P50, "TTFT 必然小於總延遲")
}

// 非 stream 步驟:HasTTFT 維持 false,行為零改動。
func TestRampEngine_PlainStepNoTTFT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	col := metrics.NewCollector(2)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{{Duration: 300 * time.Millisecond, Target: 2}},
		Steps: []engine.RampStep{{
			Name:    "plain",
			Request: protocols.Request{Method: "GET", URL: srv.URL},
		}},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	require.Greater(t, sum.Total, int64(0))
	assert.False(t, sum.HasTTFT)
}
