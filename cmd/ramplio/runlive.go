package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/machiko/ramplio/v3/internal/reporter"
)

// defaultProgressInterval is how often the non-TTY progress line is printed.
const defaultProgressInterval = 2 * time.Second

// errRunInterrupted is returned by `run` when Ctrl-C or SIGTERM stopped the
// test early: the results are partial, so the run must not look successful.
var errRunInterrupted = errors.New("測試被中斷，結果不完整（未存執行紀錄）")

// liveEnv is what watching a run depends on from the outside world: whether
// stdout is a terminal, the live TUI, and where stop signals come from.
type liveEnv struct {
	isTTY   func() bool
	runTUI  func(reporter.LiveProvider, context.CancelFunc, <-chan struct{}) error
	signals func() (<-chan os.Signal, func())
}

// newLiveEnv builds the environment for one run; tests replace it.
var newLiveEnv = defaultLiveEnv

func defaultLiveEnv() liveEnv {
	return liveEnv{
		isTTY:  isTerminal,
		runTUI: reporter.RunTUI,
		signals: func() (<-chan os.Signal, func()) {
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
			return sigs, func() { signal.Stop(sigs) }
		},
	}
}

// shouldUseTUI reports whether the live TUI should be shown: only on a real
// terminal, and only when the user did not opt out with --no-tui.
func shouldUseTUI(noTUI, isTTY bool) bool {
	return !noTUI && isTTY
}

// watchOpts selects how a run is shown while it is in progress.
type watchOpts struct {
	noTUI bool
	// progress prints plain progress lines when the TUI is not used.
	progress bool
	interval time.Duration
}

// runStopper cancels a run once and remembers that it was stopped early.
type runStopper struct {
	once    sync.Once
	cancel  context.CancelFunc
	stopped chan struct{}
}

func newRunStopper(cancel context.CancelFunc) *runStopper {
	return &runStopper{cancel: cancel, stopped: make(chan struct{})}
}

func (s *runStopper) stop() {
	s.once.Do(func() {
		close(s.stopped)
		s.cancel()
	})
}

func (s *runStopper) wasStopped() bool {
	select {
	case <-s.stopped:
		return true
	default:
		return false
	}
}

// watchRun shows a running test (live TUI on a terminal, otherwise optional
// progress lines) until it finishes or is stopped by Ctrl-C / SIGTERM. It
// reports whether the run was interrupted. The run may still be winding down
// (teardown) when watchRun returns after an interruption; callers wait on done.
func watchRun(env liveEnv, opts watchOpts, provider reporter.LiveProvider, cancel context.CancelFunc, done <-chan struct{}) bool {
	stopper := newRunStopper(cancel)
	sigs, stopSignals := env.signals()
	defer stopSignals()

	useTUI := shouldUseTUI(opts.noTUI, env.isTTY())
	go watchSignals(sigs, stopSignals, stopper, done, !useTUI)

	if useTUI {
		watchTUI(env, provider, stopper, done)
	} else {
		watchPlain(opts, provider, stopper, done)
	}
	return stopper.wasStopped()
}

// watchSignals stops the run on the first signal. Signals are then released
// so a second Ctrl-C kills the process (skipping cleanup), as before.
func watchSignals(sigs <-chan os.Signal, stopSignals func(), stopper *runStopper, done <-chan struct{}, announce bool) {
	select {
	case <-done:
		return
	case <-sigs:
	}
	stopSignals()
	select {
	case <-done:
		return // finished on its own at the same moment: not an interruption
	default:
	}
	if announce {
		fmt.Fprintln(os.Stderr, "\nInterrupted — stopping the test...")
	}
	stopper.stop()
}

// watchTUI runs the live TUI. The TUI cancels through the stopper when the
// user presses Ctrl-C. If it returns cleanly while the run is still going, it
// was quit by a signal (bubbletea handles SIGTERM itself), so stop the run.
// If it fails, keep waiting: the run continues and signals still stop it.
func watchTUI(env liveEnv, provider reporter.LiveProvider, stopper *runStopper, done <-chan struct{}) {
	err := env.runTUI(provider, stopper.stop, done)
	select {
	case <-done:
		return
	default:
	}
	if err == nil {
		stopper.stop()
		return
	}
	select {
	case <-done:
	case <-stopper.stopped:
	}
}

// watchPlain waits for the run without a TUI, printing progress lines when asked.
func watchPlain(opts watchOpts, provider reporter.LiveProvider, stopper *runStopper, done <-chan struct{}) {
	var tick <-chan time.Time
	if opts.progress {
		interval := opts.interval
		if interval <= 0 {
			interval = defaultProgressInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-done:
			return
		case <-stopper.stopped:
			return
		case <-tick:
			s := provider.Snapshot()
			fmt.Printf("[%s] VUs=%d  reqs=%d  errors=%d  rps=%.0f  p99=%dms\n",
				s.Elapsed.Round(time.Second), s.ActiveVUs, s.Total, s.Errors, s.RPS, s.P99.Milliseconds())
		}
	}
}
