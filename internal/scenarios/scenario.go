package scenarios

import (
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Scenario struct {
	Name       string            `yaml:"name"`
	Stages     []Stage           `yaml:"stages"`
	Steps      []Step            `yaml:"steps"`
	Vars       map[string]string `yaml:"vars,omitempty"`
	Thresholds *Thresholds       `yaml:"thresholds,omitempty"`
	HTTP       *ScenarioHTTP     `yaml:"http,omitempty"`
}

// StatusCheck holds a status assertion: either an exact code ("200") or a
// class wildcard ("2xx", "3xx", "4xx", "5xx"). It accepts both quoted and
// unquoted YAML values so `status: 200` and `status: 2xx` both work.
type StatusCheck struct {
	raw string
}

func (s *StatusCheck) UnmarshalYAML(value *yaml.Node) error {
	s.raw = value.Value
	return nil
}

// Match reports whether code satisfies the check.
func (s *StatusCheck) Match(code int) bool {
	switch s.raw {
	case "1xx":
		return code >= 100 && code < 200
	case "2xx":
		return code >= 200 && code < 300
	case "3xx":
		return code >= 300 && code < 400
	case "4xx":
		return code >= 400 && code < 500
	case "5xx":
		return code >= 500 && code < 600
	default:
		n, err := strconv.Atoi(s.raw)
		if err != nil {
			return false
		}
		return code == n
	}
}

func (s *StatusCheck) String() string { return s.raw }

// MarshalYAML serialises the check as a plain scalar (e.g. 2xx, 200).
func (s *StatusCheck) MarshalYAML() (any, error) {
	return s.raw, nil
}

// StatusExact creates a StatusCheck that matches a single HTTP status code.
func StatusExact(code int) *StatusCheck {
	return &StatusCheck{raw: strconv.Itoa(code)}
}

// StatusClass creates a StatusCheck for a wildcard class: "2xx", "3xx", "4xx", "5xx".
func StatusClass(class string) *StatusCheck {
	return &StatusCheck{raw: class}
}

// ScenarioHTTP allows per-scenario tuning of the HTTP connection pool and timeouts.
type ScenarioHTTP struct {
	MaxIdleConns        *int `yaml:"max_idle_conns,omitempty"`
	MaxIdleConnsPerHost *int `yaml:"max_idle_conns_per_host,omitempty"`
	RequestTimeoutMs    *int `yaml:"request_timeout_ms,omitempty"`
}

type Stage struct {
	DurationRaw string        `yaml:"duration"`
	Target      int           `yaml:"target,omitempty"`
	TargetRPS   int           `yaml:"target_rps,omitempty"`
	Duration    time.Duration `yaml:"-"`
}

type Step struct {
	Name       string            `yaml:"name"`
	Method     string            `yaml:"method"`
	URL        string            `yaml:"url"`
	Headers    map[string]string `yaml:"headers,omitempty"`
	Body       string            `yaml:"body,omitempty"`
	// Pause specifies think time after this step (e.g. "500ms", "1s").
	// Parsed from PauseRaw by the scenario parser.
	PauseRaw   string            `yaml:"pause,omitempty"`
	Pause      time.Duration     `yaml:"-"`
	Auth       *Auth             `yaml:"auth,omitempty"`
	Capture    *Capture          `yaml:"capture,omitempty"`
	Assertions *Assertions       `yaml:"assertions,omitempty"`
}

// Auth provides authentication helpers that inject the appropriate header automatically.
type Auth struct {
	Bearer *string    `yaml:"bearer,omitempty"`
	Basic  *BasicAuth `yaml:"basic,omitempty"`
}

type BasicAuth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Capture extracts values from a response for use in subsequent steps.
// Values are JSONPath expressions (e.g. "$.data.token") or "header:Header-Name".
type Capture struct {
	Values map[string]string `yaml:",inline"`
}

func (c *Capture) UnmarshalYAML(unmarshal func(any) error) error {
	return unmarshal(&c.Values)
}

// Assertions holds per-request pass/fail checks.
// Percentile-based assertions (latency_p95_ms etc.) require HDR histogram and are handled as Thresholds.
type Assertions struct {
	// Status accepts an exact code (200) or a wildcard class (2xx, 3xx, 4xx, 5xx).
	Status       *StatusCheck      `yaml:"status,omitempty"`
	BodyContains *string           `yaml:"body_contains,omitempty"`
	BodyMatches  *string           `yaml:"body_matches,omitempty"`
	BodyJSON     map[string]string `yaml:"body_json,omitempty"`
	HeaderEquals map[string]string `yaml:"header_equals,omitempty"`
}

type Thresholds struct {
	ErrorRatePct *float64 `yaml:"error_rate_pct,omitempty"`
	P99Ms        *float64 `yaml:"p99_ms,omitempty"`
}
