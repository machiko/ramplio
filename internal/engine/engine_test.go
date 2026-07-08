package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
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

	// 計時斷言在全套並行 race 下會被其他套件搶 CPU 撐爆。
	// 比照 groundtruth_test.go 既有模式:上界用 driftTol 放寬(競爭只會
	// 讓 wall time 偏大)、下界每輪必守——WallTime = time.Since(start)
	// 不可能短於注入的 duration,短於就是記錄 bug,永不放寬。
	const dur = 300 * time.Millisecond
	upperTol := driftTol(100 * time.Millisecond)

	var last time.Duration
	for i := 0; i < groundTruthRounds; i++ {
		eng, _ := newTestEngine(t, server.URL, 3, dur)
		sum := eng.Run(context.Background())
		last = sum.WallTime
		if last < dur {
			t.Fatalf("wall time %v 短於注入的 duration %v——記錄 bug,非環境噪音", last, dur)
		}
		if last <= dur+upperTol {
			return
		}
		t.Logf("round %d/%d wall time 上漂(%v > %v+%v)——疑似 CPU 競爭,重試",
			i+1, groundTruthRounds, last, dur, upperTol)
	}
	t.Fatalf("連續 %d 輪皆上漂:wall=%v want ≤ %v", groundTruthRounds, last, dur+upperTol)
}
