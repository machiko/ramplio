package importer

import (
	"strings"

	"github.com/tidwall/gjson"
)

const loginThreshold = 0.7

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
