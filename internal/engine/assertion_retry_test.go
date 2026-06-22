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

// TestAssertionFailure_NotRetried locks in a deliberate design choice: an
// assertion failure (the server replied 200 but the body is wrong) is recorded
// as an error and NOT retried, even when Retry is configured. Retrying it would
// mask a real defect and flatter the error rate. The executor's retry only
// covers transport/status failures, which are evaluated before assertions.
func TestAssertionFailure_NotRetried(t *testing.T) {
	var hits atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("actual-body"))
	}))
	defer s.Close()

	want := "expected-but-absent"
	col := metrics.NewCollector(20)
	eng := engine.NewRamp(engine.RampConfig{
		Stages: []scenarios.Stage{{Duration: 300 * time.Millisecond, Target: 3}},
		Steps: []engine.RampStep{{
			Request:    protocols.Request{Method: http.MethodGet, URL: s.URL},
			Assertions: &scenarios.Assertions{BodyContains: &want},
			Retry:      &scenarios.RetryConfig{Count: 3},
		}},
		Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig()),
	}, col)

	sum := eng.Run(context.Background())

	assert.Greater(t, sum.Total, int64(0), "expected some requests")
	// No retry: the server is hit ~once per recorded sample. If assertion failures
	// retried (Count=3) hits would be up to 4× the sample count. We allow a small
	// overage for the request in flight when the stage ends (sent but not counted).
	assert.GreaterOrEqual(t, hits.Load(), sum.Total, "server should see at least every recorded request")
	assert.Less(t, hits.Load(), sum.Total*2, "assertion failures must not trigger retries (retry would ~4× the hits)")
	// Every recorded request failed its assertion → all counted as errors.
	assert.Equal(t, sum.Total, sum.Errors, "assertion failures must be recorded as errors")
}
