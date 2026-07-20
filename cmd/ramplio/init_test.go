package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/machiko/ramplio/v3/internal/scenariogen"
)

// scannerOf builds a stdin scanner from scripted answer lines so the interactive
// wizard helpers can be driven deterministically in tests.
func scannerOf(lines ...string) *bufio.Scanner {
	return bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

// An explicit start of 0 entered in the wizard must reach the DataColumn as
// {Start:0, StartSet:true}, not be lost to the int zero value.
func TestCollectDataColumns_StartZeroHonored(t *testing.T) {
	cols := collectDataColumns(scannerOf("id", "1", "0", "n"))
	if len(cols) != 1 {
		t.Fatalf("expected 1 column, got %d", len(cols))
	}
	if !cols[0].StartSet || cols[0].Start != 0 {
		t.Errorf("Start = %d StartSet = %v, want 0 / true", cols[0].Start, cols[0].StartSet)
	}
}

// A duplicate field name is re-prompted rather than accepted, so the generated
// CSV never carries a duplicate header.
func TestCollectDataColumns_RejectsDuplicateName(t *testing.T) {
	cols := collectDataColumns(scannerOf("id", "1", "1", "y", "id", "name2", "2", "n"))
	if len(cols) != 2 {
		t.Fatalf("expected 2 columns after re-prompt, got %d", len(cols))
	}
	if cols[0].Name != "id" || cols[1].Name != "name2" {
		t.Errorf("names = %q,%q, want id,name2", cols[0].Name, cols[1].Name)
	}
}

// Cookie auth owns vars_from, so the data-file step must skip without consuming
// any input.
func TestPromptDataFileConfig_CookieSkips(t *testing.T) {
	cols, file, rows := promptDataFileConfig(scannerOf(), scenariogen.Auth{Kind: "cookie"})
	if cols != nil || file != "" || rows != 0 {
		t.Errorf("cookie auth should opt out, got cols=%v file=%q rows=%d", cols, file, rows)
	}
}

func TestPromptDataFileConfig_OptOut(t *testing.T) {
	cols, file, rows := promptDataFileConfig(scannerOf("n"), scenariogen.Auth{})
	if cols != nil || file != "" || rows != 0 {
		t.Errorf("declining should opt out, got cols=%v file=%q rows=%d", cols, file, rows)
	}
}

func TestPromptDataFileConfig_RowsClamp(t *testing.T) {
	_, file, rows := promptDataFileConfig(
		scannerOf("y", "id", "1", "1", "n", "9999999", "data.csv"), scenariogen.Auth{})
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
		scannerOf("y", "kw", "2", "n", "0", "mydata"), scenariogen.Auth{})
	if rows != defaultDataRows {
		t.Errorf("rows = %d, want fallback %d", rows, defaultDataRows)
	}
	if file != "mydata.csv" {
		t.Errorf("file = %q, want mydata.csv", file)
	}
}

func TestPromptDataFileConfig_KeepsUppercaseCSVSuffix(t *testing.T) {
	_, file, _ := promptDataFileConfig(
		scannerOf("y", "kw", "2", "n", "50", "REPORT.CSV"), scenariogen.Auth{})
	if file != "REPORT.CSV" {
		t.Errorf("file = %q, want REPORT.CSV unchanged", file)
	}
}
