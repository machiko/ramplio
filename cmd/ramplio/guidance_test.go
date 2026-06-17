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

	// The front door must surface all three primary paths and the help pointer.
	for _, want := range []string{
		"Ramplio",
		"ramplio run --dashboard",
		"ramplio init",
		"ramplio run --url https://example.com",
		"ramplio --help",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printWelcome output missing %q\n--- got ---\n%s", want, out)
		}
	}
}
