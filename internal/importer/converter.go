package importer

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ramplio/ramplio/internal/scenarios"
	"gopkg.in/yaml.v3"
)

// Options controls how a HAR file is converted to a scenario.
type Options struct {
	Filter   bool          // Filter static assets (default true)
	Duration time.Duration // Total test duration; 0 uses default stages
}

// DefaultOptions returns the default conversion options.
func DefaultOptions() Options {
	return Options{Filter: true}
}

// Convert parses a HAR file at path and returns scenario YAML bytes.
func Convert(path string, opts Options) ([]byte, error) {
	har, err := parseHARFile(path)
	if err != nil {
		return nil, err
	}
	return convertEntries(har.Log.Entries, opts, path)
}

// ConvertBytes parses HAR bytes and returns scenario YAML bytes.
func ConvertBytes(data []byte, opts Options, name string) ([]byte, error) {
	har, err := parseHAR(data)
	if err != nil {
		return nil, err
	}
	return convertEntries(har.Log.Entries, opts, name)
}

func convertEntries(entries []harEntry, opts Options, sourceName string) ([]byte, error) {
	if opts.Filter {
		entries = filterEntries(entries)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no API requests found in HAR (try --no-filter to include all entries)")
	}
	sc := buildScenario(entries, opts, sourceName)
	return yaml.Marshal(sc)
}

func buildScenario(entries []harEntry, opts Options, sourceName string) scenarios.Scenario {
	sc := scenarios.Scenario{
		Name:   scenarioName(sourceName),
		Stages: buildStages(opts.Duration),
	}

	preAuthIdx, preAuthVar, preAuthPath, preAuthRaw := findPreAuthToken(entries)
	loginIdx := findLoginEntry(entries)
	var captureVar, tokenPath string
	if loginIdx >= 0 {
		tokenPath = extractTokenPath(entries[loginIdx].Response.Content.Text)
		captureVar = "token"
	}

	sc.Steps = make([]scenarios.Step, 0, len(entries))
	for i, e := range entries {
		step := entryToStep(e)

		// Pre-auth token provider step (e.g. GET /csrf)
		if i == preAuthIdx {
			step.Capture = &scenarios.Capture{
				Values: map[string]string{preAuthVar: preAuthPath},
			}
		}

		// Replace pre-auth token literal with template variable in steps that follow
		if preAuthIdx >= 0 && i > preAuthIdx && preAuthRaw != "" {
			if step.Body != "" {
				step.Body = strings.ReplaceAll(step.Body, preAuthRaw, "{{capture."+preAuthVar+"}}")
			}
			if strings.Contains(step.URL, preAuthRaw) {
				step.URL = strings.ReplaceAll(step.URL, preAuthRaw, "{{capture."+preAuthVar+"}}")
			}
		}

		// JWT login capture
		if i == loginIdx {
			step.Capture = &scenarios.Capture{
				Values: map[string]string{captureVar: tokenPath},
			}
		} else if loginIdx >= 0 && i > loginIdx {
			if rawBearer := extractBearerFromHeaders(step.Headers); rawBearer != "" {
				delete(step.Headers, canonicalAuthKey(step.Headers))
				bearer := "{{capture." + captureVar + "}}"
				step.Auth = &scenarios.Auth{Bearer: &bearer}
			}
		}

		sc.Steps = append(sc.Steps, step)
	}
	return sc
}

func entryToStep(e harEntry) scenarios.Step {
	step := scenarios.Step{
		Name:   stepName(e),
		Method: strings.ToUpper(e.Request.Method),
		URL:    e.Request.URL,
		Assertions: &scenarios.Assertions{
			Status: scenarios.StatusClass("2xx"),
		},
	}
	if h := extractHeaders(e.Request.Headers); len(h) > 0 {
		step.Headers = h
	}
	if e.Request.PostData != nil && e.Request.PostData.Text != "" {
		step.Body = e.Request.PostData.Text
	}
	return step
}

// extractBearerFromHeaders returns the Authorization header value if it is a Bearer token.
func extractBearerFromHeaders(headers map[string]string) string {
	for _, v := range headers {
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			return v
		}
	}
	return ""
}

// canonicalAuthKey returns the key in the headers map that holds the Authorization value.
func canonicalAuthKey(headers map[string]string) string {
	for k := range headers {
		if strings.EqualFold(k, "authorization") {
			return k
		}
	}
	return "Authorization"
}

var skipHeaders = map[string]bool{
	"host":            true,
	"content-length":  true,
	"connection":      true,
	"accept-encoding": true,
	"user-agent":      true,
	"cache-control":   true,
	"pragma":          true,
	"cookie":          true, // handled automatically by the per-VU cookie jar
}

func extractHeaders(headers []harHeader) map[string]string {
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		if skipHeaders[strings.ToLower(h.Name)] {
			continue
		}
		out[h.Name] = h.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stepName(e harEntry) string {
	u, err := url.Parse(e.Request.URL)
	if err != nil {
		return strings.ToUpper(e.Request.Method) + " " + e.Request.URL
	}
	return strings.ToUpper(e.Request.Method) + " " + u.Path
}

func scenarioName(source string) string {
	base := source
	if idx := strings.LastIndexByte(base, '/'); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := strings.LastIndexByte(base, '.'); idx >= 0 {
		base = base[:idx]
	}
	return base
}

func buildStages(total time.Duration) []scenarios.Stage {
	if total == 0 {
		return []scenarios.Stage{
			{DurationRaw: "30s", Target: 10},
			{DurationRaw: "60s", Target: 10},
			{DurationRaw: "30s", Target: 0},
		}
	}
	ramp := total / 4
	if ramp < time.Second {
		ramp = time.Second
	}
	hold := total - 2*ramp
	if hold < time.Second {
		hold = time.Second
	}
	return []scenarios.Stage{
		{DurationRaw: formatDuration(ramp), Target: 10},
		{DurationRaw: formatDuration(hold), Target: 10},
		{DurationRaw: formatDuration(ramp), Target: 0},
	}
}

func formatDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
