package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// latencyProfile describes the server's injected per-request delay. When Fixed
// is set every request waits exactly that long. Otherwise a deterministic
// bimodal distribution applies: SlowPct percent of requests wait Slow, the rest
// wait Fast. A known distribution is what lets Ramplio be validated against
// ground truth — measured percentiles should match the injected ones.
type latencyProfile struct {
	Fixed   time.Duration
	Fast    time.Duration
	Slow    time.Duration
	SlowPct int
}

// pickLatency returns the delay for the n-th request (1-based). It is pure so it
// can be unit tested independently of the HTTP server.
func (p latencyProfile) pickLatency(n int64) time.Duration {
	if p.Fixed > 0 {
		return p.Fixed
	}
	if p.SlowPct > 0 && p.Slow > 0 && (n-1)%100 < int64(p.SlowPct) {
		return p.Slow
	}
	return p.Fast
}

// describe renders a human-readable summary of the injected latency, or "" when none.
func (p latencyProfile) describe() string {
	if p.Fixed > 0 {
		return fmt.Sprintf(" (simulated latency: %s)", p.Fixed)
	}
	if p.SlowPct > 0 && p.Slow > 0 {
		return fmt.Sprintf(" (simulated latency: %s fast, %s slow for %d%%)", p.Fast, p.Slow, p.SlowPct)
	}
	if p.Fast > 0 {
		return fmt.Sprintf(" (simulated latency: %s)", p.Fast)
	}
	return ""
}

func newMockServerCmd() *cobra.Command {
	var (
		port        int
		latency     string
		latencyFast string
		latencySlow string
		slowPct     int
	)

	cmd := &cobra.Command{
		Use:   "mock-server",
		Short: "Start a local HTTP mock server for self-stress testing",
		Long: `Starts a minimal HTTP server that responds to every request with 200 OK.
Intended as a local target for Ramplio self-stress and smoke tests.

With --latency every request waits a fixed time. With --latency-fast/--latency-slow
/--slow-pct the server injects a known bimodal distribution, so you can validate
Ramplio's measured percentiles against ground truth (see docs/accuracy.md).

Example workflow:
  ramplio mock-server --port 8080 &
  ramplio run --scenario testdata/self-stress.yaml`,
		Example: `  ramplio mock-server
  ramplio mock-server --port 8888 --latency 10ms
  ramplio mock-server --latency-fast 10ms --latency-slow 200ms --slow-pct 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := latencyProfile{SlowPct: slowPct}
			for _, f := range []struct {
				name string
				raw  string
				dest *time.Duration
			}{
				{"--latency", latency, &profile.Fixed},
				{"--latency-fast", latencyFast, &profile.Fast},
				{"--latency-slow", latencySlow, &profile.Slow},
			} {
				if f.raw == "" {
					continue
				}
				d, err := time.ParseDuration(f.raw)
				if err != nil {
					return fmt.Errorf("invalid %s %q: %w", f.name, f.raw, err)
				}
				*f.dest = d
			}
			if profile.SlowPct < 0 || profile.SlowPct > 100 {
				return fmt.Errorf("--slow-pct must be between 0 and 100, got %d", profile.SlowPct)
			}

			var reqCount atomic.Int64

			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				n := reqCount.Add(1)
				if d := profile.pickLatency(n); d > 0 {
					time.Sleep(d)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "ok",
					"n":      n,
				})
			})
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
			})
			// /login — returns a mock token for capture testing
			mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
				if d := profile.pickLatency(reqCount.Add(1)); d > 0 {
					time.Sleep(d)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{"token": "mock-token-abc123"},
				})
			})
			// /profile — requires Authorization header, returns 401 if missing
			mux.HandleFunc("/profile", func(w http.ResponseWriter, r *http.Request) {
				if d := profile.pickLatency(reqCount.Add(1)); d > 0 {
					time.Sleep(d)
				}
				if r.Header.Get("Authorization") == "" {
					http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
					return
				}
				reqCount.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"user": "test-user",
				})
			})

			srv := &http.Server{
				Addr:    fmt.Sprintf(":%d", port),
				Handler: mux,
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sigs)

			go func() {
				<-sigs
				cancel()
				shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer shutCancel()
				_ = srv.Shutdown(shutCtx)
			}()

			fmt.Printf("Mock server → http://localhost:%d%s\n", port, profile.describe())
			fmt.Println("Endpoints: /  /healthz   Press Ctrl+C to stop.")

			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return err
			}
			<-ctx.Done()
			fmt.Printf("\nServed %d requests total.\n", reqCount.Load())
			return nil
		},
	}

	cmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
	cmd.Flags().StringVar(&latency, "latency", "", "Fixed simulated per-request latency (e.g. 5ms, 10ms)")
	cmd.Flags().StringVar(&latencyFast, "latency-fast", "", "Bimodal: latency for the fast majority (e.g. 10ms)")
	cmd.Flags().StringVar(&latencySlow, "latency-slow", "", "Bimodal: latency for the slow tail (e.g. 200ms)")
	cmd.Flags().IntVar(&slowPct, "slow-pct", 0, "Bimodal: percent of requests served slow (0-100)")
	return cmd
}
