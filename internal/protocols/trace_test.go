package protocols_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/machiko/ramplio/internal/protocols"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecuteTraced_CapturesPhases verifies the diagnostic probe records the
// connection phase breakdown: a fresh connection resolves DNS/connects and a
// delayed handler shows up as time-to-first-byte ≈ the handler delay.
func TestExecuteTraced_CapturesPhases(t *testing.T) {
	const handlerDelay = 40 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(handlerDelay)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	res, tr := exec.ExecuteTraced(context.Background(), protocols.Request{Method: "GET", URL: srv.URL})

	require.NoError(t, res.Error)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.False(t, tr.Reused, "first request must open a fresh connection")
	assert.Greater(t, tr.Connect, time.Duration(0), "TCP connect should be measured")
	assert.GreaterOrEqual(t, tr.TTFB, handlerDelay, "TTFB must include the handler delay")
	assert.GreaterOrEqual(t, tr.Total, tr.TTFB, "total must be at least TTFB")
}

// TestExecuteTraced_ReusedConnection confirms a second request over keep-alive is
// flagged as reused (no DNS/connect/TLS), so the breakdown doesn't mislead.
func TestExecuteTraced_ReusedConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exec := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	req := protocols.Request{Method: "GET", URL: srv.URL}
	_, _ = exec.ExecuteTraced(context.Background(), req)
	_, tr := exec.ExecuteTraced(context.Background(), req)

	assert.True(t, tr.Reused, "second request should reuse the pooled connection")
}
