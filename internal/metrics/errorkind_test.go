package metrics

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"
)

// timeoutErr is a synthetic net.Error that reports Timeout()==true, used to test
// the net.Error timeout branch without a real network call.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassifyError(t *testing.T) {
	wrap := func(inner error) error {
		// Mimic the wrapping http.Client produces: url.Error → net.OpError → inner.
		return &url.Error{Op: "Get", URL: "https://x", Err: &net.OpError{Op: "dial", Err: inner}}
	}

	tests := []struct {
		name       string
		err        error
		statusCode int
		want       ErrorKind
	}{
		{"success 200", nil, 200, ErrKindNone},
		{"http 404", nil, 404, ErrKindHTTP4xx},
		{"http 401", nil, 401, ErrKindHTTP4xx},
		{"http 503", nil, 503, ErrKindHTTP5xx},
		{"http 3xx as other", nil, 302, ErrKindOther},
		{"assertion failure has status", errors.New("assertion failed: expected status 200, got 204"), 204, ErrKindAssertion},
		{"dns not found", &url.Error{Err: &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}}, 0, ErrKindDNS},
		{"connection refused", wrap(os.NewSyscallError("connect", syscall.ECONNREFUSED)), 0, ErrKindConnRefused},
		{"connection reset", wrap(os.NewSyscallError("read", syscall.ECONNRESET)), 0, ErrKindConnReset},
		{"broken pipe", wrap(os.NewSyscallError("write", syscall.EPIPE)), 0, ErrKindConnReset},
		{"context deadline", &url.Error{Err: context.DeadlineExceeded}, 0, ErrKindTimeout},
		{"net timeout", wrap(timeoutErr{}), 0, ErrKindTimeout},
		{"tls unknown authority", &url.Error{Err: x509.UnknownAuthorityError{}}, 0, ErrKindTLS},
		{"tls hostname mismatch", &url.Error{Err: x509.HostnameError{Host: "x"}}, 0, ErrKindTLS},
		{"unknown transport", wrap(errors.New("some weird failure")), 0, ErrKindOther},
		{"string-fallback refused", errors.New("dial tcp 1.2.3.4:80: connect: connection refused"), 0, ErrKindConnRefused},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyError(tt.err, tt.statusCode); got != tt.want {
				t.Errorf("ClassifyError(%v, %d) = %q, want %q", tt.err, tt.statusCode, got, tt.want)
			}
		})
	}
}

func TestSummaryErrorBreakdown(t *testing.T) {
	var s Summary
	now := time.Now()

	samples := []Sample{
		{StatusCode: 200, At: now}, // ok
		{StatusCode: 503, At: now}, // 5xx
		{StatusCode: 503, At: now}, // 5xx
		{StatusCode: 0, Error: &url.Error{Err: &net.DNSError{Err: "no such host"}}},      // dns
		{StatusCode: 200, Error: fmt.Errorf("assertion failed: body mismatch"), At: now}, // assertion
	}
	for _, sm := range samples {
		s.record(sm)
	}

	if s.Total != 5 {
		t.Fatalf("Total = %d, want 5", s.Total)
	}
	if s.Errors != 4 {
		t.Fatalf("Errors = %d, want 4", s.Errors)
	}
	if got := s.ErrorBreakdown[ErrKindHTTP5xx]; got != 2 {
		t.Errorf("breakdown[5xx] = %d, want 2", got)
	}
	if got := s.ErrorBreakdown[ErrKindDNS]; got != 1 {
		t.Errorf("breakdown[dns] = %d, want 1", got)
	}
	if got := s.ErrorBreakdown[ErrKindAssertion]; got != 1 {
		t.Errorf("breakdown[assertion] = %d, want 1", got)
	}
	if _, ok := s.ErrorBreakdown[ErrKindNone]; ok {
		t.Errorf("breakdown must not record successes")
	}
}

// DominantErrorKind is a helper the reporter relies on; verify it picks the most
// frequent kind and reports its share.
func TestDominantErrorKind(t *testing.T) {
	bd := map[ErrorKind]int64{ErrKindConnRefused: 8, ErrKindTimeout: 2}
	kind, count, share := DominantErrorKind(bd, 10)
	if kind != ErrKindConnRefused || count != 8 {
		t.Fatalf("got kind=%q count=%d, want conn_refused/8", kind, count)
	}
	if share < 0.79 || share > 0.81 {
		t.Errorf("share = %f, want ~0.8", share)
	}

	if k, _, _ := DominantErrorKind(nil, 0); k != ErrKindNone {
		t.Errorf("empty breakdown should yield ErrKindNone, got %q", k)
	}
}
