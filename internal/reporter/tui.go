package reporter

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/machiko/ramplio/v3/internal/metrics"
)

// LiveSnapshot is a point-in-time view of a running load test.
type LiveSnapshot struct {
	Total        int64
	Errors       int64
	RPS          float64
	MeanLatency  time.Duration
	P50          time.Duration
	P90          time.Duration
	P95          time.Duration
	P99          time.Duration
	ActiveVUs    int
	StageCurrent int
	StageTotal   int
	StagePct     float64 // 0–1 progress within current stage
	Elapsed      time.Duration
	// StepMetrics is non-nil only when running a multi-step scenario.
	StepMetrics []metrics.StepSummary
	// GroupMetrics is non-nil only when steps carry a non-empty Group field.
	GroupMetrics []metrics.GroupSummary
}

// LiveProvider supplies live snapshots during a running load test.
type LiveProvider interface {
	Snapshot() LiveSnapshot
}

// QuitMsg signals the TUI to exit after the load test finishes.
type QuitMsg struct{}

type tickMsg struct{}

type tuiModel struct {
	provider LiveProvider
	cancel   context.CancelFunc
	done     <-chan struct{}
	snap     LiveSnapshot
}

// NewTUIModel creates a bubbletea model for the live dashboard.
func NewTUIModel(provider LiveProvider, cancel context.CancelFunc, done <-chan struct{}) tea.Model {
	return tuiModel{
		provider: provider,
		cancel:   cancel,
		done:     done,
		snap:     provider.Snapshot(),
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(doTick(), m.waitDone())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.cancel()
			return m, tea.Quit
		}
	case tickMsg:
		m.snap = m.provider.Snapshot()
		return m, doTick()
	case QuitMsg:
		m.snap = m.provider.Snapshot()
		return m, tea.Quit
	}
	return m, nil
}

func (m tuiModel) View() string {
	s := m.snap
	purple := lipgloss.Color("99")

	var line1 string
	if s.StageTotal > 0 {
		filled := int(s.StagePct * 10)
		if filled > 10 {
			filled = 10
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
		line1 = fmt.Sprintf("Stage %d/%d  [%s] %3.0f%%  ·  VUs: %d",
			s.StageCurrent, s.StageTotal, bar, s.StagePct*100, s.ActiveVUs)
	} else {
		line1 = fmt.Sprintf("VUs: %d  ·  Elapsed: %s", s.ActiveVUs, fmtElapsed(s.Elapsed))
	}

	errPct := 0.0
	if s.Total > 0 {
		errPct = float64(s.Errors) / float64(s.Total) * 100
	}
	line2 := fmt.Sprintf("RPS: %6.1f  │  p99: %-8s  │  Err: %.2f%%",
		s.RPS, fmtLatency(s.P99), errPct)
	line3 := fmt.Sprintf("Total: %-7d │  Mean: %-8s  │  Elapsed: %s",
		s.Total, fmtLatency(s.MeanLatency), fmtElapsed(s.Elapsed))

	titleStyle := lipgloss.NewStyle().Foreground(purple).Bold(true)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(purple).
		Padding(0, 1)

	inner := line1 + "\n" + line2 + "\n" + line3
	out := "\n" + titleStyle.Render("Ramplio") + "\n" + boxStyle.Render(inner) + "\n"
	if len(s.StepMetrics) > 0 {
		out += renderStepTable(s.StepMetrics)
	}
	return out
}

func renderStepTable(steps []metrics.StepSummary) string {
	const nameW = 34
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  %-*s  %7s  %7s  %7s  %6s\n",
		nameW, "Step", "Total", "p50", "p99", "Err%"))
	sb.WriteString("  " + strings.Repeat("─", nameW+36) + "\n")
	for _, s := range steps {
		errPct := 0.0
		if s.Total > 0 {
			errPct = float64(s.Errors) / float64(s.Total) * 100
		}
		name := s.Name
		if len(name) > nameW {
			name = name[:nameW-3] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %-*s  %7d  %7s  %7s  %5.1f%%\n",
			nameW, name, s.Total, fmtLatency(s.P50), fmtLatency(s.P99), errPct))
	}
	return sb.String()
}

// RunTUI starts the bubbletea program and blocks until the test ends or Ctrl+C is pressed.
func RunTUI(provider LiveProvider, cancel context.CancelFunc, done <-chan struct{}) error {
	p := tea.NewProgram(NewTUIModel(provider, cancel, done))
	_, err := p.Run()
	return err
}

func (m tuiModel) waitDone() tea.Cmd {
	return func() tea.Msg {
		<-m.done
		return QuitMsg{}
	}
}

func doTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func fmtLatency(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

func fmtElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}
