package reporter

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// PrometheusServer exposes live Ramplio metrics in Prometheus text format.
type PrometheusServer struct {
	provider LiveProvider
	addr     string
}

// NewPrometheusServer creates a server that listens on addr (e.g. ":9100").
func NewPrometheusServer(provider LiveProvider, addr string) *PrometheusServer {
	return &PrometheusServer{provider: provider, addr: addr}
}

// Start binds the listener and begins serving in the background.
// The server stops gracefully when ctx is cancelled.
func (p *PrometheusServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", p.handler)

	srv := &http.Server{Handler: mux}

	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return fmt.Errorf("prometheus: cannot bind %s: %w", p.addr, err)
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	go func() { _ = srv.Serve(ln) }()

	return nil
}

func (p *PrometheusServer) handler(w http.ResponseWriter, r *http.Request) {
	s := p.provider.Snapshot()

	errPct := 0.0
	if s.Total > 0 {
		errPct = float64(s.Errors) / float64(s.Total) * 100
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprint(w, metric("ramplio_requests_total", "counter", "Total HTTP requests sent", float64(s.Total)))
	fmt.Fprint(w, metric("ramplio_errors_total", "counter", "Total failed requests", float64(s.Errors)))
	fmt.Fprint(w, metric("ramplio_error_rate_pct", "gauge", "Error rate percent", errPct))
	fmt.Fprint(w, metric("ramplio_rps", "gauge", "Requests per second", s.RPS))
	fmt.Fprint(w, metric("ramplio_latency_p50_ms", "gauge", "p50 latency in ms", durMs(s.P50)))
	fmt.Fprint(w, metric("ramplio_latency_p90_ms", "gauge", "p90 latency in ms", durMs(s.P90)))
	fmt.Fprint(w, metric("ramplio_latency_p99_ms", "gauge", "p99 latency in ms", durMs(s.P99)))
	fmt.Fprint(w, metric("ramplio_mean_latency_ms", "gauge", "Mean latency in ms", durMs(s.MeanLatency)))
	fmt.Fprint(w, metric("ramplio_active_vus", "gauge", "Number of active virtual users", float64(s.ActiveVUs)))
	fmt.Fprint(w, metric("ramplio_elapsed_seconds", "gauge", "Test elapsed time in seconds", s.Elapsed.Seconds()))
}

func metric(name, typ, help string, value float64) string {
	return fmt.Sprintf("# HELP %s %s\n# TYPE %s %s\n%s %g\n",
		name, help, name, typ, name, value)
}

func durMs(d time.Duration) float64 {
	return float64(d.Milliseconds())
}
