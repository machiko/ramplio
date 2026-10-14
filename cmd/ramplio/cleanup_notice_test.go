package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWaitForCleanup(t *testing.T) {
	t.Run("silent when the run already finished", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		var out bytes.Buffer
		waitForCleanup(&out, done, "cleaning up…")
		if out.Len() != 0 {
			t.Errorf("a finished run must not print a cleanup notice, got %q", out.String())
		}
	})
	t.Run("announces cleanup while the run winds down", func(t *testing.T) {
		done := make(chan struct{})
		// The run "finishes" only once the notice has been written, so the
		// order is fixed regardless of scheduling.
		out := &closeOnWrite{done: done}
		waitForCleanup(out, done, "cleaning up…")
		if !strings.Contains(out.buf.String(), "cleaning up…") {
			t.Errorf("expected the cleanup notice, got %q", out.buf.String())
		}
	})
}

// closeOnWrite records what is written and closes done on the first write.
type closeOnWrite struct {
	buf  bytes.Buffer
	done chan struct{}
}

func (c *closeOnWrite) Write(p []byte) (int, error) {
	if c.buf.Len() == 0 {
		defer close(c.done)
	}
	return c.buf.Write(p)
}
