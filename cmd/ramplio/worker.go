package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/machiko/ramplio/v3/internal/distributed"
	"github.com/spf13/cobra"
)

func newWorkerCmd() *cobra.Command {
	var (
		addr    string
		secret  string
		tlsCert string
		tlsKey  string
	)

	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Start a load test worker process",
		Long: `Start a local worker process that participates in distributed load testing.

The worker listens for load test assignments from a coordinator process via HTTP
and executes them locally, reporting metrics back to the coordinator.

When a shared secret is set (via --secret or the RAMPLIO_WORKER_SECRET env var),
every request must carry a matching "Authorization: Bearer <secret>" header.

Example:
  Terminal 1: ramplio worker --addr :7700
  Terminal 2: ramplio worker --addr :7701
  Terminal 3: ramplio run --scenario test.yaml --worker :7700 --worker :7701`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if addr == "" {
				return fmt.Errorf("--addr flag is required (e.g., :7700)")
			}

			if secret == "" {
				secret = os.Getenv("RAMPLIO_WORKER_SECRET")
			}

			if (tlsCert == "") != (tlsKey == "") {
				return fmt.Errorf("--tls-cert and --tls-key must be provided together")
			}

			worker := distributed.NewWorker("ramplio-worker")
			worker.SetSecret(secret)
			worker.SetTLS(tlsCert, tlsKey)

			scheme := "http"
			if tlsCert != "" {
				scheme = "https"
			}
			authNote := "no auth — set --secret to protect this endpoint"
			if secret != "" {
				authNote = "auth enabled"
			}
			log.Printf("Starting worker on %s (%s, %s)", addr, scheme, authNote)

			// Handle graceful shutdown on SIGINT/SIGTERM
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

			go func() {
				<-sigChan
				log.Println("Received shutdown signal, stopping worker...")
				worker.Stop()
				os.Exit(0)
			}()

			return worker.StartHTTPServer(addr)
		},
	}

	cmd.Flags().StringVarP(&addr, "addr", "a", "", "Listen address (e.g., :7700)")
	cmd.Flags().StringVar(&secret, "secret", "", "Shared secret required on requests (or set RAMPLIO_WORKER_SECRET)")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate file (serve HTTPS; requires --tls-key)")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS private key file (requires --tls-cert)")

	return cmd
}
