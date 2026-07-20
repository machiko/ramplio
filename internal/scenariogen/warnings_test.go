package scenariogen

import (
	"strings"
	"testing"
)

func TestDataParamWarnings_UndeclaredAndUnused(t *testing.T) {
	steps := []Step{{Path: "/search", Body: `{"q":"{{data.keywrod}}"}`}}
	cols := []DataColumn{{Name: "keyword", Kind: KindList, ListValues: []string{"a"}}}

	warnings := DataParamWarnings(steps, cols)
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
	steps := []Step{{Path: "/users/{{data.user_id}}", Method: "GET"}}
	cols := []DataColumn{{Name: "user_id", Kind: KindIntSeq, StartSet: true}}

	if warnings := DataParamWarnings(steps, cols); len(warnings) != 0 {
		t.Errorf("expected no warnings when references match declarations, got %v", warnings)
	}
}
