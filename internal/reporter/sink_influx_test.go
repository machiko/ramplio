package reporter

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/stretchr/testify/assert"
)

func TestInfluxSink_Write_SendsBasicMetrics(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dsn := "influxdb://" + strings.TrimPrefix(server.URL, "http://") + "/test?token=mytoken&org=myorg"
	sink, err := NewInfluxSink(dsn)
	assert.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{
		Total:      100,
		Errors:     5,
		WallTime:   10 * time.Second,
		P50:        100 * time.Millisecond,
		P95:        200 * time.Millisecond,
		P99:        300 * time.Millisecond,
		MaxLatency: 500 * time.Millisecond,
		BytesIn:    50000,
	}

	err = sink.Write(sum, "test-scenario")
	assert.NoError(t, err)

	// Verify line protocol format
	assert.Contains(t, receivedBody, "ramplio_results")
	assert.Contains(t, receivedBody, "scenario=test-scenario")
	assert.Contains(t, receivedBody, "type=global")
	assert.Contains(t, receivedBody, "total=100i")
	assert.Contains(t, receivedBody, "errors=5i")
}

func TestInfluxSink_WriteDetailed_SendsGlobalStepsAndGroups(t *testing.T) {
	var receivedBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, string(body))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dsn := "influxdb://" + strings.TrimPrefix(server.URL, "http://") + "/test?token=mytoken&org=myorg"
	sink, err := NewInfluxSink(dsn)
	assert.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{
		Total:      100,
		Errors:     5,
		WallTime:   10 * time.Second,
		P50:        100 * time.Millisecond,
		P95:        200 * time.Millisecond,
		P99:        300 * time.Millisecond,
		MaxLatency: 500 * time.Millisecond,
		BytesIn:    50000,
		Steps: []metrics.StepSummary{
			{
				Name:   "get-user",
				Total:  50,
				Errors: 2,
				P50:    80 * time.Millisecond,
				P95:    150 * time.Millisecond,
				P99:    200 * time.Millisecond,
			},
		},
		Groups: []metrics.GroupSummary{
			{
				Name:   "auth-flow",
				Total:  100,
				Errors: 5,
				P50:    100 * time.Millisecond,
				P95:    200 * time.Millisecond,
				P99:    300 * time.Millisecond,
			},
		},
	}

	err = sink.WriteDetailed(sum, "test-scenario")
	assert.NoError(t, err)

	// Should have sent 3 requests (global + step + group)
	assert.Equal(t, 1, len(receivedBodies), "should have sent in one request")

	body := receivedBodies[0]
	lines := strings.Split(strings.TrimSpace(body), "\n")

	// 3 lines: global, step, group
	assert.Equal(t, 3, len(lines), "should have 3 measurement lines")

	// Verify global line
	assert.Contains(t, lines[0], "type=global")
	assert.Contains(t, lines[0], "total=100i")

	// Verify step line
	assert.Contains(t, lines[1], "type=step")
	assert.Contains(t, lines[1], "name=get-user")
	assert.Contains(t, lines[1], "total=50i")

	// Verify group line
	assert.Contains(t, lines[2], "type=group")
	assert.Contains(t, lines[2], "name=auth-flow")
	assert.Contains(t, lines[2], "total=100i")
}

func TestInfluxSink_WriteDetailed_EmptyStepsAndGroups(t *testing.T) {
	var receivedBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, string(body))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dsn := "influxdb://" + strings.TrimPrefix(server.URL, "http://") + "/test?token=mytoken&org=myorg"
	sink, err := NewInfluxSink(dsn)
	assert.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{
		Total:      100,
		Errors:     0,
		WallTime:   10 * time.Second,
		P50:        100 * time.Millisecond,
		P95:        200 * time.Millisecond,
		P99:        300 * time.Millisecond,
		MaxLatency: 500 * time.Millisecond,
		BytesIn:    50000,
		Steps:      nil,
		Groups:     nil,
	}

	err = sink.WriteDetailed(sum, "test-scenario")
	assert.NoError(t, err)

	// Should have sent 1 request (global only)
	assert.Equal(t, 1, len(receivedBodies))
	lines := strings.Split(strings.TrimSpace(receivedBodies[0]), "\n")
	assert.Equal(t, 1, len(lines), "should have only global line")
}

func TestInfluxSink_EscapeTag_ProperlyEscapesSpecialChars(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with space", "with\\ space"},
		{"with,comma", "with\\,comma"},
		{"with space,and comma", "with\\ space\\,and\\ comma"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := escapeTag(test.input)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestInfluxSink_ErrorResponse_ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid request"))
	}))
	defer server.Close()

	dsn := "influxdb://" + strings.TrimPrefix(server.URL, "http://") + "/test?token=mytoken"
	sink, err := NewInfluxSink(dsn)
	assert.NoError(t, err)
	defer sink.Close()

	sum := metrics.Summary{Total: 100, Errors: 5, WallTime: 10 * time.Second}
	err = sink.Write(sum, "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}
