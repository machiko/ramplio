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

func okServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	return s
}

func rampStep(url string) engine.RampStep {
	return engine.RampStep{Request: protocols.Request{Method: http.MethodGet, URL: url}}
}

func TestRampEngine_SendsRequests(t *testing.T) {
	s := okServer(t)
	col := metrics.NewCollector(20)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 300 * time.Millisecond, Target: 5}},
		Steps:    []engine.RampStep{rampStep(s.URL)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	assert.Greater(t, sum.Total, int64(0))
	assert.Equal(t, 0.0, sum.ErrorRate())
}

func TestRampEngine_RespectsStageDuration(t *testing.T) {
	s := okServer(t)
	col := metrics.NewCollector(5)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{
			{Duration: 200 * time.Millisecond, Target: 5},
			{Duration: 200 * time.Millisecond, Target: 5},
		},
		Steps:    []engine.RampStep{rampStep(s.URL)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())
	assert.InDelta(t, 0.4, sum.WallTime.Seconds(), 0.1)
}

func TestRampEngine_NeverExceedsTargetVUs(t *testing.T) {
	const maxTarget = 8
	var concurrent atomic.Int32
	var peak atomic.Int32

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := concurrent.Add(1)
		if cur > peak.Load() {
			peak.Store(cur)
		}
		time.Sleep(20 * time.Millisecond)
		concurrent.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	col := metrics.NewCollector(maxTarget)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 500 * time.Millisecond, Target: maxTarget}},
		Steps:    []engine.RampStep{rampStep(s.URL)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	eng.Run(context.Background())
	assert.LessOrEqual(t, peak.Load(), int32(maxTarget))
}

func TestRampEngine_AssertionFailureCountsAsError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer s.Close()

	col := metrics.NewCollector(2)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{{Duration: 200 * time.Millisecond, Target: 2}},
		Steps: []engine.RampStep{
			{
				Request:    protocols.Request{Method: http.MethodGet, URL: s.URL},
				Assertions: &scenarios.Assertions{Status: scenarios.StatusExact(200)},
			},
		},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())
	assert.Greater(t, sum.ErrorRate(), 0.0)
}

func TestRampEngine_StopsOnContextCancel(t *testing.T) {
	s := okServer(t)
	col := metrics.NewCollector(5)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 30 * time.Second, Target: 5}},
		Steps:    []engine.RampStep{rampStep(s.URL)},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RampEngine did not stop after context cancellation")
	}
}

func TestRampEngine_ThinkTime_SlowsVU(t *testing.T) {
	var requestCount atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	// With 200ms think time and a 300ms test window, a single VU should complete
	// at most 2 requests (300ms / ~200ms think time).
	col := metrics.NewCollector(1)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{{Duration: 300 * time.Millisecond, Target: 1}},
		Steps: []engine.RampStep{{
			Request: protocols.Request{Method: http.MethodGet, URL: s.URL},
			Pause:   200 * time.Millisecond,
		}},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	eng.Run(context.Background())
	// Without think time a single VU would make many more requests in 300ms.
	assert.LessOrEqual(t, requestCount.Load(), int64(3))
}

func TestRampEngine_RateMode_Basic(t *testing.T) {
	s := okServer(t)

	col := metrics.NewCollector(50)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{
			{Duration: 300 * time.Millisecond, TargetRPS: 10},
			{Duration: 300 * time.Millisecond, TargetRPS: 10},
			{Duration: 300 * time.Millisecond, TargetRPS: 0},
		},
		Steps:    []engine.RampStep{{Request: protocols.Request{Method: http.MethodGet, URL: s.URL}}},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())
	assert.Greater(t, sum.Total, int64(0))
	assert.Equal(t, int64(0), sum.Errors)
	assert.Greater(t, sum.RPS(), 0.0)
}

func TestRampEngine_RateMode_StopsOnContextCancel(t *testing.T) {
	s := okServer(t)

	col := metrics.NewCollector(50)
	eng := engine.NewRamp(engine.RampConfig{
		Stages:   []scenarios.Stage{{Duration: 10 * time.Second, TargetRPS: 20}},
		Steps:    []engine.RampStep{{Request: protocols.Request{Method: http.MethodGet, URL: s.URL}}},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RateMode engine did not stop after context cancellation")
	}
}
