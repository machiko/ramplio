package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func newStopCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a running dashboard server",
		Example: `  ramplio stop
  ramplio stop --port 8080`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port)).Output()
			if err != nil || strings.TrimSpace(string(out)) == "" {
				fmt.Printf("No process found on port %d\n", port)
				return nil
			}

			pids := strings.Fields(strings.TrimSpace(string(out)))
			killArgs := append([]string{"-9"}, pids...)
			if err := exec.Command("kill", killArgs...).Run(); err != nil {
				return fmt.Errorf("kill failed: %w", err)
			}

			fmt.Printf("Stopped dashboard (port %d, PID %s)\n", port, strings.Join(pids, " "))
			return nil
		},
	}

	cmd.Flags().IntVar(&port, "port", 9999, "Dashboard port to stop")
	return cmd
}
