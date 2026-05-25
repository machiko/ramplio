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
func ParseSink(dsn string) (Sink, error) {
	switch {
	case strings.HasPrefix(dsn, "csv:"):
		path := strings.TrimPrefix(dsn, "csv:")
		return NewCsvSink(path)
	case strings.HasPrefix(dsn, "influxdb://"):
		return NewInfluxSink(dsn)
	default:
		return nil, fmt.Errorf("unknown sink scheme in %q — supported: csv:<path>, influxdb://...", dsn)
	}
}
