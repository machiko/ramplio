package scenariogen

import (
	"strings"
	"testing"
)

func baseStep() []Step {
	return []Step{{
		Name: "GET /items", Path: "/items", Method: "GET", StatusCode: "200",
	}}
}

func TestGenerateYAML_DataFileAddsVarsFrom(t *testing.T) {
	yaml := GenerateYAML("t", "https://x", Auth{}, baseStep(),
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
	yaml := GenerateYAML("t", "https://x", Auth{}, baseStep(),
		10, "1m", "steady", "", "", "")

	if strings.Contains(yaml, "vars_from:") {
		t.Errorf("did not expect vars_from without a data file, got:\n%s", yaml)
	}
}

// Cookie auth already owns vars_from (sessions.csv); a data file must not emit a
// second, conflicting vars_from block.
func TestGenerateYAML_CookieAuthDoesNotDoubleVarsFrom(t *testing.T) {
	auth := Auth{Kind: "cookie", CSVFile: "sessions.csv", CookieName: "session"}
	yaml := GenerateYAML("t", "https://x", auth, baseStep(),
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
