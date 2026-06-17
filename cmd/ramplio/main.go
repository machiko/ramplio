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
	// With no subcommand, show a friendly front door pointing at the three
	// primary paths instead of dumping the full command help. --help and
	// --version are handled by cobra before Run and are unaffected.
	Run: func(cmd *cobra.Command, args []string) {
		printWelcome(os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(newRunCmd())
	rootCmd.AddCommand(newWorkerCmd())
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
