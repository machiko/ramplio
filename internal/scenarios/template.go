package scenarios

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// conditionWarnf writes a warning to stderr so misconfigured `if:` conditions
// surface immediately rather than being silently treated as "always execute".
var conditionWarnf = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[WARN] "+format+"\n", args...)
}

// VarContext holds the variables available during template rendering.
type VarContext struct {
	Vars     map[string]string // scenario-level vars block
	Captures map[string]string // values captured from previous step responses
	Data     map[string]string // current data file row (from vars_from)
}

var tokenRe = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// RenderString replaces template tokens in s with their resolved values.
// Supported tokens:
//
//	{{uuid}}              — random UUID v4
//	{{timestamp}}         — Unix seconds
//	{{timestamp_ms}}      — Unix milliseconds
//	{{env "VAR"}}         — os.Getenv("VAR")
//	{{vars.key}}          — ctx.Vars["key"]
//	{{capture.key}}       — ctx.Captures["key"]
//	{{data.column}}       — ctx.Data["column"] (from vars_from data file)
func RenderString(s string, ctx *VarContext) (string, error) {
	var renderErr error
	result := tokenRe.ReplaceAllStringFunc(s, func(match string) string {
		if renderErr != nil {
			return match
		}
		inner := strings.TrimSpace(match[2 : len(match)-2])
		val, err := resolveToken(inner, ctx)
		if err != nil {
			renderErr = err
			return match
		}
		return val
	})
	return result, renderErr
}

func resolveToken(token string, ctx *VarContext) (string, error) {
	switch {
	case token == "uuid":
		return uuid.NewString(), nil

	case token == "timestamp":
		return strconv.FormatInt(time.Now().Unix(), 10), nil

	case token == "timestamp_ms":
		return strconv.FormatInt(time.Now().UnixMilli(), 10), nil

	case strings.HasPrefix(token, "env "):
		name := strings.Trim(strings.TrimPrefix(token, "env "), `"'`)
		return os.Getenv(name), nil

	case strings.HasPrefix(token, "vars."):
		key := strings.TrimPrefix(token, "vars.")
		if ctx != nil {
			if v, ok := ctx.Vars[key]; ok {
				return v, nil
			}
		}
		return "", fmt.Errorf("template: vars.%s not defined", key)

	case strings.HasPrefix(token, "capture."):
		key := strings.TrimPrefix(token, "capture.")
		if ctx != nil {
			if v, ok := ctx.Captures[key]; ok {
				return v, nil
			}
		}
		return "", fmt.Errorf("template: capture.%s not captured yet", key)

	case strings.HasPrefix(token, "data."):
		key := strings.TrimPrefix(token, "data.")
		if ctx != nil {
			if v, ok := ctx.Data[key]; ok {
				return v, nil
			}
		}
		return "", fmt.Errorf("template: data.%s not found in data file row", key)

	default:
		return "", fmt.Errorf("template: unknown token %q", token)
	}
}

// EvalCondition evaluates a simple boolean expression used in step `if:` fields.
// The expression is first rendered through the template engine, then evaluated.
// Supported forms (after rendering):
//
//	<value> == ""      → true when value is empty
//	<value> != ""      → true when value is not empty
//	<value> == "x"     → true when value equals x
//	<value> != "x"     → true when value does not equal x
//
// Returns true when the expression cannot be parsed, so unknown conditions do not
// accidentally skip steps. A warning is printed to stderr in both failure cases.
func EvalCondition(expr string, ctx *VarContext) bool {
	rendered, err := RenderString(expr, ctx)
	if err != nil {
		conditionWarnf("step condition render failed (%v) — step will execute", err)
		return true
	}
	// Try `LHS == RHS` and `LHS != RHS`.
	if idx := strings.Index(rendered, " != "); idx >= 0 {
		lhs := strings.TrimSpace(rendered[:idx])
		rhs := strings.Trim(strings.TrimSpace(rendered[idx+4:]), `"`)
		return lhs != rhs
	}
	if idx := strings.Index(rendered, " == "); idx >= 0 {
		lhs := strings.TrimSpace(rendered[:idx])
		rhs := strings.Trim(strings.TrimSpace(rendered[idx+4:]), `"`)
		return lhs == rhs
	}
	conditionWarnf("step condition %q could not be parsed (no == or !=) — step will execute", rendered)
	return true
}

// RenderHeaders renders all header values in the map, returning a new map.
func RenderHeaders(headers map[string]string, ctx *VarContext) (map[string]string, error) {
	if len(headers) == 0 {
		return headers, nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		rendered, err := RenderString(v, ctx)
		if err != nil {
			return nil, err
		}
		out[k] = rendered
	}
	return out, nil
}
