package scenarios

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/tidwall/gjson"
)

// EvalAssertions checks all assertions against the result and returns the first failure.
func EvalAssertions(a *Assertions, result protocols.Result) error {
	if a == nil {
		return nil
	}

	if a.Status != nil && !a.Status.Match(result.StatusCode) {
		return fmt.Errorf("assertion failed: expected status %s, got %d", a.Status, result.StatusCode)
	}

	if a.BodyContains != nil {
		if !strings.Contains(string(result.Body), *a.BodyContains) {
			return fmt.Errorf("assertion failed: body does not contain %q", *a.BodyContains)
		}
	}

	if a.BodyMatches != nil {
		re, err := regexp.Compile(*a.BodyMatches)
		if err != nil {
			return fmt.Errorf("assertion failed: invalid body_matches pattern %q: %w", *a.BodyMatches, err)
		}
		if !re.Match(result.Body) {
			return fmt.Errorf("assertion failed: body does not match pattern %q", *a.BodyMatches)
		}
	}

	for path, expected := range a.BodyJSON {
		actual := gjson.GetBytes(result.Body, JSONPathToGJSON(path)).String()
		if actual != expected {
			return fmt.Errorf("assertion failed: body_json %q: expected %q, got %q", path, expected, actual)
		}
	}

	for header, expected := range a.HeaderEquals {
		actual := result.ResponseHeaders[http.CanonicalHeaderKey(header)]
		if actual != expected {
			return fmt.Errorf("assertion failed: header %q: expected %q, got %q", header, expected, actual)
		}
	}

	return nil
}

// JSONPathToGJSON converts a JSONPath expression like "$.data.id" or "$.items[0].name"
// to gjson syntax like "data.id" or "items.0.name".
func JSONPathToGJSON(path string) string {
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	// Replace [N] array indexing with .N
	var b strings.Builder
	i := 0
	for i < len(path) {
		if path[i] == '[' {
			end := strings.IndexByte(path[i:], ']')
			if end > 0 {
				b.WriteByte('.')
				b.WriteString(path[i+1 : i+end])
				i += end + 1
				continue
			}
		}
		b.WriteByte(path[i])
		i++
	}
	return b.String()
}
