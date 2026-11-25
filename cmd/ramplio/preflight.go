package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

// preflightTimeout caps the single probe request so a hung target can't stall
// startup for long.
const preflightTimeout = 8 * time.Second

// preflightTarget decides what single request to probe before a full run.
// Returns ok=false when there's nothing safe to probe (e.g. the target URL is
// templated and can't be rendered without full run context).
func preflightTarget(scenarioFile, rawURL, method string) (probeURL, probeMethod string, ok bool) {
	if rawURL != "" {
		return rawURL, methodOrGet(method), true
	}
	if scenarioFile == "" {
		return "", "", false
	}
	sc, err := scenarios.ParseFile(scenarioFile)
	if err != nil || len(sc.Steps) == 0 {
		return "", "", false
	}
	first := sc.Steps[0]
	// Templated URLs (e.g. {{base}}/path) need the full variable context the
	// engine builds at runtime; probing the raw string would be a false alarm.
	if first.URL == "" || strings.Contains(first.URL, "{{") {
		return "", "", false
	}
	return first.URL, methodOrGet(first.Method), true
}

func methodOrGet(m string) string {
	if m == "" {
		return "GET"
	}
	return m
}

// runPreflight fires one probe request and, if it fails because the target is
// unreachable (DNS / connection refused / TLS), prints a plain-language
// explanation to w and returns a non-nil error so the caller can abort. Slow
// targets, timeouts and HTTP error codes do NOT abort — the service is reachable
// and the user may legitimately want to measure those.
//
// req is sent as-is (headers and body included) so a write-type probe gets the
// same auth / content-type as the real traffic instead of a misleading 401/415.
func runPreflight(ctx context.Context, w io.Writer, cfg protocols.HTTPConfig, req protocols.Request) error {
	_, err := preflightOnce(ctx, w, cfg, req)
	return err
}

// preflightOnce is runPreflight that also returns the raw result, so a caller
// can tell whether the request actually reached the target.
func preflightOnce(ctx context.Context, w io.Writer, cfg protocols.HTTPConfig, req protocols.Request) (protocols.Result, error) {
	cfg.RequestTimeout = preflightTimeout
	exec := protocols.NewHTTPExecutor(cfg)
	defer exec.CloseIdleConnections()

	pctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()

	probeURL := maskURLForDisplay(req.URL) // display only; req is sent unmodified
	res, trace := exec.ExecuteTraced(pctx, req)
	kind := metrics.ClassifyError(res.Error, res.StatusCode)

	if !reporter.IsReachabilityFailure(kind) {
		// Reachable: surface where the single probe's time went, so the user can
		// see what the tool measures (DNS / 連線 / TLS / 首位元組) before the run.
		printPreflightBreakdown(w, probeURL, res, trace)
		return res, nil
	}

	title, cause, action, ok := reporter.ExplainErrorKind(kind)
	if !ok {
		return res, nil
	}
	fmt.Fprintf(w, "\n✗ 預檢沒過：%s\n", title)
	fmt.Fprintf(w, "  目標：%s\n", probeURL)
	fmt.Fprintf(w, "  %s\n", cause)
	fmt.Fprintf(w, "  → 建議：%s\n", action)
	fmt.Fprintln(w, "  （確定要照跑可加 --no-preflight 略過這項檢查）")
	return res, fmt.Errorf("預檢失敗：目標無法連線（%s）", kind)
}

// printPreflightBreakdown shows the single probe's latency split into connection
// phases — pure measurement transparency, computed from one diagnostic request
// (never the load hot path).
func printPreflightBreakdown(w io.Writer, probeURL string, res protocols.Result, tr protocols.Trace) {
	status := "無回應"
	if res.StatusCode > 0 {
		status = fmt.Sprintf("HTTP %d", res.StatusCode)
	}
	fmt.Fprintf(w, "\n✓ 預檢通過：%s（%s，總共 %s）\n", probeURL, status, fmtMs(tr.Total))
	if tr.Reused {
		fmt.Fprintln(w, "  連線分解：沿用既有連線（免去 DNS／連線／TLS）")
		fmt.Fprintf(w, "    首位元組（TTFB）：%s\n", fmtMs(tr.TTFB))
		return
	}
	fmt.Fprintln(w, "  連線分解（這一發請求的時間花在哪）：")
	fmt.Fprintf(w, "    DNS 解析：%s    連線：%s    TLS：%s    首位元組：%s\n",
		fmtMs(tr.DNS), fmtMs(tr.Connect), fmtMs(tr.TLS), fmtMs(tr.TTFB))
}

// fmtMs renders a duration in whole milliseconds, or "—" when not measured (0).
func fmtMs(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// sensitiveQueryKeyParts: a query parameter whose name contains any of these
// (case-insensitive substring) has its value masked on screen and in saved
// files. Over-masking a harmless parameter is preferred to leaking a secret.
var sensitiveQueryKeyParts = []string{
	"token", "secret", "key", "sig", "auth", "pass", "credential", "session", "jwt", "code",
}

const maskedValue = "***"

// maskURLForDisplay hides credentials in a URL before it is printed or saved:
// the userinfo password (or the whole userinfo when there is no password), the
// values of secret-looking query parameters, and the whole fragment (OAuth
// implicit flows put access_token there). The request itself is unchanged.
func maskURLForDisplay(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "（無法解析的網址）"
	}
	userinfo := ""
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			userinfo = u.User.Username() + ":" + maskedValue + "@"
		} else {
			userinfo = maskedValue + "@"
		}
		u.User = nil
	}
	hasFragment := u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#")
	u.Fragment, u.RawFragment = "", ""
	u.RawQuery = maskQuery(u.RawQuery)
	out := u.String()
	if userinfo != "" {
		out = strings.Replace(out, "//", "//"+userinfo, 1)
	}
	if hasFragment {
		out += "#" + maskedValue
	}
	return out
}

func isSensitiveQueryKey(name string) bool {
	lower := strings.ToLower(name)
	for _, part := range sensitiveQueryKeyParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func maskQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	masked := make([]string, len(parts))
	for i, part := range parts {
		k, _, hasValue := strings.Cut(part, "=")
		name, err := url.QueryUnescape(k)
		if err != nil {
			name = k
		}
		if hasValue && isSensitiveQueryKey(name) {
			masked[i] = k + "=" + maskedValue
			continue
		}
		masked[i] = part
	}
	return strings.Join(masked, "&")
}

// preflightOutcomeMessage tells the user honestly what the blocked preflight
// did: a response means the server processed it (a write happened once); a
// timeout or mid-flight reset means it may have; anything else means the
// request never reached the target.
func preflightOutcomeMessage(method string, res protocols.Result) string {
	if res.StatusCode > 0 {
		return fmt.Sprintf("已送出 1 發真實 %s 預檢（伺服器已實際處理這一發，若是寫入就已經寫入一次）。", method)
	}
	switch metrics.ClassifyError(res.Error, res.StatusCode) {
	case metrics.ErrKindTimeout, metrics.ErrKindConnReset:
		return fmt.Sprintf("1 發 %s 預檢已送出但沒有收到回應（逾時或連線中斷），無法確定伺服器是否已處理（可能已寫入一次）。", method)
	}
	return "預檢沒有送達目標，沒有寫入。"
}
