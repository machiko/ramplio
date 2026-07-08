package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/machiko/ramplio/v3/internal/protocols"
)

func TestPreflightTarget(t *testing.T) {
	t.Run("url mode", func(t *testing.T) {
		u, m, ok := preflightTarget("", "https://example.com", "POST")
		if !ok || u != "https://example.com" || m != "POST" {
			t.Fatalf("got %q %q %v", u, m, ok)
		}
	})

	t.Run("url mode defaults method to GET", func(t *testing.T) {
		_, m, ok := preflightTarget("", "https://example.com", "")
		if !ok || m != "GET" {
			t.Fatalf("method = %q ok=%v", m, ok)
		}
	})

	t.Run("nothing to probe", func(t *testing.T) {
		if _, _, ok := preflightTarget("", "", ""); ok {
			t.Fatal("expected ok=false with no target")
		}
	})

	t.Run("scenario concrete url", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "s.yaml")
		yaml := "name: t\nstages:\n  - duration: 1s\n    target: 1\nsteps:\n  - name: home\n    method: GET\n    url: https://api.example.com/health\n"
		if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		u, _, ok := preflightTarget(path, "", "")
		if !ok || u != "https://api.example.com/health" {
			t.Fatalf("got %q ok=%v", u, ok)
		}
	})

	t.Run("scenario templated url is skipped", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "s.yaml")
		yaml := "name: t\nstages:\n  - duration: 1s\n    target: 1\nsteps:\n  - name: home\n    method: GET\n    url: \"{{base}}/health\"\n"
		if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, ok := preflightTarget(path, "", ""); ok {
			t.Fatal("templated URL must not be probed")
		}
	})
}

func TestRunPreflight_ReachableServerPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // server up but erroring → must NOT abort
	}))
	defer srv.Close()

	var buf bytes.Buffer
	err := runPreflight(context.Background(), &buf, protocols.DefaultHTTPConfig(), srv.URL, "GET")
	if err != nil {
		t.Fatalf("reachable server (even 5xx) should pass preflight, got %v", err)
	}
}

func TestRunPreflight_ConnRefusedAborts(t *testing.T) {
	// Reserve a port then release it so nothing is listening → connection refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	var buf bytes.Buffer
	err = runPreflight(context.Background(), &buf, protocols.DefaultHTTPConfig(), "http://"+addr, "GET")
	if err == nil {
		t.Fatal("connection refused should abort preflight")
	}
	out := buf.String()
	if !strings.Contains(out, "預檢沒過") || !strings.Contains(out, "--no-preflight") {
		t.Errorf("expected plain-language explanation with escape hatch, got:\n%s", out)
	}
}
