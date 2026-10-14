package engine

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Setting any single scenarios.Step field must change the converted RampStep —
// so a newly added field that is not wired fails here, and cannot pass by merely
// being listed. Only fields consumed before conversion are exempt. This is the
// drift that once let the distributed worker silently drop WSMode/Stream.
func TestStepsFromScenario_EveryStepFieldIsWired(t *testing.T) {
	exempt := map[string]string{
		"PauseRaw": "parsed into Pause by the scenario parser",
	}
	stepType := reflect.TypeOf(scenarios.Step{})
	for name := range exempt {
		_, ok := stepType.FieldByName(name)
		assert.True(t, ok, "exempt field %s no longer exists on scenarios.Step", name)
	}
	for _, f := range reflect.VisibleFields(stepType) {
		if _, skip := exempt[f.Name]; skip {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			base := scenarios.Step{Method: "GET", URL: "http://x"}
			if strings.HasPrefix(f.Name, "WS") {
				base.Protocol = "websocket" // ws_* fields only act on websocket steps
			}
			perturbed := base
			field := reflect.ValueOf(&perturbed).Elem().FieldByIndex(f.Index)
			require.True(t, setSentinel(field), "no sentinel for kind %s: extend setSentinel", f.Type.Kind())

			want := StepsFromScenario([]scenarios.Step{base})[0]
			got := StepsFromScenario([]scenarios.Step{perturbed})[0]
			assert.NotEqual(t, want, got, "Step.%s does not reach RampStep: wire it in StepsFromScenario", f.Name)
		})
	}
}

// setSentinel sets v to a non-zero value that differs from the base step.
func setSentinel(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		v.SetString("sentinel")
	case reflect.Int, reflect.Int64:
		v.SetInt(3)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(reflect.ValueOf("X-Sentinel"), reflect.ValueOf("1"))
		v.Set(m)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
	default:
		return false
	}
	return true
}

func TestStepsFromScenario_CarriesEveryField(t *testing.T) {
	bearer := "{{capture.token}}"
	auth := &scenarios.Auth{Bearer: &bearer}
	capture := &scenarios.Capture{Values: map[string]string{"id": "$.id"}}
	assertions := &scenarios.Assertions{Status: scenarios.StatusExact(201)}
	retry := &scenarios.RetryConfig{Count: 2}

	got := StepsFromScenario([]scenarios.Step{{
		Name:       "create",
		Method:     "post",
		URL:        "http://x/items",
		Headers:    map[string]string{"Accept": "application/json"},
		Body:       `{"a":1}`,
		Pause:      250 * time.Millisecond,
		Auth:       auth,
		Capture:    capture,
		Assertions: assertions,
		Retry:      retry,
		Group:      "writes",
		Protocol:   "http",
		Stream:     "sse",
		If:         `vars.on == "1"`,
		Loop:       3,
	}})

	require.Len(t, got, 1)
	assert.Equal(t, RampStep{
		Name: "create",
		Request: protocols.Request{
			Method:  "POST",
			URL:     "http://x/items",
			Headers: map[string]string{"Accept": "application/json"},
			Body:    []byte(`{"a":1}`),
		},
		Assertions: assertions,
		Auth:       auth,
		Capture:    capture,
		Retry:      retry,
		Pause:      250 * time.Millisecond,
		Group:      "writes",
		Protocol:   "http",
		Stream:     "sse",
		If:         `vars.on == "1"`,
		Loop:       3,
	}, got[0])
}

func TestStepsFromScenario_NameDefaultsToMethodAndURL(t *testing.T) {
	got := StepsFromScenario([]scenarios.Step{{Method: "post", URL: "http://example.com/api"}})
	assert.Equal(t, "POST http://example.com/api", got[0].Name)
}

