package components

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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

// TestStatusTTYWritesNoTerminalQueries pins SK-763: the status line must not
// send terminal capability queries (DECRQM mode 2026/2027, etc.). Replies to
// those queries arrive after the short-lived status finishes and get echoed
// to the user's terminal as garbage like `^[[?2026;2$y`.
func TestStatusTTYWritesNoTerminalQueries(t *testing.T) {
	// Force the query-friendly environment bubbletea checks for, so the old
	// implementation fails deterministically regardless of the test host.
	os.Unsetenv("TERM_PROGRAM")
	os.Unsetenv("SSH_TTY")

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

// TestRunStatusTTYWritesNoTerminalQueries covers the RunStatus wrapper on
// the TTY path via the same leak signature.
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
