package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/machiko/ramplio/v3/internal/scenariogen"
)

// defaultGenerateRows is used when the generate request declares data columns but
// an unset or non-positive row count, mirroring the CLI wizard's default.
const defaultGenerateRows = 100

// maxGenerateRows caps generated data rows so a mistyped or hostile count cannot
// make the server allocate an unbounded CSV. Mirrors the CLI wizard's limit.
const maxGenerateRows = 1_000_000

// maxGenerateColumns bounds the declared column count. Real data files have a
// handful of columns; this stops a pathological header.
const maxGenerateColumns = 256

// maxGenerateCells bounds the CSV allocation by its true cost — columns × rows —
// since capping each dimension alone still permits an astronomical product. A
// single-column file may still reach the full row cap.
const maxGenerateCells = 2_000_000

// maxGenerateBytes bounds the request body so a hostile payload cannot exhaust
// memory before generation even starts.
const maxGenerateBytes = 1 << 20 // 1 MB

// defaultDataFileName is used when data columns are declared without a filename,
// so the generated vars_from block always has a target.
const defaultDataFileName = "data.csv"

// GenerateRequest is the wizard input for POST /api/generate. It deserializes
// straight into the shared scenariogen types so the dashboard generator and the
// CLI init wizard produce byte-identical output from the same inputs.
type GenerateRequest struct {
	Name     string                   `json:"name"`
	BaseURL  string                   `json:"base_url"`
	Auth     scenariogen.Auth         `json:"auth"`
	Steps    []scenariogen.Step       `json:"steps"`
	VUs      int                      `json:"vus"`
	Duration string                   `json:"duration"`
	Shape    string                   `json:"shape"`
	ErrPct   string                   `json:"err_pct"`
	P95      string                   `json:"p95"`
	DataFile string                   `json:"data_file"`
	Columns  []scenariogen.DataColumn `json:"columns"`
	Rows     int                      `json:"rows"`
	// Run, when true, also loads the generated scenario into memory (data rows and
	// all) ready to start — closing the gap where a remote dashboard cannot read a
	// vars_from CSV off the server's disk.
	Run bool `json:"run"`
}

// GenerateResponse is returned by POST /api/generate. YAML and CSV are always
// suitable for download; Warnings surfaces {{data.X}} mismatches at generation
// time rather than at run time.
type GenerateResponse struct {
	YAML     string   `json:"yaml"`
	CSV      string   `json:"csv,omitempty"`
	DataFile string   `json:"data_file,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req GenerateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxGenerateBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.BaseURL == "" {
		http.Error(w, "base_url is required", http.StatusBadRequest)
		return
	}
	if len(req.Steps) == 0 {
		http.Error(w, "at least one step is required", http.StatusBadRequest)
		return
	}

	if len(req.Columns) > maxGenerateColumns {
		http.Error(w, fmt.Sprintf("too many data columns (max %d)", maxGenerateColumns), http.StatusBadRequest)
		return
	}
	// Cookie auth references a session CSV that lives only on disk, so a
	// cookie scenario cannot be run straight from the browser (out of v1 scope).
	// Fail loudly rather than loading a scenario whose every request would fail
	// template resolution for {{data.session_cookie}}.
	if req.Run && req.Auth.Kind == "cookie" {
		http.Error(w, "cookie auth cannot be run directly; download the scenario and provide sessions.csv", http.StatusBadRequest)
		return
	}

	// A data file exists only when columns are declared; ignore a stray data_file
	// otherwise so we never emit a vars_from block with no data behind it.
	var dataFile string
	if len(req.Columns) > 0 {
		dataFile = req.DataFile
		if dataFile == "" {
			dataFile = defaultDataFileName
		}
	}

	yaml := scenariogen.GenerateYAML(
		req.Name, req.BaseURL, req.Auth, req.Steps,
		req.VUs, req.Duration, req.Shape, req.ErrPct, req.P95, dataFile)
	warnings := scenariogen.DataParamWarnings(req.Steps, req.Columns)

	var csv string
	if len(req.Columns) > 0 {
		rows := req.Rows
		if rows < 1 {
			rows = defaultGenerateRows
		}
		if rows > maxGenerateRows {
			rows = maxGenerateRows
		}
		if int64(len(req.Columns))*int64(rows) > maxGenerateCells {
			http.Error(w, fmt.Sprintf("data file too large: %d columns × %d rows exceeds %d cells",
				len(req.Columns), rows, maxGenerateCells), http.StatusBadRequest)
			return
		}
		out, err := scenariogen.GenerateCSV(req.Columns, rows)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		csv = out
	}

	if req.Run {
		if err := s.ctrl.LoadScenarioWithData([]byte(yaml), csv); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(GenerateResponse{
		YAML:     yaml,
		CSV:      csv,
		DataFile: dataFile,
		Warnings: warnings,
	})
}
