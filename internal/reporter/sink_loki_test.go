package reporter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/internal/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lokiDSN(serverURL, query string) string {
	return "loki://" + strings.TrimPrefix(serverURL, "http://") + query
}

func TestLokiSink_Write_PushesStream(t *testing.T) {
	var path, contentType, orgID string
	var payload lokiPush
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		contentType = r.Header.Get("Content-Type")
		orgID = r.Header.Get("X-Scope-OrgID")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sink, err := NewLokiSink(lokiDSN(server.URL, "?labels=env=staging,region=tw&org=tenant-a"))
	require.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{
		Total: 100, Errors: 5, WallTime: 10 * time.Second,
		P50: 100 * time.Millisecond, P99: 300 * time.Millisecond, MaxLatency: 500 * time.Millisecond,
	}
	require.NoError(t, sink.Write(sum, "test-scenario"))

	assert.Equal(t, "/loki/api/v1/push", path)
	assert.Equal(t, "application/json", contentType)
	assert.Equal(t, "tenant-a", orgID)

	require.Len(t, payload.Streams, 1)
	st := payload.Streams[0]
	assert.Equal(t, "ramplio", st.Stream["job"])
	assert.Equal(t, "test-scenario", st.Stream["scenario"])
	assert.Equal(t, "staging", st.Stream["env"])
	assert.Equal(t, "tw", st.Stream["region"])

	require.Len(t, st.Values, 1)
	var line map[string]any
	require.NoError(t, json.Unmarshal([]byte(st.Values[0][1]), &line))
	assert.Equal(t, "global", line["type"])
	assert.EqualValues(t, 100, line["total"])
	assert.EqualValues(t, 5, line["errors"])
}

func TestLokiSink_WriteDetailed_IncludesStepsAndGroups(t *testing.T) {
	var payload lokiPush
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sink, err := NewLokiSink(lokiDSN(server.URL, ""))
	require.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{
		Total: 100, Errors: 2,
		Steps: []metrics.StepSummary{
			{Name: "GET /", Total: 60, Errors: 1, P99: 50 * time.Millisecond},
			{Name: "POST /login", Total: 40, Errors: 1, P99: 80 * time.Millisecond},
		},
		Groups: []metrics.GroupSummary{
			{Name: "checkout", Total: 40, Errors: 1, P99: 90 * time.Millisecond},
		},
	}
	require.NoError(t, sink.WriteDetailed(sum, "detailed"))

	require.Len(t, payload.Streams, 1)
	values := payload.Streams[0].Values
	// 1 global + 2 steps + 1 group = 4 lines.
	require.Len(t, values, 4)

	// Timestamps must be strictly increasing so Loki accepts the batch.
	for i := 1; i < len(values); i++ {
		assert.Greater(t, values[i][0], values[i-1][0], "timestamps must increase")
	}

	types := map[string]int{}
	for _, v := range values {
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(v[1]), &line))
		types[line["type"].(string)]++
	}
	assert.Equal(t, 1, types["global"])
	assert.Equal(t, 2, types["step"])
	assert.Equal(t, 1, types["group"])
}

func TestLokiSink_DSNValidation(t *testing.T) {
	t.Run("https scheme builds push url", func(t *testing.T) {
		sink, err := NewLokiSink("lokis://loki.example.com:3100")
		require.NoError(t, err)
		assert.Equal(t, "https://loki.example.com:3100/loki/api/v1/push", sink.pushURL)
	})

	t.Run("missing host fails", func(t *testing.T) {
		_, err := NewLokiSink("loki://")
		assert.Error(t, err)
	})

	t.Run("default job label present", func(t *testing.T) {
		sink, err := NewLokiSink("loki://localhost:3100")
		require.NoError(t, err)
		assert.Equal(t, "ramplio", sink.labels["job"])
	})
}

func TestParseSink_RoutesLoki(t *testing.T) {
	sink, err := ParseSink("loki://localhost:3100?labels=env=ci")
	require.NoError(t, err)
	_, ok := sink.(*LokiSink)
	assert.True(t, ok, "loki:// DSN should produce a *LokiSink")
}