func TestStepsFromScenario_WebSocket(t *testing.T) {
	t.Run("message becomes the body and expect a synthetic header", func(t *testing.T) {
		got := StepsFromScenario([]scenarios.Step{{
			Method: "GET", URL: "ws://x/echo", Protocol: "websocket",
			WSMessage: `{"msg":"hello"}`, WSExpect: "hello", WSMode: "persistent",
		}})
		assert.Equal(t, `{"msg":"hello"}`, string(got[0].Request.Body))
		assert.Equal(t, map[string]string{protocols.WSExpectHeader: "hello"}, got[0].Request.Headers)
		assert.Equal(t, "persistent", got[0].WSMode)
	})
	t.Run("explicit body wins over ws_message", func(t *testing.T) {
		got := StepsFromScenario([]scenarios.Step{{
			Method: "GET", URL: "ws://x", Protocol: "websocket", Body: "body", WSMessage: "msg",
		}})
		assert.Equal(t, "body", string(got[0].Request.Body))
	})
	t.Run("synthetic header never lands in the parsed scenario", func(t *testing.T) {
		in := map[string]string{"Origin": "http://x"}
		got := StepsFromScenario([]scenarios.Step{{
			Method: "GET", URL: "ws://x", Protocol: "websocket", Headers: in, WSExpect: "pong",
		}})
		assert.Equal(t, map[string]string{"Origin": "http://x"}, in)
		assert.Equal(t, map[string]string{"Origin": "http://x", protocols.WSExpectHeader: "pong"}, got[0].Request.Headers)
	})
	t.Run("protocol is lower-cased so the engine routes it", func(t *testing.T) {
		got := StepsFromScenario([]scenarios.Step{{
			Method: "GET", URL: "ws://x", Protocol: "WebSocket", WSExpect: "pong",
		}})
		assert.Equal(t, "websocket", got[0].Protocol)
		assert.Equal(t, "pong", got[0].Request.Headers[protocols.WSExpectHeader])
	})
	t.Run("ws fields are ignored on http steps", func(t *testing.T) {
		got := StepsFromScenario([]scenarios.Step{{
			Method: "GET", URL: "http://x", WSMessage: "msg", WSExpect: "pong",
		}})
		assert.Nil(t, got[0].Request.Headers)
		assert.Empty(t, got[0].Request.Body)
	})
}

// The cache key must be the bare pattern: that is what applyCaptures looks up.
// A worker copy once keyed by "regex:<pattern>", so every lookup missed and
// each capture recompiled its regex on the hot path.
func TestStepsFromScenario_PrecompilesCaptureRegexesByPattern(t *testing.T) {
	got := StepsFromScenario([]scenarios.Step{{
		Method: "GET", URL: "http://x",
		Capture: &scenarios.Capture{Values: map[string]string{
			"token": `regex:"token": "([^"]+)"`,
			"bad":   `regex:[invalid(`,
			"id":    `$.id`,
		}},
	}})

	compiled := got[0].CompiledRegexes
	require.Len(t, compiled, 1, "only the valid regex capture is precompiled")

	// Poison every cached entry: applyCaptures falls back to compiling on a miss,
	// so only a real cache hit lets the poison decide the captured value.
	poisoned := make(map[string]*regexp.Regexp, len(compiled))
	for key := range compiled {
		poisoned[key] = regexp.MustCompile(`"token": "(t)`)
	}
	vc := &scenarios.VarContext{Captures: map[string]string{}}
	applyCaptures(got[0].Capture, poisoned, protocols.Result{Body: []byte(`{"token": "t-9"}`)}, vc)
	assert.Equal(t, "t", vc.Captures["token"], "applyCaptures must hit the key StepsFromScenario built")
}

func TestStepsFromScenario_NoCapturesNoCompiledMap(t *testing.T) {
	got := StepsFromScenario([]scenarios.Step{{
		Method: "GET", URL: "http://x", Capture: &scenarios.Capture{Values: map[string]string{"id": "$.id"}},
	}})
	assert.Nil(t, got[0].CompiledRegexes)
}

// BenchmarkApplyCapturesRegex compares the precompiled-cache key shapes: the
// bare pattern (what StepsFromScenario now builds, a cache hit) against the
// "regex:"-prefixed key the old worker copy built, which always missed and fell
// back to compiling the regex on every capture. Kept as a historical
// comparison of the two shapes, not as a regression test.
func BenchmarkApplyCapturesRegex(b *testing.B) {
	const pattern = `"token": "([^"]+)"`
	capture := &scenarios.Capture{Values: map[string]string{"token": "regex:" + pattern}}
	result := protocols.Result{Body: []byte(`{"user":"u1","token": "t-9","exp":3600}`)}
	re := regexp.MustCompile(pattern)
	shapes := []struct {
		name     string
		compiled map[string]*regexp.Regexp
	}{
		{"pattern-key", map[string]*regexp.Regexp{pattern: re}},
		{"prefixed-key", map[string]*regexp.Regexp{"regex:" + pattern: re}},
	}
	for _, sh := range shapes {
		b.Run(sh.name, func(b *testing.B) {
			b.ReportAllocs()
			vc := &scenarios.VarContext{Captures: map[string]string{}}
			for b.Loop() {
				applyCaptures(capture, sh.compiled, result, vc)
			}
		})
	}
}
