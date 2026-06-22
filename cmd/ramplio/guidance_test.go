package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintNextSteps(t *testing.T) {
	var buf bytes.Buffer
	printNextSteps(&buf, "下一步：",
		nextStep{"驗證格式", "ramplio validate --scenario scenario.yaml"},
		nextStep{"執行壓測", "ramplio run --scenario scenario.yaml"},
	)
	out := buf.String()

	for _, want := range []string{
		"下一步：",
		"驗證格式",
		"ramplio validate --scenario scenario.yaml",
		"執行壓測",
		"ramplio run --scenario scenario.yaml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printNextSteps output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestPrintNextSteps_NoSteps(t *testing.T) {
	var buf bytes.Buffer
	printNextSteps(&buf, "只有標題")
	if !strings.Contains(buf.String(), "只有標題") {
		t.Errorf("expected heading even with no steps, got %q", buf.String())
	}
}

func TestPrintWelcome(t *testing.T) {
	var buf bytes.Buffer
	printWelcome(&buf)
	out := buf.String()

	// The front door leads with capacity discovery (the product's sharp
	// positioning), keeps dashboard and direct run as secondary paths, and
	// surfaces the help pointer for advanced features.
	for _, want := range []string{
		"撐得住多少人",
		"ramplio discover --url https://example.com",
		"ramplio run --dashboard",
		"ramplio run --url https://example.com",
		"ramplio --help",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printWelcome output missing %q\n--- got ---\n%s", want, out)
		}
	}
}
