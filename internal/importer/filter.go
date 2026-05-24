package importer

import (
	"path/filepath"
	"strings"
)

var staticExtensions = map[string]bool{
	".js":    true,
	".css":   true,
	".png":   true,
	".jpg":   true,
	".jpeg":  true,
	".gif":   true,
	".svg":   true,
	".ico":   true,
	".woff":  true,
	".woff2": true,
	".ttf":   true,
	".eot":   true,
	".map":   true,
	".webp":  true,
	".avif":  true,
}

var staticMimePrefixes = []string{
	"text/javascript",
	"application/javascript",
	"text/css",
	"image/",
	"font/",
	"application/x-font",
}

func isStaticAsset(entry harEntry) bool {
	rawURL := entry.Request.URL
	if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	ext := strings.ToLower(filepath.Ext(rawURL))
	if staticExtensions[ext] {
		return true
	}
	for _, h := range entry.Response.Headers {
		if !strings.EqualFold(h.Name, "content-type") {
			continue
		}
		ct := strings.ToLower(h.Value)
		for _, prefix := range staticMimePrefixes {
			if strings.HasPrefix(ct, prefix) {
				return true
			}
		}
		break
	}
	return false
}

func filterEntries(entries []harEntry) []harEntry {
	out := make([]harEntry, 0, len(entries))
	for _, e := range entries {
		if !isStaticAsset(e) {
			out = append(out, e)
		}
	}
	return out
}
