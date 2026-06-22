package main

import (
	"fmt"
	"io"
)

// guidanceRule is the visual divider shared by all CLI guidance blocks. It mirrors
// the style already used by the `init` wizard so every surface looks consistent.
const guidanceRule = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

// nextStep is a single labelled command shown in a guidance footer: a short
// human label and the exact command the user can copy-paste.
type nextStep struct {
	label string
	cmd   string
}

// printNextSteps renders a consistent "下一步" guidance block: a heading followed
// by label/command pairs. Every command reuses this so the CLI always ends with
// the same shape, giving users a clear, predictable "what to do next".
func printNextSteps(w io.Writer, heading string, steps ...nextStep) {
	fmt.Fprintf(w, "%s\n", heading)
	for _, s := range steps {
		fmt.Fprintf(w, "\n  %s\n    %s\n", s.label, s.cmd)
	}
}

// printWelcome is the friendly front door shown when `ramplio` runs with no
// arguments. It orients a first-time user toward the three primary paths instead
// of dumping the full command help.
func printWelcome(w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, guidanceRule)
	fmt.Fprintln(w, "  Ramplio — 你的服務撐得住多少人？")
	fmt.Fprintln(w, "  給網址，自動探測容量上限並給白話答案；數字你能自己驗證")
	fmt.Fprintln(w, guidanceRule)
	printNextSteps(w, "\n  從這裡開始：",
		nextStep{"① 探測容量上限（最推薦，一行回答撐多少人）", "ramplio discover --url https://example.com"},
		nextStep{"② 開視覺面板（全程點選操作）", "ramplio run --dashboard"},
		nextStep{"③ 直接壓測一個網址", "ramplio run --url https://example.com"},
	)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  進階：YAML 多階段情境、登入流程、分散式 → ramplio --help")
	fmt.Fprintln(w, guidanceRule)
	fmt.Fprintln(w)
}
