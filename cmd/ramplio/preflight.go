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
		return nil
	}

	title, cause, action, ok := reporter.ExplainErrorKind(kind)
	if !ok {
		return nil
	}
	fmt.Fprintf(w, "\n✗ 預檢沒過：%s\n", title)
	fmt.Fprintf(w, "  目標：%s\n", probeURL)
	fmt.Fprintf(w, "  %s\n", cause)
	fmt.Fprintf(w, "  → 建議：%s\n", action)
	fmt.Fprintln(w, "  （確定要照跑可加 --no-preflight 略過這項檢查）")
	return fmt.Errorf("預檢失敗：目標無法連線（%s）", kind)
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

// sensitiveQueryKeys are query parameters whose values are masked on screen
// (compared case-insensitively).
var sensitiveQueryKeys = map[string]bool{
	"token": true, "key": true, "api_key": true, "apikey": true,
	"access_token": true, "sig": true, "signature": true, "password": true,
}

const maskedValue = "***"

// maskURLForDisplay hides credentials in a URL before it is printed: the
// userinfo password (or the whole userinfo when there is no password) and the
// values of well-known secret query parameters. The request itself is
// unchanged; this is for terminal output only.
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
	u.RawQuery = maskQuery(u.RawQuery)
	out := u.String()
	if userinfo != "" {
		out = strings.Replace(out, "//", "//"+userinfo, 1)
	}
	return out
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
		if hasValue && sensitiveQueryKeys[strings.ToLower(name)] {
			masked[i] = k + "=" + maskedValue
			continue
		}
		masked[i] = part
	}
	return strings.Join(masked, "&")
}
