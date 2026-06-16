package reporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
)

// LokiSink pushes results to a Grafana Loki instance via its push API.
// DSN format: loki://host:port?labels=key=val,key2=val2[&org=TENANT][&token=TOKEN]
// Use lokis:// for HTTPS.
//
// Metrics are emitted as JSON log lines (queryable in Loki with `| json`),
// keeping stream labels low-cardinality (job + scenario + user labels) while
// per-step/group detail lives in the line body.
type LokiSink struct {
	pushURL string
	labels  map[string]string
	orgID   string
	token   string
	client  *http.Client
}

func NewLokiSink(dsn string) (*LokiSink, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("loki sink: invalid DSN: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("loki sink: host required (e.g. loki://localhost:3100)")
	}

	scheme := "http"
	if u.Scheme == "lokis" {
		scheme = "https"
	}

	labels := map[string]string{"job": "ramplio"}
	if raw := u.Query().Get("labels"); raw != "" {
		for _, pair := range strings.Split(raw, ",") {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 && kv[0] != "" {
				labels[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			}
		}
	}

	return &LokiSink{
		pushURL: fmt.Sprintf("%s://%s/loki/api/v1/push", scheme, u.Host),
		labels:  labels,
		orgID:   u.Query().Get("org"),
		token:   u.Query().Get("token"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (s *LokiSink) Write(sum metrics.Summary, scenarioName string) error {
	lines := []lokiLine{globalLine(sum)}
	return s.push(scenarioName, lines)
}

// WriteDetailed adds per-step and per-group lines alongside the global summary.
func (s *LokiSink) WriteDetailed(sum metrics.Summary, scenarioName string) error {
	lines := []lokiLine{globalLine(sum)}
	for _, step := range sum.Steps {
		lines = append(lines, stepLine(step))
	}
	for _, group := range sum.Groups {
		lines = append(lines, groupLine(group))
	}
	return s.push(scenarioName, lines)
}

func (s *LokiSink) Close() error { return nil }

// lokiLine is a single structured metric record serialized as a JSON log line.
type lokiLine map[string]any

func globalLine(sum metrics.Summary) lokiLine {
	return lokiLine{
		"type": "global", "total": sum.Total, "errors": sum.Errors,
		"error_rate": sum.ErrorRate(), "rps": sum.RPS(),
		"p50_ms": sum.P50.Milliseconds(), "p90_ms": sum.P90.Milliseconds(),
		"p95_ms": sum.P95.Milliseconds(), "p99_ms": sum.P99.Milliseconds(),
		"max_ms": sum.MaxLatency.Milliseconds(),
	}
}

func stepLine(step metrics.StepSummary) lokiLine {
	errorRate := 0.0
	if step.Total > 0 {
		errorRate = float64(step.Errors) / float64(step.Total) * 100
	}
	return lokiLine{
		"type": "step", "name": step.Name, "total": step.Total, "errors": step.Errors,
		"error_rate": errorRate,
		"p50_ms":     step.P50.Milliseconds(), "p90_ms": step.P90.Milliseconds(),
		"p95_ms": step.P95.Milliseconds(), "p99_ms": step.P99.Milliseconds(),
	}
}

func groupLine(group metrics.GroupSummary) lokiLine {
	errorRate := 0.0
	if group.Total > 0 {
		errorRate = float64(group.Errors) / float64(group.Total) * 100
	}
	return lokiLine{
		"type": "group", "name": group.Name, "total": group.Total, "errors": group.Errors,
		"error_rate": errorRate,
		"p50_ms":     group.P50.Milliseconds(), "p95_ms": group.P95.Milliseconds(),
		"p99_ms": group.P99.Milliseconds(),
	}
}

// push sends all lines as a single Loki stream. Timestamps are strictly
// increasing (base + index) so Loki accepts every entry in order.
func (s *LokiSink) push(scenarioName string, lines []lokiLine) error {
	labels := make(map[string]string, len(s.labels)+1)
	for k, v := range s.labels {
		labels[k] = v
	}
	labels["scenario"] = scenarioName

	base := time.Now().UnixNano()
	values := make([][2]string, 0, len(lines))
	for i, line := range lines {
		body, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("loki sink: marshal line: %w", err)
		}
		ts := strconv.FormatInt(base+int64(i), 10)
		values = append(values, [2]string{ts, string(body)})
	}

	payload := lokiPush{Streams: []lokiStream{{Stream: labels, Values: values}}}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("loki sink: marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, s.pushURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.orgID != "" {
		req.Header.Set("X-Scope-OrgID", s.orgID)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("loki sink: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("loki sink: server returned %s", resp.Status)
	}
	return nil
}

type lokiPush struct {
	Streams []lokiStream `json:"streams"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}
