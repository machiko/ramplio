package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "ramplio",
	Short:   "Developer-first HTTP stress testing tool",
	Long:    "Ramplio generates configurable load against HTTP APIs and websites, collecting real-time performance metrics.",
	Version: "1.0.0",
}

func init() {
	rootCmd.AddCommand(newRunCmd())
	rootCmd.AddCommand(newDiscoverCmd())
	rootCmd.AddCommand(newImportCmd())
	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newValidateCmd())
	rootCmd.AddCommand(newReportCmd())
	rootCmd.AddCommand(newMockServerCmd())
	rootCmd.AddCommand(newStopCmd())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
