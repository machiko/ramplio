package reporter

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

type staticProvider struct{ snap LiveSnapshot }

func (p *staticProvider) Snapshot() LiveSnapshot { return p.snap }

func newTestModel(snap LiveSnapshot) tuiModel {
	done := make(chan struct{})
	return NewTUIModel(&staticProvider{snap: snap}, func() {}, done).(tuiModel)
}

func TestTUIModel_UpdateTick(t *testing.T) {
	snap := LiveSnapshot{Total: 42, RPS: 10.5}
	m := newTestModel(snap)

	m2, cmd := m.Update(tickMsg{})

	tm := m2.(tuiModel)
	assert.Equal(t, int64(42), tm.snap.Total)
	assert.NotNil(t, cmd) // should return next tick
}

func TestTUIModel_UpdateQuitMsg(t *testing.T) {
	m := newTestModel(LiveSnapshot{Total: 5})
	_, cmd := m.Update(QuitMsg{})

	assert.NotNil(t, cmd)
	assert.Equal(t, tea.QuitMsg{}, cmd())
}

func TestTUIModel_UpdateCtrlC(t *testing.T) {
	cancelCalled := false
	done := make(chan struct{})
	m := NewTUIModel(&staticProvider{}, func() { cancelCalled = true }, done).(tuiModel)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	assert.True(t, cancelCalled)
	assert.NotNil(t, cmd)
	assert.Equal(t, tea.QuitMsg{}, cmd())
}

func TestTUIModel_View_WithStages(t *testing.T) {
	m := newTestModel(LiveSnapshot{
		Total: 100, Errors: 2, RPS: 42.5,
		MeanLatency: 80 * time.Millisecond, P99: 500 * time.Millisecond,
		ActiveVUs: 10, StageCurrent: 2, StageTotal: 3, StagePct: 0.6,
		Elapsed: 15 * time.Second,
	})

	view := m.View()

	assert.Contains(t, view, "Stage 2/3")
	assert.Contains(t, view, "500ms")
	assert.Contains(t, view, "42.5")
}

func TestTUIModel_View_NoStages(t *testing.T) {
	m := newTestModel(LiveSnapshot{
		Total: 50, RPS: 25.0, ActiveVUs: 5, Elapsed: 10 * time.Second,
	})

	view := m.View()

	assert.Contains(t, view, "VUs: 5")
	assert.Contains(t, view, "10s")
}

func TestFmtLatency(t *testing.T) {
	assert.Equal(t, "—", fmtLatency(0))
	assert.Equal(t, "500µs", fmtLatency(500*time.Microsecond))
	assert.Equal(t, "150ms", fmtLatency(150*time.Millisecond))
	assert.Equal(t, "2000ms", fmtLatency(2*time.Second))
}

func TestFmtElapsed(t *testing.T) {
	assert.Equal(t, "30s", fmtElapsed(30*time.Second))
	assert.Equal(t, "1m30s", fmtElapsed(90*time.Second))
	assert.Equal(t, "0s", fmtElapsed(0))
}
