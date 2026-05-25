package importer

import (
	"strings"

	"github.com/tidwall/gjson"
)

const loginThreshold = 0.7

// responseContentType returns the response Content-Type value in lowercase.
func responseContentType(e harEntry) string {
	for _, h := range e.Response.Headers {
		if strings.EqualFold(h.Name, "content-type") {
			return strings.ToLower(h.Value)
		}
	}
	return ""
}

// sanitizeVarName converts a JSON field name (e.g. "_token", "csrf-token")
// into a valid template variable name by replacing non-alphanumeric chars.
func sanitizeVarName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// findPreAuthToken detects CSRF / nonce patterns: a GET request whose JSON
// response contains a string field whose value appears verbatim in a subsequent
// request body or URL. Returns (stepIndex, varName, jsonPath, rawValue) or
// (-1, "", "", "") when no match is found.
func findPreAuthToken(entries []harEntry) (int, string, string, string) {
	const minTokenLen = 20
	for i, e := range entries {
		if strings.ToUpper(e.Request.Method) != "GET" {
			continue
		}
		if !strings.Contains(responseContentType(e), "json") {
			continue
		}
		body := e.Response.Content.Text
		if body == "" {
			continue
		}

		var foundVar, foundPath, foundVal string
		gjson.Parse(body).ForEach(func(key, val gjson.Result) bool {
			if val.Type != gjson.String {
				return true
			}
			raw := val.String()
			if len(raw) < minTokenLen {
				return true
			}
			for j := i + 1; j < len(entries); j++ {
				next := entries[j]
				inBody := next.Request.PostData != nil && strings.Contains(next.Request.PostData.Text, raw)
				inURL := strings.Contains(next.Request.URL, raw)
				if inBody || inURL {
					foundVar = sanitizeVarName(key.String())
					foundPath = "$." + key.String()
					foundVal = raw
					return false
				}
			}
			return true
		})

		if foundVar != "" {
			return i, foundVar, foundPath, foundVal
		}
	}
	return -1, "", "", ""
}

func loginScore(entry harEntry) float64 {
	var score float64

	lower := strings.ToLower(entry.Request.URL)
	if strings.Contains(lower, "login") || strings.Contains(lower, "signin") || strings.Contains(lower, "auth") {
		score += 0.3
	}

	if strings.ToUpper(entry.Request.Method) == "POST" {
		score += 0.2
	}

	if entry.Response.Status >= 200 && entry.Response.Status < 300 {
		score += 0.15
	}

	body := entry.Response.Content.Text
	if body != "" {
		for _, path := range []string{"token", "access_token", "jwt", "data.token", "data.access_token"} {
			if gjson.Get(body, path).Exists() {
				score += 0.35
				break
			}
		}
	}

	return score
}

func findLoginEntry(entries []harEntry) int {
	for i, e := range entries {
		if loginScore(e) >= loginThreshold {
			return i
		}
	}
	return -1
}

func extractTokenPath(body string) string {
	for gjsonPath, jsonPath := range map[string]string{
		"token":              "$.token",
		"access_token":       "$.access_token",
		"jwt":                "$.jwt",
		"data.token":         "$.data.token",
		"data.access_token":  "$.data.access_token",
	} {
		if gjson.Get(body, gjsonPath).Exists() {
			return jsonPath
		}
	}
	return "$.token"
}
