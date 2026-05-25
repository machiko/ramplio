package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ramplio/ramplio/internal/importer"
	"github.com/ramplio/ramplio/internal/metrics"
)

// StepMetric is the per-step metrics payload in a wsMessage.
type StepMetric struct {
	Name   string  `json:"name"`
	Total  int64   `json:"total"`
	P50Ms  int64   `json:"p50_ms"`
	P90Ms  int64   `json:"p90_ms"`
	P95Ms  int64   `json:"p95_ms"`
	P99Ms  int64   `json:"p99_ms"`
	ErrPct float64 `json:"err_pct"`
}

// GroupMetric is the per-group aggregated metrics payload in a wsMessage.
type GroupMetric struct {
	Name   string  `json:"name"`
	Total  int64   `json:"total"`
	P50Ms  int64   `json:"p50_ms"`
	P95Ms  int64   `json:"p95_ms"`
	P99Ms  int64   `json:"p99_ms"`
	ErrPct float64 `json:"err_pct"`
}

// wsMessage is the JSON payload pushed to every connected dashboard client.
type wsMessage struct {
	RPS            float64        `json:"rps"`
	Total          int64          `json:"total"`
	Errors         int64          `json:"errors"`
	ErrorPct       float64        `json:"error_pct"`
	MeanMs         int64          `json:"mean_ms"`
	P50Ms          int64          `json:"p50_ms"`
	P90Ms          int64          `json:"p90_ms"`
	P95Ms          int64          `json:"p95_ms"`
	P99Ms          int64          `json:"p99_ms"`
	ActiveVUs      int            `json:"active_vus"`
	StageCurrent   int            `json:"stage_current"`
	StageTotal     int            `json:"stage_total"`
	StagePct       float64        `json:"stage_pct"`
	ElapsedS       float64        `json:"elapsed_s"`
	State         State          `json:"state"`
	Result        *RunResult     `json:"result,omitempty"`
	ScenarioInfo  *ScenarioMeta  `json:"scenario_info,omitempty"`
	GuidedProfile  *GuidedProfile     `json:"guided_profile,omitempty"` // non-nil during a guided test
	StepMetrics    []StepMetric       `json:"step_metrics,omitempty"`
	GroupMetrics   []GroupMetric      `json:"group_metrics,omitempty"`
	DiscoverMode    bool                 `json:"discover_mode,omitempty"`
	DiscoverProbes  []DiscoverProbeSnap  `json:"discover_probes,omitempty"`
	DiscoverResult  *DiscoverResultSnap  `json:"discover_result,omitempty"`
	DiscoverCurrent *DiscoverCurrentSnap `json:"discover_current,omitempty"`
	DiscoverProbeSeq []int              `json:"discover_probe_seq,omitempty"`
}

// Server serves the embedded dashboard HTML and streams live metrics over WebSocket.
// It also exposes a REST control API for starting and stopping tests.
type Server struct {
	ctrl     Controller
	port     int
	upgrader websocket.Upgrader
	addr     string
	ctx      context.Context
}

