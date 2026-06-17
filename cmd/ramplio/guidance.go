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
	fmt.Fprintln(w, "  Ramplio — 開發者優先的 HTTP 壓力測試工具")
	fmt.Fprintln(w, "  對網站或 API 施加可調負載，即時量測效能並產生白話報告")
	fmt.Fprintln(w, guidanceRule)
	printNextSteps(w, "\n  三條最常用的路徑：",
		nextStep{"① 開視覺面板（最推薦，全程點選操作）", "ramplio run --dashboard"},
		nextStep{"② 引導式建立測試情境（問答產生 YAML）", "ramplio init"},
		nextStep{"③ 快速測一個網址一次", "ramplio run --url https://example.com"},
	)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  看完整指令清單：ramplio --help")
	fmt.Fprintln(w, guidanceRule)
	fmt.Fprintln(w)
}
