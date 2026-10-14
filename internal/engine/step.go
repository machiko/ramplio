package engine

import (
	"context"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"

	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/tidwall/gjson"
)

// wsBase returns the configured websocket executor (a real one by default).
func (e *RampEngine) wsBase() protocols.Executor {
	if e.cfg.WSExecutor != nil {
		return e.cfg.WSExecutor
	}
	return protocols.NewWSExecutor()
}

// stepExecutor returns the correct executor for a step (protocol + retry wrapper).
// sessionExec is the HTTP session executor for the current VU; pass e.cfg.Executor for rate mode.
// wsSession is the VU's persistent websocket session (nil outside a VU loop or
// when the websocket executor is a test double) — used only for ws_mode: persistent.
func (e *RampEngine) stepExecutor(step RampStep, sessionExec protocols.Executor, wsSession *protocols.WSSession) protocols.Executor {
	var base protocols.Executor
	if step.Protocol == "websocket" {
		if step.WSMode == "persistent" && wsSession != nil {
			base = wsSession
		} else {
			base = e.wsBase()
		}
	} else {
		base = sessionExec
	}
	if step.Retry != nil && step.Retry.Count > 0 {
		return protocols.NewRetryingExecutor(base, step.Retry.Count, step.Retry.On, step.Retry.BackoffMs)
	}
	return base
}

// pickExecutor selects the executor for a step without a session (setup/teardown/rate mode).
// persistent websocket 在此不適用:這些路徑沒有 VU 生命週期可以承載連線。
func (e *RampEngine) pickExecutor(step RampStep) protocols.Executor {
	return e.stepExecutor(step, e.cfg.Executor, nil)
}

// executeSingleStep runs a step once; used by setup and teardown.
func (e *RampEngine) executeSingleStep(ctx context.Context, step RampStep, varCtx *scenarios.VarContext, exec protocols.Executor) {
	req, err := renderRequest(step, varCtx)
	if err != nil {
		return
	}
	result := exec.Execute(ctx, req)
	if result.Error == nil {
		applyCaptures(step.Capture, step.CompiledRegexes, result, varCtx)
	}
}

// renderRequest resolves template tokens in the step's URL, headers, and body,
// then applies any auth helper.
func renderRequest(step RampStep, ctx *scenarios.VarContext) (protocols.Request, error) {
	url, err := scenarios.RenderString(step.Request.URL, ctx)
	if err != nil {
		return protocols.Request{}, err
	}

	// RenderHeaders hands back a fresh map (or nil), never step.Request.Headers
	// itself, which every VU shares. Executors only read headers, so nil is fine.
	headers, err := scenarios.RenderHeaders(step.Request.Headers, ctx)
	if err != nil {
		return protocols.Request{}, err
	}

	var body []byte
	if len(step.Request.Body) > 0 {
		rendered, err := scenarios.RenderString(string(step.Request.Body), ctx)
		if err != nil {
			return protocols.Request{}, err
		}
		body = []byte(rendered)
	}

	if step.Auth != nil {
		authz, err := renderAuthorization(step.Auth, ctx)
		if err != nil {
			return protocols.Request{}, err
		}
		if authz != "" {
			if headers == nil {
				headers = make(map[string]string, 1)
			}
			headers["Authorization"] = authz
		}
	}

	return protocols.Request{
		Method:  step.Request.Method,
		URL:     url,
		Headers: headers,
		Body:    body,
		Stream:  step.Stream == "sse",
	}, nil
}

// renderAuthorization returns the Authorization header value for auth, or ""
// when neither bearer nor basic is set. Bearer wins when both are.
func renderAuthorization(auth *scenarios.Auth, ctx *scenarios.VarContext) (string, error) {
	if auth.Bearer != nil {
		token, err := scenarios.RenderString(*auth.Bearer, ctx)
		if err != nil {
			return "", err
		}
		return "Bearer " + token, nil
	}
	if auth.Basic != nil {
		username, err := scenarios.RenderString(auth.Basic.Username, ctx)
		if err != nil {
			return "", err
		}
		password, err := scenarios.RenderString(auth.Basic.Password, ctx)
		if err != nil {
			return "", err
		}
		return basicAuthHeader(username, password), nil
	}
	return "", nil
}

// applyCaptures extracts values from the result and stores them in varCtx.Captures.
// compiled is a pre-built map of regex patterns to avoid repeated Compile calls in the hot loop.
func applyCaptures(cap *scenarios.Capture, compiled map[string]*regexp.Regexp, result protocols.Result, ctx *scenarios.VarContext) {
	if cap == nil {
		return
	}
	for key, expr := range cap.Values {
		switch {
		case strings.HasPrefix(expr, "cookie:"):
			cookieName := strings.TrimPrefix(expr, "cookie:")
			for _, raw := range result.RawSetCookies {
				if c, err := http.ParseSetCookie(raw); err == nil && c.Name == cookieName {
					ctx.Captures[key] = c.Value
					break
				}
			}
		case strings.HasPrefix(expr, "header:"):
			ctx.Captures[key] = result.ResponseHeaders[http.CanonicalHeaderKey(strings.TrimPrefix(expr, "header:"))]
		case strings.HasPrefix(expr, "regex:"):
			pattern := strings.TrimPrefix(expr, "regex:")
			re := compiled[pattern]
			if re == nil {
				re, _ = regexp.Compile(pattern)
			}
			if re != nil {
				if m := re.FindSubmatch(result.Body); len(m) > 1 {
					ctx.Captures[key] = string(m[1])
				}
			}
		default:
			ctx.Captures[key] = gjson.GetBytes(result.Body, scenarios.JSONPathToGJSON(expr)).String()
		}
	}
}

func basicAuthHeader(username, password string) string {
	creds := username + ":" + password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}
