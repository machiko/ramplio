package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSummary_SingleSample(t *testing.T) {
	var s Summary
	s.record(Sample{Latency: 50 * time.Millisecond, StatusCode: 200})

	assert.Equal(t, int64(1), s.Total)
	assert.Equal(t, int64(0), s.Errors)
	assert.Equal(t, 50*time.Millisecond, s.MinLatency)
	assert.Equal(t, 50*time.Millisecond, s.MaxLatency)
	assert.Equal(t, 50*time.Millisecond, s.MeanLatency())
	assert.InDelta(t, 0.0, s.ErrorRate(), 0.001)
}

func TestSummary_TrackMinMax(t *testing.T) {
	var s Summary
	s.record(Sample{Latency: 30 * time.Millisecond, StatusCode: 200})
	s.record(Sample{Latency: 10 * time.Millisecond, StatusCode: 200})
	s.record(Sample{Latency: 80 * time.Millisecond, StatusCode: 200})

	assert.Equal(t, 10*time.Millisecond, s.MinLatency)
	assert.Equal(t, 80*time.Millisecond, s.MaxLatency)
	assert.Equal(t, 40*time.Millisecond, s.MeanLatency())
}

func TestSummary_ErrorFromNonSuccessStatus(t *testing.T) {
	var s Summary
	s.record(Sample{Latency: 10 * time.Millisecond, StatusCode: 500})
	s.record(Sample{Latency: 10 * time.Millisecond, StatusCode: 200})

	assert.Equal(t, int64(2), s.Total)
	assert.Equal(t, int64(1), s.Errors)
	assert.InDelta(t, 50.0, s.ErrorRate(), 0.001)
}

func TestSummary_ErrorFromNetworkFailure(t *testing.T) {
	var s Summary
	s.record(Sample{Error: errors.New("connection refused"), StatusCode: 0})
	s.record(Sample{Latency: 20 * time.Millisecond, StatusCode: 200})

	assert.Equal(t, int64(2), s.Total)
	assert.Equal(t, int64(1), s.Errors)
}

func TestSummary_ZeroTotal(t *testing.T) {
	var s Summary
	assert.Equal(t, 0.0, s.ErrorRate())
	assert.Equal(t, time.Duration(0), s.MeanLatency())
}

func TestSummary_RPS(t *testing.T) {
	var s Summary
	for i := 0; i < 100; i++ {
		s.record(Sample{Latency: time.Millisecond, StatusCode: 200})
	}
	s.WallTime = 2 * time.Second

	assert.InDelta(t, 50.0, s.RPS(), 0.001)
}

// 101 是 WebSocket 握手成功的狀態碼(WSExecutor 回報 Switching Protocols),
// 不可被「非 2xx = 錯誤」誤傷——否則 WS 步驟的錯誤率恆為 100%。
func TestSummary_WebSocket101IsNotError(t *testing.T) {
	var s Summary
	s.record(Sample{Latency: 10 * time.Millisecond, StatusCode: 101})

	assert.Equal(t, int64(1), s.Total)
	assert.Equal(t, int64(0), s.Errors, "101(WS 握手成功)不應計為錯誤")
}
