package components

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// syncBuffer is a goroutine-safe bytes.Buffer for capturing async writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newTTYStatus builds a Status writing to buf with the TTY rendering path
// forced on, so tests can observe exactly what a terminal would receive.
func newTTYStatus(buf *syncBuffer) *Status {
	s := NewStatus(buf)
	s.noTTY = false
	return s
}

// visibleWidth returns the display-cell width of a segment with escape
// sequences removed.
func visibleWidth(seg string) int {
	return ansi.StringWidth(seg)
}

// TestStatusTTYWritesNoTerminalQueries pins SK-763: the status line must not
// send terminal capability queries (DECRQM mode 2026/2027, etc.). Replies to
// those queries arrive after the short-lived status finishes and get echoed
// to the user's terminal as garbage like `^[[?2026;2$y`.
func TestStatusTTYWritesNoTerminalQueries(t *testing.T) {
	buf := &syncBuffer{}
	s := newTTYStatus(buf)

	s.Start("Fetching lock file")
	time.Sleep(150 * time.Millisecond)
	s.Update("Still fetching")
	time.Sleep(150 * time.Millisecond)
	s.Done("done")
	time.Sleep(100 * time.Millisecond)

	out := buf.String()
	for _, q := range []string{
		"\x1b[?2026$p", // DECRQM synchronized output query
		"\x1b[?2027$p", // DECRQM unicode core query
		"$p",           // any other DECRQM
	} {
		if strings.Contains(out, q) {
			t.Errorf("status output contains terminal query %q; replies to it leak to the user's terminal\noutput: %q", q, out)
		}
	}

	if !strings.Contains(out, "Fetching lock file") {
		t.Errorf("status output missing running message\noutput: %q", out)
	}
	if !strings.Contains(out, "Still fetching") {
		t.Errorf("status output missing updated message\noutput: %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("status output missing final message\noutput: %q", out)
	}
}

// TestStatusTTYFailShowsErrorMessage verifies the failure path renders the
// final message on the TTY path.
func TestStatusTTYFailShowsErrorMessage(t *testing.T) {
	buf := &syncBuffer{}
	s := newTTYStatus(buf)

	s.Start("Working")
	time.Sleep(50 * time.Millisecond)
	s.Fail("it broke")
	time.Sleep(50 * time.Millisecond)

	if !strings.Contains(buf.String(), "it broke") {
		t.Errorf("status output missing failure message\noutput: %q", buf.String())
	}
}

// TestStatusFinishWithoutStartIsNoOp: Done/Fail/Clear on a status that was
// never started must not write anything — escape sequences on a TTY, or
// stray " done"/" failed" lines on a pipe.
func TestStatusFinishWithoutStartIsNoOp(t *testing.T) {
	for _, tty := range []bool{true, false} {
		buf := &syncBuffer{}
		s := NewStatus(buf)
		s.noTTY = !tty
		s.Done("boom")
		s.Fail("boom")
		s.Clear()
		if got := buf.String(); got != "" {
			t.Errorf("tty=%v: finish without Start wrote %q, want nothing", tty, got)
		}
	}
}

// TestStatusDoubleDonePrintsFinalOnce: a duplicated Done must not erase the
// terminal line again or repeat the final message, on either path.
func TestStatusDoubleDonePrintsFinalOnce(t *testing.T) {
	for _, tty := range []bool{true, false} {
		buf := &syncBuffer{}
		s := NewStatus(buf)
		s.noTTY = !tty
		s.Start("Working")
		s.Done("all good")
		s.Done("all good")
		if n := strings.Count(buf.String(), "all good"); n != 1 {
			t.Errorf("tty=%v: final message printed %d times, want 1\noutput: %q", tty, n, buf.String())
		}
	}
}

// TestStatusSilentAfterStartStopsAnimation: enabling silent mode mid-run
// must not leak the animation goroutine — Done must still stop repaints.
func TestStatusSilentAfterStartStopsAnimation(t *testing.T) {
	buf := &syncBuffer{}
	s := newTTYStatus(buf)
	s.Start("Working")
	s.SetSilent(true)
	s.Done("")
	settled := len(buf.String())
	time.Sleep(300 * time.Millisecond)
	if got := len(buf.String()); got != settled {
		t.Errorf("output grew from %d to %d bytes after Done — animation goroutine still running", settled, got)
	}
}

// TestStatusTruncatesToTerminalWidth: the status line must stay on one row,
// or in-place erasing leaves wrapped residue on narrow terminals.
func TestStatusTruncatesToTerminalWidth(t *testing.T) {
	buf := &syncBuffer{}
	s := newTTYStatus(buf)
	s.termWidth = func() int { return 20 }

	s.Start("this message is much longer than twenty columns")
	s.Done("")

	for seg := range strings.SplitSeq(buf.String(), "\r") {
		if w := visibleWidth(seg); w > 20 {
			t.Errorf("rendered segment %q is %d cells wide, want <= 20", seg, w)
		}
	}
}

// TestRunStatusNonTTY covers the RunStatus wrapper's non-TTY branch. The
// TTY branch delegates to Status, whose query-leak regression is pinned by
// TestStatusTTYWritesNoTerminalQueries.
func TestRunStatusNonTTY(t *testing.T) {
	var buf bytes.Buffer
	got, err := RunStatus(&buf, "Working", func() (int, error) { return 42, nil })
	if err != nil || got != 42 {
		t.Fatalf("RunStatus = %v, %v; want 42, nil", got, err)
	}
	if !strings.Contains(buf.String(), "Working") || !strings.Contains(buf.String(), "done") {
		t.Errorf("RunStatus non-TTY output = %q, want message and done", buf.String())
	}

	buf.Reset()
	_, err = RunStatus(&buf, "Working", func() (int, error) { return 0, errors.New("boom") })
	if err == nil {
		t.Fatal("RunStatus should propagate fn error")
	}
	if !strings.Contains(buf.String(), "failed") {
		t.Errorf("RunStatus non-TTY failure output = %q, want 'failed'", buf.String())
	}
}
