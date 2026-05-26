package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/engine"
	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/scenarios"
	"github.com/stretchr/testify/assert"
)

func newTestEngine(t *testing.T, serverURL string, vus int, duration time.Duration) (*engine.RampEngine, *metrics.Collector) {
	t.Helper()
	col := metrics.NewCollector(vus)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{{Duration: duration, Target: vus}},
		Steps: []engine.RampStep{{
			Name:    "GET " + serverURL,
			Request: protocols.Request{Method: http.MethodGet, URL: serverURL},
		}},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)
	return eng, col
}

func TestEngine_SendsRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eng, _ := newTestEngine(t, server.URL, 5, 300*time.Millisecond)
	sum := eng.Run(context.Background())

	assert.Greater(t, sum.Total, int64(0))
	assert.Equal(t, 0.0, sum.ErrorRate())
}

func TestEngine_RespectsVUCount(t *testing.T) {
	const wantVUs = 10
	var concurrent atomic.Int32
	var peak atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := concurrent.Add(1)
		if int32(current) > peak.Load() {
			peak.Store(int32(current))
		}
		time.Sleep(50 * time.Millisecond)
		concurrent.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	eng, _ := newTestEngine(t, server.URL, wantVUs, 500*time.Millisecond)
	eng.Run(context.Background())

	assert.LessOrEqual(t, peak.Load(), int32(wantVUs))
}

func TestEngine_StopsOnContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	eng, _ := newTestEngine(t, server.URL, 5, 10*time.Second)

	done := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("engine did not stop after context cancellation")
	}
}

func TestEngine_RecordsWallTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	const dur = 300 * time.Millisecond
	eng, _ := newTestEngine(t, server.URL, 3, dur)
	sum := eng.Run(context.Background())

	assert.InDelta(t, dur.Seconds(), sum.WallTime.Seconds(), 0.1)
}
