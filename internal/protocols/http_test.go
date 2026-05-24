package protocols_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/protocols"
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
