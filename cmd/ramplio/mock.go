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

func newMockServerCmd() *cobra.Command {
	var (
		port    int
		latency string
	)

	cmd := &cobra.Command{
		Use:   "mock-server",
		Short: "Start a local HTTP mock server for self-stress testing",
		Long: `Starts a minimal HTTP server that responds to every request with 200 OK.
Intended as a local target for Ramplio self-stress and smoke tests.

Example workflow:
  ramplio mock-server --port 8080 &
  ramplio run --scenario testdata/self-stress.yaml`,
		Example: `  ramplio mock-server
  ramplio mock-server --port 8888 --latency 10ms`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var delay time.Duration
			if latency != "" {
				d, err := time.ParseDuration(latency)
				if err != nil {
					return fmt.Errorf("invalid --latency %q: %w", latency, err)
				}
				delay = d
			}

			var reqCount atomic.Int64

			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if delay > 0 {
					time.Sleep(delay)
				}
				n := reqCount.Add(1)
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
				if delay > 0 {
					time.Sleep(delay)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{"token": "mock-token-abc123"},
				})
			})
			// /profile — requires Authorization header, returns 401 if missing
			mux.HandleFunc("/profile", func(w http.ResponseWriter, r *http.Request) {
				if delay > 0 {
					time.Sleep(delay)
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

			latMsg := ""
			if delay > 0 {
				latMsg = fmt.Sprintf(" (simulated latency: %s)", delay)
			}
			fmt.Printf("Mock server → http://localhost:%d%s\n", port, latMsg)
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
	cmd.Flags().StringVar(&latency, "latency", "", "Simulated per-request latency (e.g. 5ms, 10ms)")
	return cmd
}
