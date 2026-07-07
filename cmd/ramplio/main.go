package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version 由建置時以 -ldflags "-X main.version=..." 注入(Makefile 與
// GoReleaser 皆循此路徑);未注入時保留 dev 以區別正式發布的 binary。
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "ramplio",
	Short:   "回答你的服務撐得住多少人 — 容量探測與壓力測試工具",
	Long:    "Ramplio 給網址就能自動探測 HTTP 服務的容量上限，輸出白話容量報告；量測準確度可用內建 mock-server 注入已知延遲自行驗證。也支援 YAML 多階段情境、登入流程與分散式壓測。",
	Version: buildVersion(),
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
	rootCmd.AddCommand(newVerifyCmd())
	rootCmd.AddCommand(newStopCmd())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