// New creates a dashboard Server backed by the given Controller.
func New(ctrl Controller, port int) *Server {
	return &Server{
		ctrl: ctrl,
		port: port,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// Addr returns the actual bound address (host:port) after Start() has been called.
func (s *Server) Addr() string { return s.addr }

// Start begins serving the dashboard in the background. It returns when the
// HTTP listener is ready. The server shuts down gracefully when ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveDashboard)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/run", s.handleRun)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/scenario", s.handleScenario)
	mux.HandleFunc("/api/import-har", s.handleImportHAR)
	mux.HandleFunc("/api/report", s.handleReport)
	mux.HandleFunc("/api/discover", s.handleDiscover)

	srv := &http.Server{Handler: mux}
	s.ctx = ctx

	ready := make(chan error, 1)
	go func() {
		ln, err := newListener(s.port)
		if err != nil {
			ready <- err
			return
		}
		s.addr = ln.Addr().String()
		ready <- nil

		go func() {
			<-ctx.Done()
			shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
		}()

		_ = srv.Serve(ln)
	}()

	return <-ready
}

func (s *Server) serveDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(dashboardHTML)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			data, _ := json.Marshal(s.buildWSMessage())
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.ctrl.Start(req); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.ctrl.Stop()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleImportHAR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MB
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	yaml, err := importer.ConvertBytes(data, importer.DefaultOptions(), "upload.har")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.ctrl.LoadScenario(yaml, ""); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleScenario(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	if err := s.ctrl.LoadScenario(body, ""); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Generate into a buffer first so any template error returns a clean HTTP
	// error before headers are sent. Writing partial HTML to w and then calling
	// http.Error leaves the browser with an incomplete response that hangs.
	var buf bytes.Buffer
	if err := s.ctrl.WriteReport(&buf); err != nil {
		http.Error(w, "report not available: run a test first", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ramplio-report.html"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	type response struct {
		State  State      `json:"state"`
		Result *RunResult `json:"result,omitempty"`
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response{
		State:  s.ctrl.State(),
		Result: s.ctrl.Result(),
	})
}

func (s *Server) buildWSMessage() wsMessage {
	snap := s.ctrl.Snapshot()
	errPct := 0.0
	if snap.Total > 0 {
		errPct = float64(snap.Errors) / float64(snap.Total) * 100
	}
	msg := wsMessage{
		RPS:           snap.RPS,
		Total:         snap.Total,
		Errors:        snap.Errors,
		ErrorPct:      errPct,
		MeanMs:        snap.MeanLatency.Milliseconds(),
		P50Ms:         snap.P50.Milliseconds(),
		P90Ms:         snap.P90.Milliseconds(),
		P95Ms:         snap.P95.Milliseconds(),
		P99Ms:         snap.P99.Milliseconds(),
		ActiveVUs:     snap.ActiveVUs,
		StageCurrent:  snap.StageCurrent,
		StageTotal:    snap.StageTotal,
		StagePct:      snap.StagePct,
		ElapsedS:      snap.Elapsed.Seconds(),
		State:         s.ctrl.State(),
		Result:        s.ctrl.Result(),
		ScenarioInfo:  s.ctrl.ScenarioInfo(),
		GuidedProfile: s.ctrl.ActiveGuidedProfile(),
	}
	if len(snap.StepMetrics) > 0 {
		msg.StepMetrics = toWSStepMetrics(snap.StepMetrics)
	}
	if len(snap.GroupMetrics) > 0 {
		msg.GroupMetrics = toWSGroupMetrics(snap.GroupMetrics)
	}
	probes, discResult, discCurrent, discSeq, discActive := s.ctrl.DiscoverProgress()
	if discActive {
		msg.DiscoverMode = true
		msg.DiscoverProbes = probes
		msg.DiscoverResult = discResult
		msg.DiscoverCurrent = discCurrent
		msg.DiscoverProbeSeq = discSeq
	}
	return msg
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req DiscoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.ctrl.StartDiscover(req); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func toWSStepMetrics(steps []metrics.StepSummary) []StepMetric {
	out := make([]StepMetric, len(steps))
	for i, s := range steps {
		errPct := 0.0
		if s.Total > 0 {
			errPct = float64(s.Errors) / float64(s.Total) * 100
		}
		out[i] = StepMetric{
			Name:   s.Name,
			Total:  s.Total,
			P50Ms:  s.P50.Milliseconds(),
			P90Ms:  s.P90.Milliseconds(),
			P95Ms:  s.P95.Milliseconds(),
			P99Ms:  s.P99.Milliseconds(),
			ErrPct: errPct,
		}
	}
	return out
}

func toWSGroupMetrics(groups []metrics.GroupSummary) []GroupMetric {
	out := make([]GroupMetric, len(groups))
	for i, g := range groups {
		errPct := 0.0
		if g.Total > 0 {
			errPct = float64(g.Errors) / float64(g.Total) * 100
		}
		out[i] = GroupMetric{
			Name:   g.Name,
			Total:  g.Total,
			P50Ms:  g.P50.Milliseconds(),
			P95Ms:  g.P95.Milliseconds(),
			P99Ms:  g.P99.Milliseconds(),
			ErrPct: errPct,
		}
	}
	return out
}
