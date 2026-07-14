package metrics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"syscall"
)

// ErrorKind is a coarse, plain-language-friendly classification of why a request
// failed. It exists so reporters can explain failures in human terms (e.g. "目標
// 服務沒開") instead of dumping raw Go errors. The kinds are intentionally few —
// enough to drive a distinct piece of advice, no more.
type ErrorKind string

const (
	ErrKindNone        ErrorKind = ""             // not an error
	ErrKindDNS         ErrorKind = "dns"          // 網域解析失敗（拼錯/不存在）
	ErrKindConnRefused ErrorKind = "conn_refused" // 連線被拒（服務沒開/埠號錯）
	ErrKindConnReset   ErrorKind = "conn_reset"   // 連線中途被切斷
	ErrKindTimeout     ErrorKind = "timeout"      // 連線或回應逾時
	ErrKindTLS         ErrorKind = "tls"          // TLS/憑證問題
	ErrKindHTTP4xx     ErrorKind = "http_4xx"     // 4xx（含 401/403/404）
	ErrKindHTTP5xx     ErrorKind = "http_5xx"     // 5xx（伺服器端錯誤）
	ErrKindAssertion   ErrorKind = "assertion"    // 請求成功但斷言不通過
	ErrKindOther       ErrorKind = "other"        // 無法歸類
)

// DisplayOrder is the stable order error kinds are listed in reports. Connection-
// level failures come first because they usually point at a misconfiguration the
// user must fix before any number is meaningful.
var DisplayOrder = []ErrorKind{
	ErrKindDNS,
	ErrKindConnRefused,
	ErrKindConnReset,
	ErrKindTimeout,
	ErrKindTLS,
	ErrKindHTTP4xx,
	ErrKindHTTP5xx,
	ErrKindAssertion,
	ErrKindOther,
}

// DominantErrorKind returns the most frequent error kind in a breakdown, its
// count, and its share of totalErrors (0..1). Returns ErrKindNone when the
// breakdown is empty. Ties are broken by DisplayOrder so the result is stable.
func DominantErrorKind(breakdown map[ErrorKind]int64, totalErrors int64) (ErrorKind, int64, float64) {
	if len(breakdown) == 0 || totalErrors <= 0 {
		return ErrKindNone, 0, 0
	}
	best := ErrKindNone
	var bestCount int64
	rank := func(k ErrorKind) int {
		for i, dk := range DisplayOrder {
			if dk == k {
				return i
			}
		}
		return len(DisplayOrder)
	}
	for _, k := range DisplayOrder {
		if c := breakdown[k]; c > bestCount || (c == bestCount && c > 0 && rank(k) < rank(best)) {
			best, bestCount = k, c
		}
	}
	if bestCount == 0 {
		return ErrKindNone, 0, 0
	}
	return best, bestCount, float64(bestCount) / float64(totalErrors)
}

// ClassifyError maps a request's error and HTTP status into one ErrorKind.
//
// The discriminator between a transport failure and a post-response failure is
// the status code: executors return a zero StatusCode when the request never
// completed (dial/TLS/timeout), and a real code (>0) once a response arrived.
// So a non-nil error with a status code means the HTTP exchange succeeded but a
// later check (assertion/capture) failed.
func ClassifyError(err error, statusCode int) ErrorKind {
	if err == nil {
		switch {
		case statusCode >= 500:
			return ErrKindHTTP5xx
		case statusCode >= 400:
			return ErrKindHTTP4xx
		case statusCode >= 200 && statusCode < 300:
			return ErrKindNone
		case statusCode == 101:
			// WebSocket 握手成功;isError() 對 101 豁免,分類須一致。
			return ErrKindNone
		default:
			// 其餘 1xx / 3xx counted as errors by isError() but with no transport error.
			return ErrKindOther
		}
	}

	// A response arrived (status set) but a post-response check failed.
	if statusCode > 0 {
		return ErrKindAssertion
	}

	return classifyTransportError(err)
}

// classifyTransportError inspects a transport-level error (no HTTP response was
// received) and buckets it. Order matters: more specific causes are checked
// before broader ones.
func classifyTransportError(err error) ErrorKind {
	// DNS resolution failure — the most common newcomer mistake (typo'd host).
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ErrKindDNS
	}

	// TLS / certificate problems.
	var certErr *tls.CertificateVerificationError
	var unknownAuthErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var certInvalidErr x509.CertificateInvalidError
	var recordErr tls.RecordHeaderError
	if errors.As(err, &certErr) ||
		errors.As(err, &unknownAuthErr) ||
		errors.As(err, &hostnameErr) ||
		errors.As(err, &certInvalidErr) ||
		errors.As(err, &recordErr) {
		return ErrKindTLS
	}

	// Connection refused — service not listening, or wrong host/port.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ErrKindConnRefused
	}

	// Connection reset / broken pipe — peer dropped an established connection.
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return ErrKindConnReset
	}

	// Timeout — either the request deadline or a net-level timeout.
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrKindTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrKindTimeout
	}

	// Last-resort string heuristics for errors that don't expose typed values
	// across platforms.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"):
		return ErrKindConnRefused
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "broken pipe"):
		return ErrKindConnReset
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return ErrKindTimeout
	case strings.Contains(msg, "certificate"), strings.Contains(msg, "tls"), strings.Contains(msg, "x509"):
		return ErrKindTLS
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "server misbehaving"):
		return ErrKindDNS
	}

	return ErrKindOther
}
