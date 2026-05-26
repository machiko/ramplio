package reporter

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
)

// InfluxSink pushes results to an InfluxDB v2 instance using the line protocol.
// DSN format: influxdb://host:port/bucket?token=TOKEN&org=ORG
type InfluxSink struct {
	writeURL string
	token    string
	client   *http.Client
}

func NewInfluxSink(dsn string) (*InfluxSink, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("influx sink: invalid DSN: %w", err)
	}

	host := u.Host
	bucket := strings.TrimPrefix(u.Path, "/")
	token := u.Query().Get("token")
	org := u.Query().Get("org")
	if bucket == "" {
		return nil, fmt.Errorf("influx sink: bucket required in path (e.g. influxdb://host/mybucket)")
	}

	scheme := "http"
	if u.Scheme == "influxdbs" {
		scheme = "https"
	}
	writeURL := fmt.Sprintf("%s://%s/api/v2/write?bucket=%s&precision=ms",
		scheme, host, url.QueryEscape(bucket))
	if org != "" {
		writeURL += "&org=" + url.QueryEscape(org)
	}

	return &InfluxSink{
		writeURL: writeURL,
		token:    token,
		client:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (s *InfluxSink) Write(sum metrics.Summary, scenarioName string) error {
	ts := time.Now().UnixMilli()
	tags := fmt.Sprintf("scenario=%s", escapeTag(scenarioName))

	lines := []string{
		fmt.Sprintf("ramplio_results,%s total=%di,errors=%di,error_rate=%.4f,rps=%.2f,p50_ms=%di,p90_ms=%di,p95_ms=%di,p99_ms=%di,max_ms=%di %d",
			tags,
			sum.Total, sum.Errors, sum.ErrorRate(), sum.RPS(),
			sum.P50.Milliseconds(), sum.P90.Milliseconds(), sum.P95.Milliseconds(),
			sum.P99.Milliseconds(), sum.MaxLatency.Milliseconds(),
			ts,
		),
	}

	body := strings.Join(lines, "\n")
	req, err := http.NewRequest(http.MethodPost, s.writeURL, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if s.token != "" {
		req.Header.Set("Authorization", "Token "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("influx sink: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("influx sink: server returned %s", resp.Status)
	}
	return nil
}

func (s *InfluxSink) Close() error { return nil }

func escapeTag(v string) string {
	v = strings.ReplaceAll(v, " ", "\\ ")
	v = strings.ReplaceAll(v, ",", "\\,")
	return v
}
