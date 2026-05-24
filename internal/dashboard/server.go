package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

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
	State          State          `json:"state"`
	Result         *RunResult     `json:"result,omitempty"`
	ScenarioInfo   *ScenarioMeta  `json:"scenario_info,omitempty"`
	GuidedProfile  *GuidedProfile `json:"guided_profile,omitempty"` // non-nil during a guided test
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
	if err := s.ctrl.LoadScenario(body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
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
	return wsMessage{
		RPS:          snap.RPS,
		Total:        snap.Total,
		Errors:       snap.Errors,
		ErrorPct:     errPct,
		MeanMs:       snap.MeanLatency.Milliseconds(),
		P50Ms:        snap.P50.Milliseconds(),
		P90Ms:        snap.P90.Milliseconds(),
		P95Ms:        snap.P95.Milliseconds(),
		P99Ms:        snap.P99.Milliseconds(),
		ActiveVUs:    snap.ActiveVUs,
		StageCurrent: snap.StageCurrent,
		StageTotal:   snap.StageTotal,
		StagePct:     snap.StagePct,
		ElapsedS:     snap.Elapsed.Seconds(),
		State:         s.ctrl.State(),
		Result:        s.ctrl.Result(),
		ScenarioInfo:  s.ctrl.ScenarioInfo(),
		GuidedProfile: s.ctrl.ActiveGuidedProfile(),
	}
}
