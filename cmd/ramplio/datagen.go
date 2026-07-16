package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"

	"github.com/google/uuid"
)

// dataColumnKind identifies how a data-file column's values are generated.
type dataColumnKind string

const (
	colIntSeq      dataColumnKind = "int_seq"     // sequential integers (start, start+1, ...)
	colUUID        dataColumnKind = "uuid"        // random UUID v4 per row
	colEmail       dataColumnKind = "email"       // loadtest+N@example.com
	colList        dataColumnKind = "list"        // cycles through listValues
	colPlaceholder dataColumnKind = "placeholder" // "<name>" for the user to fill in (option A)
)

// dataColumn declares one column of a generated data file.
type dataColumn struct {
	name       string
	kind       dataColumnKind
	listValues []string // colList only
	start      int      // colIntSeq only; honored only when startSet is true
	startSet   bool     // colIntSeq only; distinguishes an explicit start (incl. 0) from unset
}

// generateCSV produces CSV content: a header row of column names followed by
// `rows` data rows. Values are deterministic (except UUID) and row-unique so
// virtual users receive distinct inputs and caching does not skew results.
//
// The output is written through encoding/csv, so it round-trips cleanly through
// scenarios.LoadDataFile, including values that contain commas or quotes.
func generateCSV(cols []dataColumn, rows int) (string, error) {
	if len(cols) == 0 {
		return "", fmt.Errorf("至少需要一個欄位才能產生資料檔")
	}
	if rows < 1 {
		return "", fmt.Errorf("資料列數必須 >= 1（目前為 %d）", rows)
	}
	seen := make(map[string]bool, len(cols))
	for _, c := range cols {
		if c.name == "" {
			return "", fmt.Errorf("欄位名稱不可為空")
		}
		if seen[c.name] {
			return "", fmt.Errorf("欄位名稱 %q 重複；CSV 標頭重複會導致資料在載入時互相覆蓋", c.name)
		}
		seen[c.name] = true
		if c.kind == colList && len(c.listValues) == 0 {
			return "", fmt.Errorf("欄位 %q 為清單型別，但未提供任何值", c.name)
		}
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.name
	}
	if err := w.Write(header); err != nil {
		return "", fmt.Errorf("寫入 CSV 標頭失敗：%w", err)
	}

	for r := 0; r < rows; r++ {
		record := make([]string, len(cols))
		for i, c := range cols {
			record[i] = cellValue(c, r)
		}
		if err := w.Write(record); err != nil {
			return "", fmt.Errorf("寫入 CSV 資料列失敗：%w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return "", fmt.Errorf("產生 CSV 失敗：%w", err)
	}
	return buf.String(), nil
}

// cellValue renders a single cell for column c at zero-based row index r.
func cellValue(c dataColumn, r int) string {
	switch c.kind {
	case colIntSeq:
		start := 1 // default when the user did not specify a starting number
		if c.startSet {
			start = c.start
		}
		return strconv.Itoa(start + r)
	case colUUID:
		return uuid.NewString()
	case colEmail:
		return fmt.Sprintf("loadtest+%d@example.com", r+1)
	case colList:
		return c.listValues[r%len(c.listValues)]
	case colPlaceholder:
		return "<" + c.name + ">"
	default:
		return "<" + c.name + ">"
	}
}
