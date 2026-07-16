package main

import (
	"encoding/csv"
	"strings"
	"testing"
)

// parseCSV re-reads generated CSV output so tests verify structure and escaping
// through the same encoding/csv contract that scenarios.LoadDataFile relies on.
func parseCSV(t *testing.T, content string) [][]string {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatalf("generated CSV failed to parse: %v", err)
	}
	return records
}

func TestGenerateCSV_IntSeqDefaultStart(t *testing.T) {
	out, err := generateCSV([]dataColumn{{name: "id", kind: colIntSeq}}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	want := [][]string{{"id"}, {"1"}, {"2"}, {"3"}}
	assertRecords(t, got, want)
}

func TestGenerateCSV_IntSeqCustomStart(t *testing.T) {
	out, err := generateCSV([]dataColumn{{name: "uid", kind: colIntSeq, start: 100, startSet: true}}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	want := [][]string{{"uid"}, {"100"}, {"101"}}
	assertRecords(t, got, want)
}

// An explicit start of 0 must be honored, not silently coerced to 1 — otherwise
// zero-based IDs (array indices, pagination offsets) are impossible to express.
func TestGenerateCSV_IntSeqStartZero(t *testing.T) {
	out, err := generateCSV([]dataColumn{{name: "idx", kind: colIntSeq, start: 0, startSet: true}}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	want := [][]string{{"idx"}, {"0"}, {"1"}, {"2"}}
	assertRecords(t, got, want)
}

// Duplicate column names produce a duplicate CSV header, which LoadDataFile
// resolves by silently overwriting — so generateCSV must reject them outright.
func TestGenerateCSV_DuplicateColumnName(t *testing.T) {
	_, err := generateCSV([]dataColumn{
		{name: "id", kind: colIntSeq, startSet: true},
		{name: "id", kind: colEmail},
	}, 2)
	if err == nil {
		t.Fatal("expected error for duplicate column name, got nil")
	}
}

func TestGenerateCSV_Email(t *testing.T) {
	out, err := generateCSV([]dataColumn{{name: "email", kind: colEmail}}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	want := [][]string{
		{"email"},
		{"loadtest+1@example.com"},
		{"loadtest+2@example.com"},
	}
	assertRecords(t, got, want)
}

func TestGenerateCSV_ListCycles(t *testing.T) {
	out, err := generateCSV([]dataColumn{
		{name: "kw", kind: colList, listValues: []string{"a", "b"}},
	}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	want := [][]string{{"kw"}, {"a"}, {"b"}, {"a"}}
	assertRecords(t, got, want)
}

func TestGenerateCSV_Placeholder(t *testing.T) {
	out, err := generateCSV([]dataColumn{{name: "token", kind: colPlaceholder}}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	want := [][]string{{"token"}, {"<token>"}, {"<token>"}}
	assertRecords(t, got, want)
}

func TestGenerateCSV_UUIDFormatAndUniqueness(t *testing.T) {
	out, err := generateCSV([]dataColumn{{name: "req_id", kind: colUUID}}, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	if got[0][0] != "req_id" {
		t.Fatalf("header = %q, want req_id", got[0][0])
	}
	seen := map[string]bool{}
	for _, row := range got[1:] {
		v := row[0]
		if len(v) != 36 || strings.Count(v, "-") != 4 {
			t.Errorf("value %q is not a UUID", v)
		}
		if seen[v] {
			t.Errorf("duplicate UUID %q — VUs would collide", v)
		}
		seen[v] = true
	}
}

func TestGenerateCSV_MultipleColumns(t *testing.T) {
	out, err := generateCSV([]dataColumn{
		{name: "id", kind: colIntSeq},
		{name: "email", kind: colEmail},
	}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	want := [][]string{
		{"id", "email"},
		{"1", "loadtest+1@example.com"},
		{"2", "loadtest+2@example.com"},
	}
	assertRecords(t, got, want)
}

func TestGenerateCSV_EscapesSpecialChars(t *testing.T) {
	// A list value containing a comma must survive the CSV round-trip intact.
	out, err := generateCSV([]dataColumn{
		{name: "q", kind: colList, listValues: []string{"a,b", "c"}},
	}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := parseCSV(t, out)
	want := [][]string{{"q"}, {"a,b"}, {"c"}}
	assertRecords(t, got, want)
}

func TestGenerateCSV_Errors(t *testing.T) {
	tests := []struct {
		name string
		cols []dataColumn
		rows int
	}{
		{"no columns", nil, 3},
		{"zero rows", []dataColumn{{name: "id", kind: colIntSeq}}, 0},
		{"negative rows", []dataColumn{{name: "id", kind: colIntSeq}}, -1},
		{"empty list values", []dataColumn{{name: "kw", kind: colList}}, 2},
		{"blank column name", []dataColumn{{name: "", kind: colIntSeq}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := generateCSV(tt.cols, tt.rows); err == nil {
				t.Errorf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func assertRecords(t *testing.T, got, want [][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d\ngot: %v", len(got), len(want), got)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("row %d col count = %d, want %d", i, len(got[i]), len(want[i]))
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("row %d col %d = %q, want %q", i, j, got[i][j], want[i][j])
			}
		}
	}
}
