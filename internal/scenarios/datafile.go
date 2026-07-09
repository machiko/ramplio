package scenarios

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadDataFile reads a CSV or JSON file and returns rows as key-value maps.
//
// CSV: first row is treated as column headers; each subsequent row is a data row.
// JSON: must be an array of string-valued objects, e.g. [{"email":"a@b.com",...}].
//
// Data values are accessed in templates as {{data.column_name}}.
func LoadDataFile(path string) ([]map[string]string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".csv":
		return loadCSV(path)
	case ".json":
		return loadJSONData(path)
	default:
		return nil, fmt.Errorf("unsupported data file format %q — use .csv or .json", ext)
	}
}

func loadCSV(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening CSV: %w", err)
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // allow rows with varying column counts
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading CSV: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV %q must have a header row and at least one data row", path)
	}

	headers := records[0]
	rows := make([]map[string]string, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := make(map[string]string, len(headers))
		for i, h := range headers {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func loadJSONData(path string) ([]map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("opening JSON: %w", err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parsing JSON data file: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("JSON data file %q is empty", path)
	}
	return rows, nil
}
