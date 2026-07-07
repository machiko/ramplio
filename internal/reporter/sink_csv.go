package reporter

import (
	"encoding/csv"
	"fmt"
	"os"
	"time"

	"github.com/machiko/ramplio/v2/internal/metrics"
)

// CsvSink appends one summary row per test run to a CSV file.
// The file is created if it does not exist; headers are written only when the
// file is new (size == 0 at open time).
type CsvSink struct {
	path string
	f    *os.File
	w    *csv.Writer
}

var csvHeaders = []string{
	"timestamp", "scenario", "type", "name", "duration_s",
	"total", "errors", "error_rate_pct",
	"rps", "p50_ms", "p90_ms", "p95_ms", "p99_ms", "max_ms",
}

func NewCsvSink(path string) (*CsvSink, error) {
	needHeader := false
	info, err := os.Stat(path)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		needHeader = true
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("csv sink: open %q: %w", path, err)
	}
	w := csv.NewWriter(f)
	if needHeader {
		if err := w.Write(csvHeaders); err != nil {
			f.Close()
			return nil, err
		}
		w.Flush()
	}
	return &CsvSink{path: path, f: f, w: w}, nil
}

func (s *CsvSink) Write(sum metrics.Summary, scenarioName string) error {
	return s.writeRow(sum, scenarioName, "global", "")
}

// WriteDetailed outputs global summary plus per-step and per-group breakdowns.
func (s *CsvSink) WriteDetailed(sum metrics.Summary, scenarioName string) error {
	if err := s.writeRow(sum, scenarioName, "global", ""); err != nil {
		return err
	}
	for _, step := range sum.Steps {
		if err := s.writeRowFromStep(step, scenarioName, "step"); err != nil {
			return err
		}
	}
	for _, group := range sum.Groups {
		if err := s.writeRowFromGroup(group, scenarioName, "group"); err != nil {
			return err
		}
	}
	return nil
}

func (s *CsvSink) writeRow(sum metrics.Summary, scenarioName, rowType, name string) error {
	row := []string{
		time.Now().UTC().Format(time.RFC3339),
		scenarioName,
		rowType,
		name,
		fmt.Sprintf("%.3f", sum.WallTime.Seconds()),
		fmt.Sprintf("%d", sum.Total),
		fmt.Sprintf("%d", sum.Errors),
		fmt.Sprintf("%.4f", sum.ErrorRate()),
		fmt.Sprintf("%.2f", sum.RPS()),
		fmt.Sprintf("%d", sum.P50.Milliseconds()),
		fmt.Sprintf("%d", sum.P90.Milliseconds()),
		fmt.Sprintf("%d", sum.P95.Milliseconds()),
		fmt.Sprintf("%d", sum.P99.Milliseconds()),
		fmt.Sprintf("%d", sum.MaxLatency.Milliseconds()),
	}
	if err := s.w.Write(row); err != nil {
		return err
	}
	s.w.Flush()
	return s.w.Error()
}

func (s *CsvSink) writeRowFromStep(step metrics.StepSummary, scenarioName, rowType string) error {
	errorRate := 0.0
	if step.Total > 0 {
		errorRate = float64(step.Errors) / float64(step.Total) * 100
	}
	row := []string{
		time.Now().UTC().Format(time.RFC3339),
		scenarioName,
		rowType,
		step.Name,
		"",
		fmt.Sprintf("%d", step.Total),
		fmt.Sprintf("%d", step.Errors),
		fmt.Sprintf("%.4f", errorRate),
		"",
		fmt.Sprintf("%d", step.P50.Milliseconds()),
		fmt.Sprintf("%d", step.P90.Milliseconds()),
		fmt.Sprintf("%d", step.P95.Milliseconds()),
		fmt.Sprintf("%d", step.P99.Milliseconds()),
		"",
	}
	if err := s.w.Write(row); err != nil {
		return err
	}
	s.w.Flush()
	return s.w.Error()
}

func (s *CsvSink) writeRowFromGroup(group metrics.GroupSummary, scenarioName, rowType string) error {
	errorRate := 0.0
	if group.Total > 0 {
		errorRate = float64(group.Errors) / float64(group.Total) * 100
	}
	row := []string{
		time.Now().UTC().Format(time.RFC3339),
		scenarioName,
		rowType,
		group.Name,
		"",
		fmt.Sprintf("%d", group.Total),
		fmt.Sprintf("%d", group.Errors),
		fmt.Sprintf("%.4f", errorRate),
		"",
		fmt.Sprintf("%d", group.P50.Milliseconds()),
		"",
		fmt.Sprintf("%d", group.P95.Milliseconds()),
		fmt.Sprintf("%d", group.P99.Milliseconds()),
		"",
	}
	if err := s.w.Write(row); err != nil {
		return err
	}
	s.w.Flush()
	return s.w.Error()
}

func (s *CsvSink) Close() error { return s.f.Close() }
