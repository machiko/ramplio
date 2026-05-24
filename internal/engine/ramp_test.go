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

	status200 := 200
	col := metrics.NewCollector(2)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{{Duration: 200 * time.Millisecond, Target: 2}},
		Steps: []engine.RampStep{
			{
				Request:    protocols.Request{Method: http.MethodGet, URL: s.URL},
				Assertions: &scenarios.Assertions{Status: &status200},
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
