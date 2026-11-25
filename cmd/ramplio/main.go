package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// version 由建置時以 -ldflags "-X main.version=..." 注入(Makefile 與
// GoReleaser 皆循此路徑);未注入時保留 dev 以區別正式發布的 binary。
var version = "dev"

// rootCmd is the process-wide command tree; tests build their own via newRootCmd.
var rootCmd = newRootCmd()

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "ramplio",
		Short:   "回答你的服務撐得住多少人 — 容量探測與壓力測試工具",
		Long:    "Ramplio 給網址就能自動探測 HTTP 服務的容量上限，輸出白話容量報告；量測準確度可用內建 mock-server 注入已知延遲自行驗證。也支援 YAML 多階段情境、登入流程與分散式壓測。",
		Version: buildVersion(),
		// Any positional argument that isn't a subcommand goes to the smart
		// entry (URL / curl / - / file). Unknown words are rejected there with
		// a subcommand suggestion, so typos never start traffic.
		Args: cobra.ArbitraryArgs,
		// SuggestionsFor (used by the smart entry) does not apply cobra's
		// default distance by itself, so set the same value explicitly.
		SuggestionsMinimumDistance: 2,
		// With no argument, show a friendly front door instead of dumping the
		// full command help. --help and --version are handled by cobra before
		// RunE and are unaffected.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				printWelcome(cmd.OutOrStdout())
				return nil
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := runSmartEntry(ctx, cmd, args, defaultSmartEntryEnv(cmd)); err != nil {
				cmd.SilenceUsage = true // the message already explains what to do
				return err
			}
			return nil
		},
	}
	root.AddCommand(
		newRunCmd(),
		newWorkerCmd(),
		newDiscoverCmd(),
		newImportCmd(),
		newInitCmd(),
		newValidateCmd(),
		newReportCmd(),
		newCompareCmd(),
		newMockServerCmd(),
		newVerifyCmd(),
		newStopCmd(),
	)
	return root
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
