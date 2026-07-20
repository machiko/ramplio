// Package scenariogen builds ramplio scenario YAML and companion data-file CSV
// content from structured wizard inputs. It is the single source of generation
// logic shared by the CLI `init` wizard and the dashboard scenario generator, so
// the two surfaces can never drift apart.
package scenariogen

import (
	"fmt"
	"strconv"
	"strings"
)

// Auth describes how a generated scenario authenticates.
type Auth struct {
	Kind          string // "cookie" | "jwt" | ""
	CSVFile       string
	CookieName    string
	LoginPath     string
	EmailField    string
	PasswordField string
	LoginEmail    string
	LoginPass     string
	TokenPath     string
}

// Step declares one request step in a generated scenario.
type Step struct {
	Name       string
	Path       string
	Method     string
	Body       string
	StatusCode string
	PauseMs    int
}

// GenerateYAML renders a complete scenario YAML document from the wizard inputs.
func GenerateYAML(
	name, baseURL string,
	auth Auth,
	steps []Step,
	vus int,
	duration, shape string,
	errPctStr, p95Str string,
	dataFile string,
) string {
	var b strings.Builder

	b.WriteString("name: " + yq(name) + "\n\n")
	b.WriteString("vars:\n")
	b.WriteString("  base_url: " + yq(baseURL) + "\n")

	// Cookie auth owns vars_from (session CSV). When it is absent, a generated
	// data file can claim vars_from for data-driven parameters instead — the two
	// never coexist, since a scenario has a single vars_from source.
	switch {
	case auth.Kind == "cookie":
		b.WriteString("\nvars_from:\n")
		b.WriteString("  file: " + auth.CSVFile + "\n")
		b.WriteString("  mode: sequential\n")
	case dataFile != "":
		b.WriteString("\nvars_from:\n")
		b.WriteString("  file: " + dataFile + "\n")
		b.WriteString("  mode: random\n")
	}

	b.WriteString("\n")
	b.WriteString(stagesYAML(vus, duration, shape))

	if auth.Kind == "jwt" {
		loginBody := fmt.Sprintf(`{%q: %q, %q: %q}`,
			auth.EmailField, auth.LoginEmail, auth.PasswordField, auth.LoginPass)
		b.WriteString("\nsetup:\n")
		b.WriteString("  - name: 登入取得 JWT\n")
		b.WriteString("    method: POST\n")
		b.WriteString("    url: \"{{vars.base_url}}" + auth.LoginPath + "\"\n")
		b.WriteString("    headers:\n")
		b.WriteString("      Content-Type: application/json\n")
		b.WriteString("    body: '" + loginBody + "'\n")
		b.WriteString("    assertions:\n")
		b.WriteString("      status: 200\n")
		b.WriteString("    capture:\n")
		b.WriteString("      jwt: \"" + auth.TokenPath + "\"\n")
	}

	b.WriteString("\nsteps:\n")
	for _, s := range steps {
		b.WriteString("  - name: " + yq(s.Name) + "\n")
		b.WriteString("    method: " + s.Method + "\n")
		b.WriteString("    url: \"{{vars.base_url}}" + s.Path + "\"\n")

		switch auth.Kind {
		case "cookie":
			b.WriteString("    headers:\n")
			b.WriteString("      Cookie: \"{{data.session_cookie}}\"\n")
			if s.Body != "" {
				b.WriteString("      Content-Type: application/json\n")
			}
		case "jwt":
			b.WriteString("    auth:\n")
			b.WriteString("      bearer: \"{{capture.jwt}}\"\n")
			if s.Body != "" {
				b.WriteString("    headers:\n")
				b.WriteString("      Content-Type: application/json\n")
			}
		default:
			if s.Body != "" {
				b.WriteString("    headers:\n")
				b.WriteString("      Content-Type: application/json\n")
			}
		}

		if s.Body != "" {
			b.WriteString("    body: '" + s.Body + "'\n")
		}

		b.WriteString("    assertions:\n")
		b.WriteString("      status: " + s.StatusCode + "\n")

		if s.PauseMs > 0 {
			fmt.Fprintf(&b, "    pause: %dms\n", s.PauseMs)
		}
	}

	if errPctStr != "" || p95Str != "" {
		b.WriteString("\nthresholds:\n")
		if errPctStr != "" {
			b.WriteString("  error_rate_pct: " + errPctStr + "\n")
		}
		if p95Str != "" {
			b.WriteString("  p95_ms: " + p95Str + "\n")
		}
	}

	return b.String()
}

func stagesYAML(vus int, duration, shape string) string {
	var b strings.Builder
	b.WriteString("stages:\n")
	vuStr := strconv.Itoa(vus)

	switch shape {
	case "spike":
		b.WriteString("  - duration: 10s\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: 30s\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: 20s\n    target: 0\n")
	case "soak":
		b.WriteString("  - duration: 1m\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: " + duration + "\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: 30s\n    target: 0\n")
	default: // steady
		b.WriteString("  - duration: 30s\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: " + duration + "\n    target: " + vuStr + "\n")
		b.WriteString("  - duration: 30s\n    target: 0\n")
	}
	return b.String()
}

// yq wraps a YAML string value in quotes when it contains special characters.
func yq(s string) string {
	if strings.ContainsAny(s, `:#{}&*!|>'"%@`) || strings.Contains(s, "  ") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
