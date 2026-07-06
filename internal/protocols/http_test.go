package protocols_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/machiko/ramplio/internal/protocols"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPExecutor_GET(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	ex := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	result := ex.Execute(context.Background(), protocols.Request{
		Method: http.MethodGet,
		URL:    server.URL,
	})

	require.NoError(t, result.Error)
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Equal(t, int64(5), result.BytesRead)
	assert.Greater(t, result.Latency, time.Duration(0))
}

func TestHTTPExecutor_POST_WithBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	ex := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	result := ex.Execute(context.Background(), protocols.Request{
		Method:  http.MethodPost,
		URL:     server.URL,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    []byte(`{"test":true}`),
	})

	require.NoError(t, result.Error)
	assert.Equal(t, http.StatusCreated, result.StatusCode)
}

func TestHTTPExecutor_ReturnsErrorOnInvalidURL(t *testing.T) {
	ex := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	result := ex.Execute(context.Background(), protocols.Request{
		Method: http.MethodGet,
		URL:    "not-a-valid-url",
	})

	assert.Error(t, result.Error)
}

func TestHTTPExecutor_RespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// hang the request
		<-r.Context().Done()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	ex := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	result := ex.Execute(ctx, protocols.Request{
		Method: http.MethodGet,
		URL:    server.URL,
	})

	assert.Error(t, result.Error)
}

func TestHTTPExecutor_NewSession_IsolatedCookies(t *testing.T) {
	// Server sets a cookie on first request and verifies it on second.
	var receivedCookie string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123"})
			w.WriteHeader(http.StatusOK)
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			receivedCookie = c.Value
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ex := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	session := ex.NewSession()

	// First request sets the cookie.
	res := session.Execute(context.Background(), protocols.Request{
		Method: http.MethodGet,
		URL:    server.URL + "/set",
	})
	require.NoError(t, res.Error)

	// Second request should carry the cookie automatically.
	res = session.Execute(context.Background(), protocols.Request{
		Method: http.MethodGet,
		URL:    server.URL + "/check",
	})
	require.NoError(t, res.Error)
	assert.Equal(t, "abc123", receivedCookie)
}

func TestHTTPExecutor_NewSession_SeparateJarsPerSession(t *testing.T) {
	// Two sessions must not share cookies.
	cookiesReceived := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "tok", Value: "secret"})
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := r.Cookie("tok"); err == nil {
			cookiesReceived["found"]++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ex := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	s1 := ex.NewSession()
	s2 := ex.NewSession()

	// s1 gets a cookie.
	s1.Execute(context.Background(), protocols.Request{Method: http.MethodGet, URL: server.URL + "/set"})

	// s2 should NOT have the cookie.
	s2.Execute(context.Background(), protocols.Request{Method: http.MethodGet, URL: server.URL + "/check"})

	assert.Equal(t, 0, cookiesReceived["found"], "s2 should not see s1's cookies")

	// s1 should carry the cookie.
	s1.Execute(context.Background(), protocols.Request{Method: http.MethodGet, URL: server.URL + "/check"})
	assert.Equal(t, 1, cookiesReceived["found"])
}

func TestHTTPExecutor_CloseIdleConnections(t *testing.T) {
	ex := protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())
	// Should not panic.
	assert.NotPanics(t, ex.CloseIdleConnections)
}
