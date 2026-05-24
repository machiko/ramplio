package scenarios

import "time"

type Scenario struct {
	Name       string            `yaml:"name"`
	Stages     []Stage           `yaml:"stages"`
	Steps      []Step            `yaml:"steps"`
	Vars       map[string]string `yaml:"vars,omitempty"`
	Thresholds *Thresholds       `yaml:"thresholds,omitempty"`
	HTTP       *ScenarioHTTP     `yaml:"http,omitempty"`
}

// ScenarioHTTP allows per-scenario tuning of the HTTP connection pool and timeouts.
type ScenarioHTTP struct {
	MaxIdleConns        *int `yaml:"max_idle_conns,omitempty"`
	MaxIdleConnsPerHost *int `yaml:"max_idle_conns_per_host,omitempty"`
	RequestTimeoutMs    *int `yaml:"request_timeout_ms,omitempty"`
}

type Stage struct {
	DurationRaw string        `yaml:"duration"`
	Target      int           `yaml:"target"`
	Duration    time.Duration `yaml:"-"`
}

type Step struct {
	Name       string            `yaml:"name"`
	Method     string            `yaml:"method"`
	URL        string            `yaml:"url"`
	Headers    map[string]string `yaml:"headers,omitempty"`
	Body       string            `yaml:"body,omitempty"`
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
	Status       *int              `yaml:"status,omitempty"`
	BodyContains *string           `yaml:"body_contains,omitempty"`
	BodyMatches  *string           `yaml:"body_matches,omitempty"`
	BodyJSON     map[string]string `yaml:"body_json,omitempty"`
	HeaderEquals map[string]string `yaml:"header_equals,omitempty"`
}

type Thresholds struct {
	ErrorRatePct *float64 `yaml:"error_rate_pct,omitempty"`
	P99Ms        *float64 `yaml:"p99_ms,omitempty"`
}
