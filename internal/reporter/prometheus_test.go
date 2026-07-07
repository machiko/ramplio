package reporter_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v2/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticProvider struct {
	snap reporter.LiveSnapshot
}

func (s *staticProvider) Snapshot() reporter.LiveSnapshot { return s.snap }

func startProm(t *testing.T, addr string, snap reporter.LiveSnapshot) {
	t.Helper()
	srv := reporter.NewPrometheusServer(&staticProvider{snap: snap}, addr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, srv.Start(ctx))
	time.Sleep(40 * time.Millisecond) // wait for listener ready
}

func TestPrometheusServer_MetricsEndpoint(t *testing.T) {
	snap := reporter.LiveSnapshot{
		Total:     500,
		Errors:    10,
		RPS:       82.5,
		P50:       80 * time.Millisecond,
		P99:       500 * time.Millisecond,
		ActiveVUs: 20,
		Elapsed:   30 * time.Second,
	}
	startProm(t, "127.0.0.1:19100", snap)

	resp, err := http.Get("http://127.0.0.1:19100/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	assert.Contains(t, out, "ramplio_requests_total")
	assert.Contains(t, out, "ramplio_errors_total")
	assert.Contains(t, out, "ramplio_rps")
	assert.Contains(t, out, "ramplio_latency_p99_ms")
	assert.Contains(t, out, "ramplio_active_vus")
}

func TestPrometheusServer_ZeroTotal_NoDivByZero(t *testing.T) {
	startProm(t, "127.0.0.1:19101", reporter.LiveSnapshot{Total: 0, Errors: 0})

	resp, err := http.Get("http://127.0.0.1:19101/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "ramplio_error_rate_pct 0")
}

func TestPrometheusServer_ErrorRatePct(t *testing.T) {
	startProm(t, "127.0.0.1:19102", reporter.LiveSnapshot{Total: 100, Errors: 10})

	resp, err := http.Get("http://127.0.0.1:19102/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "ramplio_error_rate_pct 10")
}

func TestPrometheusServer_GracefulShutdown(t *testing.T) {
	srv := reporter.NewPrometheusServer(&staticProvider{}, "127.0.0.1:19103")
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, srv.Start(ctx))
	time.Sleep(40 * time.Millisecond)

	cancel()
	time.Sleep(150 * time.Millisecond)

	_, err := http.Get("http://127.0.0.1:19103/metrics")
	assert.Error(t, err, "server should be unreachable after shutdown")
}

func TestPrometheusServer_ContentType(t *testing.T) {
	startProm(t, "127.0.0.1:19104", reporter.LiveSnapshot{})

	resp, err := http.Get("http://127.0.0.1:19104/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain"))
}
