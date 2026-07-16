package main

import (
	"bufio"
	"strings"
	"testing"
)

func baseStep() []wizardStep {
	return []wizardStep{{
		name: "GET /items", path: "/items", method: "GET", statusCode: "200",
	}}
}

// scannerOf builds a stdin scanner from scripted answer lines so the interactive
// wizard helpers can be driven deterministically in tests.
func scannerOf(lines ...string) *bufio.Scanner {
	return bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func TestGenerateYAML_DataFileAddsVarsFrom(t *testing.T) {
	yaml := generateYAML("t", "https://x", wizardAuth{}, baseStep(),
		10, "1m", "steady", "", "", "data.csv")

	if !strings.Contains(yaml, "vars_from:") {
		t.Fatalf("expected vars_from block, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "file: data.csv") {
		t.Errorf("expected data.csv reference, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "mode: random") {
		t.Errorf("expected random mode for data-driven params, got:\n%s", yaml)
	}
}

func TestGenerateYAML_NoDataFileNoVarsFrom(t *testing.T) {
	yaml := generateYAML("t", "https://x", wizardAuth{}, baseStep(),
		10, "1m", "steady", "", "", "")

	if strings.Contains(yaml, "vars_from:") {
		t.Errorf("did not expect vars_from without a data file, got:\n%s", yaml)
	}
}

// Cookie auth already owns vars_from (sessions.csv); a data file must not emit a
// second, conflicting vars_from block.
func TestGenerateYAML_CookieAuthDoesNotDoubleVarsFrom(t *testing.T) {
	auth := wizardAuth{kind: "cookie", csvFile: "sessions.csv", cookieName: "session"}
	yaml := generateYAML("t", "https://x", auth, baseStep(),
		10, "1m", "steady", "", "", "data.csv")

	if n := strings.Count(yaml, "vars_from:"); n != 1 {
		t.Fatalf("expected exactly 1 vars_from block, got %d:\n%s", n, yaml)
	}
	if !strings.Contains(yaml, "file: sessions.csv") {
		t.Errorf("cookie auth should keep its sessions.csv, got:\n%s", yaml)
	}
	if strings.Contains(yaml, "data.csv") {
		t.Errorf("data.csv must be suppressed under cookie auth, got:\n%s", yaml)
	}
}

// An explicit start of 0 entered in the wizard must reach the dataColumn as
// {start:0, startSet:true}, not be lost to the int zero value.
func TestCollectDataColumns_StartZeroHonored(t *testing.T) {
	cols := collectDataColumns(scannerOf("id", "1", "0", "n"))
	if len(cols) != 1 {
		t.Fatalf("expected 1 column, got %d", len(cols))
	}
	if !cols[0].startSet || cols[0].start != 0 {
		t.Errorf("start = %d startSet = %v, want 0 / true", cols[0].start, cols[0].startSet)
	}
}

// A duplicate field name is re-prompted rather than accepted, so the generated
// CSV never carries a duplicate header.
func TestCollectDataColumns_RejectsDuplicateName(t *testing.T) {
	cols := collectDataColumns(scannerOf("id", "1", "1", "y", "id", "name2", "2", "n"))
	if len(cols) != 2 {
		t.Fatalf("expected 2 columns after re-prompt, got %d", len(cols))
	}
	if cols[0].name != "id" || cols[1].name != "name2" {
		t.Errorf("names = %q,%q, want id,name2", cols[0].name, cols[1].name)
	}
}

// Cookie auth owns vars_from, so the data-file step must skip without consuming
// any input.
func TestPromptDataFileConfig_CookieSkips(t *testing.T) {
	cols, file, rows := promptDataFileConfig(scannerOf(), wizardAuth{kind: "cookie"})
	if cols != nil || file != "" || rows != 0 {
		t.Errorf("cookie auth should opt out, got cols=%v file=%q rows=%d", cols, file, rows)
	}
}

func TestPromptDataFileConfig_OptOut(t *testing.T) {
	cols, file, rows := promptDataFileConfig(scannerOf("n"), wizardAuth{})
	if cols != nil || file != "" || rows != 0 {
		t.Errorf("declining should opt out, got cols=%v file=%q rows=%d", cols, file, rows)
	}
}

func TestPromptDataFileConfig_RowsClamp(t *testing.T) {
	_, file, rows := promptDataFileConfig(
		scannerOf("y", "id", "1", "1", "n", "9999999", "data.csv"), wizardAuth{})
	if rows != maxDataRows {
		t.Errorf("rows = %d, want clamp to %d", rows, maxDataRows)
	}
	if file != "data.csv" {
		t.Errorf("file = %q, want data.csv", file)
	}
}

// A non-positive row count falls back to the default, and a filename without a
// .csv suffix gains one (case-insensitively).
func TestPromptDataFileConfig_RowsFallbackAndFilenameSuffix(t *testing.T) {
	_, file, rows := promptDataFileConfig(
		scannerOf("y", "kw", "2", "n", "0", "mydata"), wizardAuth{})
	if rows != defaultDataRows {
		t.Errorf("rows = %d, want fallback %d", rows, defaultDataRows)
	}
	if file != "mydata.csv" {
		t.Errorf("file = %q, want mydata.csv", file)
	}
}

func TestPromptDataFileConfig_KeepsUppercaseCSVSuffix(t *testing.T) {
	_, file, _ := promptDataFileConfig(
		scannerOf("y", "kw", "2", "n", "50", "REPORT.CSV"), wizardAuth{})
	if file != "REPORT.CSV" {
		t.Errorf("file = %q, want REPORT.CSV unchanged", file)
	}
}

func TestDataParamWarnings_UndeclaredAndUnused(t *testing.T) {
	steps := []wizardStep{{path: "/search", body: `{"q":"{{data.keywrod}}"}`}}
	cols := []dataColumn{{name: "keyword", kind: colList, listValues: []string{"a"}}}

	warnings := dataParamWarnings(steps, cols)
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "keywrod") {
		t.Errorf("first warning should flag the undeclared reference, got %q", warnings[0])
	}
	if !strings.Contains(warnings[1], "keyword") {
		t.Errorf("second warning should flag the unused column, got %q", warnings[1])
	}
}

func TestDataParamWarnings_AllMatched(t *testing.T) {
	steps := []wizardStep{{path: "/users/{{data.user_id}}", method: "GET"}}
	cols := []dataColumn{{name: "user_id", kind: colIntSeq, startSet: true}}

	if warnings := dataParamWarnings(steps, cols); len(warnings) != 0 {
		t.Errorf("expected no warnings when references match declarations, got %v", warnings)
	}
}
