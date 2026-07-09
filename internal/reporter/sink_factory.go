package reporter

import (
	"fmt"
	"strings"
)

// ParseSink creates a Sink from a DSN string.
//
// Supported formats:
//
//	csv:path/to/file.csv         — append rows to a CSV file
//	influxdb://host:port/bucket  — push to InfluxDB v2 (token via ?token=)
//	loki://host:port             — push to Grafana Loki (labels via ?labels=k=v,...)
//	otel://host:4318             — push to an OpenTelemetry collector (OTLP/HTTP)
func ParseSink(dsn string) (Sink, error) {
	switch {
	case strings.HasPrefix(dsn, "csv:"):
		path := strings.TrimPrefix(dsn, "csv:")
		return NewCsvSink(path)
	case strings.HasPrefix(dsn, "influxdb://"), strings.HasPrefix(dsn, "influxdbs://"):
		return NewInfluxSink(dsn)
	case strings.HasPrefix(dsn, "loki://"), strings.HasPrefix(dsn, "lokis://"):
		return NewLokiSink(dsn)
	case strings.HasPrefix(dsn, "otel://"), strings.HasPrefix(dsn, "otels://"):
		return NewOtelSink(dsn)
	default:
		return nil, fmt.Errorf("unknown sink scheme in %q — supported: csv:<path>, influxdb://, influxdbs://, loki://, lokis://, otel://, otels://", dsn)
	}
}
