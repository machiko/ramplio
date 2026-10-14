package engine

// 場景步驟 → RampStep 的唯一轉換來源:CLI、dashboard、分散式 worker 與
// coordinator 共用。這裡曾有 CLI 與 worker 兩份複製,worker 版漏接
// WSMode/Stream、regex 預編譯 key 也對不上(fix-4),故收斂為單一來源。

import (
	"maps"
	"regexp"
	"strings"

	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

// StepsFromScenario converts parsed scenario steps into engine steps. Every
// entry point uses it, so a new step field only has to be wired once.
func StepsFromScenario(steps []scenarios.Step) []RampStep {
	out := make([]RampStep, len(steps))
	for i, s := range steps {
		name := s.Name
		if name == "" {
			name = strings.ToUpper(s.Method) + " " + s.URL
		}
		// Lower-case here too (the parser already does) so the converter is
		// self-consistent: the engine routes on an exact "websocket" match.
		protocol := strings.ToLower(s.Protocol)
		isWS := protocol == "websocket"
		// Clone so the synthetic header below never lands in the parsed scenario.
		hdrs := maps.Clone(s.Headers)
		// Inject WS expect as a synthetic header so WSExecutor can check it.
		if isWS && s.WSExpect != "" {
			if hdrs == nil {
				hdrs = make(map[string]string, 1)
			}
			hdrs[protocols.WSExpectHeader] = s.WSExpect
		}
		body := []byte(s.Body)
		if isWS && s.WSMessage != "" && s.Body == "" {
			body = []byte(s.WSMessage)
		}
		out[i] = RampStep{
			Name: name,
			Request: protocols.Request{
				Method:  strings.ToUpper(s.Method),
				URL:     s.URL,
				Headers: hdrs,
				Body:    body,
			},
			Assertions: s.Assertions,
			Auth:       s.Auth,
			Capture:    s.Capture,
			Retry:      s.Retry,
			Pause:      s.Pause,
			Group:      s.Group,
			Protocol:   protocol,
			WSMode:     s.WSMode,
			Stream:     s.Stream,
			If:         s.If,
			Loop:       s.Loop,
		}
		if s.Capture != nil {
			if compiled := precompileRegexes(s.Capture.Values); len(compiled) > 0 {
				out[i].CompiledRegexes = compiled
			}
		}
	}
	return out
}

// precompileRegexes compiles every regex: capture once, keyed by the bare
// pattern — the key applyCaptures looks up on the hot path. Invalid patterns
// are skipped, so that capture never fires.
func precompileRegexes(values map[string]string) map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp)
	for _, expr := range values {
		if strings.HasPrefix(expr, "regex:") {
			pattern := strings.TrimPrefix(expr, "regex:")
			if re, err := regexp.Compile(pattern); err == nil {
				out[pattern] = re
			}
		}
	}
	return out
}
