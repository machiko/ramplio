package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ramplio/ramplio/internal/distributed"
)

func newWorkerCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Start a load test worker process",
		Long: `Start a local worker process that participates in distributed load testing.

The worker listens for load test assignments from a coordinator process via HTTP
and executes them locally, reporting metrics back to the coordinator.

Example:
  Terminal 1: ramplio worker --addr :7700
  Terminal 2: ramplio worker --addr :7701
  Terminal 3: ramplio run --scenario test.yaml --worker :7700 --worker :7701`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if addr == "" {
				return fmt.Errorf("--addr flag is required (e.g., :7700)")
			}

			worker := distributed.NewWorker("ramplio-worker")
			log.Printf("Starting worker on %s", addr)

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

	return cmd
}
