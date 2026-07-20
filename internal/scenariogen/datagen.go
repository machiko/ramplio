package scenariogen

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"

	"github.com/google/uuid"
)

// DataColumnKind identifies how a data-file column's values are generated.
type DataColumnKind string

const (
	KindIntSeq      DataColumnKind = "int_seq"     // sequential integers (start, start+1, ...)
	KindUUID        DataColumnKind = "uuid"        // random UUID v4 per row
	KindEmail       DataColumnKind = "email"       // loadtest+N@example.com
	KindList        DataColumnKind = "list"        // cycles through ListValues
	KindPlaceholder DataColumnKind = "placeholder" // "<name>" for the user to fill in
)

// DataColumn declares one column of a generated data file.
type DataColumn struct {
	Name       string
	Kind       DataColumnKind
	ListValues []string // KindList only
	Start      int      // KindIntSeq only; honored only when StartSet is true
	StartSet   bool     // KindIntSeq only; distinguishes an explicit start (incl. 0) from unset
}

// GenerateCSV produces CSV content: a header row of column names followed by
// `rows` data rows. Values are deterministic (except UUID) and row-unique so
// virtual users receive distinct inputs and caching does not skew results.
//
// The output is written through encoding/csv, so it round-trips cleanly through
// scenarios.LoadDataFile, including values that contain commas or quotes.
func GenerateCSV(cols []DataColumn, rows int) (string, error) {
	if len(cols) == 0 {
		return "", fmt.Errorf("至少需要一個欄位才能產生資料檔")
	}
	if rows < 1 {
		return "", fmt.Errorf("資料列數必須 >= 1（目前為 %d）", rows)
	}
	seen := make(map[string]bool, len(cols))
	for _, c := range cols {
		if c.Name == "" {
			return "", fmt.Errorf("欄位名稱不可為空")
		}
		if seen[c.Name] {
			return "", fmt.Errorf("欄位名稱 %q 重複；CSV 標頭重複會導致資料在載入時互相覆蓋", c.Name)
		}
		seen[c.Name] = true
		if c.Kind == KindList && len(c.ListValues) == 0 {
			return "", fmt.Errorf("欄位 %q 為清單型別，但未提供任何值", c.Name)
		}
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.Name
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
func cellValue(c DataColumn, r int) string {
	switch c.Kind {
	case KindIntSeq:
		start := 1 // default when the user did not specify a starting number
		if c.StartSet {
			start = c.Start
		}
		return strconv.Itoa(start + r)
	case KindUUID:
		return uuid.NewString()
	case KindEmail:
		return fmt.Sprintf("loadtest+%d@example.com", r+1)
	case KindList:
		return c.ListValues[r%len(c.ListValues)]
	case KindPlaceholder:
		return "<" + c.Name + ">"
	default:
		return "<" + c.Name + ">"
	}
}
