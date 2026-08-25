package components

import (
	"strings"
	"testing"
	"time"
)

// TestSpinnerTTYWritesNoTerminalQueries pins SK-763 for the Spinner
// component: like Status, it must not send terminal capability queries whose
// replies outlive a short-lived spinner and get echoed as garbage.
func TestSpinnerTTYWritesNoTerminalQueries(t *testing.T) {
	buf := &syncBuffer{}
	s := NewSpinnerWithOutput("Checking authentication", buf)
	s.status.noTTY = false

	s.Start()
	time.Sleep(150 * time.Millisecond)
	s.UpdateMessage("Still checking")
	time.Sleep(150 * time.Millisecond)
	s.Stop()
	time.Sleep(100 * time.Millisecond)

	out := buf.String()
	if strings.Contains(out, "$p") {
		t.Errorf("spinner output contains a terminal query; replies leak to the user's terminal\noutput: %q", out)
	}
	if !strings.Contains(out, "Checking authentication") {
		t.Errorf("spinner output missing message\noutput: %q", out)
	}
	if !strings.Contains(out, "Still checking") {
		t.Errorf("spinner output missing updated message\noutput: %q", out)
	}
}
