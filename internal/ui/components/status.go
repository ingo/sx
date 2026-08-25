package components

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/sleuth-io/sx/v2/internal/ui"
	"github.com/sleuth-io/sx/v2/internal/ui/theme"
)

// statusFrames are the spinner animation frames (same glyphs as the
// charmbracelet Dot spinner previously used here).
var statusFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

const statusFrameInterval = 100 * time.Millisecond

// Status provides a transient status line that updates in place.
// Use for operations where you want to show progress without cluttering output.
//
// Rendered with plain ANSI writes rather than a bubbletea program: bubbletea
// queries the terminal for capabilities at startup, and for a short-lived
// status the replies arrive after the program has exited and restored echo,
// so they end up printed to the user's terminal as garbage (SK-763).
type Status struct {
	out    io.Writer
	noTTY  bool
	mu     sync.Mutex
	silent bool

	// termWidth reports the terminal width in cells (0 = unknown).
	// Overridable in tests.
	termWidth func() int

	message string
	frame   int
	started bool
	running bool
	stop    chan struct{}
	stopped chan struct{}
}

// NewStatus creates a new status line.
func NewStatus(out io.Writer) *Status {
	return &Status{
		out:       out,
		noTTY:     !ui.IsTTY(out),
		termWidth: func() int { return terminalWidth(out) },
	}
}

// terminalWidth returns the terminal width for out, or 0 if unknown.
func terminalWidth(out io.Writer) int {
	f, ok := out.(*os.File)
	if !ok {
		return 0
	}
	w, _, err := term.GetSize(int(f.Fd()))
	if err != nil || w <= 0 {
		return 0
	}
	return w
}

// SetSilent enables silent mode (no output).
func (s *Status) SetSilent(silent bool) {
	s.silent = silent
}

// render writes the current spinner frame and message in place.
// Caller must hold s.mu.
func (s *Status) render() {
	styles := theme.Current().Styles()
	frame := styles.Spinner.Render(statusFrames[s.frame%len(statusFrames)])
	line := frame + " " + styles.Muted.Render(s.message)
	// Keep the line to a single row: in-place erasing (\r + EL) only
	// covers the current row, so a wrapped line would leave residue.
	if w := s.termWidth(); w > 0 {
		line = ansi.Truncate(line, w, "…")
	}
	fmt.Fprintf(s.out, "\r\x1b[K%s", line)
}

// clearLine erases the in-place status line. Caller must hold s.mu.
func (s *Status) clearLine() {
	fmt.Fprint(s.out, "\r\x1b[K")
}

// Start begins showing a status with spinner.
func (s *Status) Start(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent {
		return
	}

	s.message = message

	if s.noTTY {
		fmt.Fprintf(s.out, "%s...", message)
		return
	}

	if s.running {
		s.render()
		return
	}

	s.started = true
	s.running = true
	s.frame = 0
	s.stop = make(chan struct{})
	s.stopped = make(chan struct{})
	// The cursor is deliberately left visible: hiding it would leave the
	// user's terminal cursorless if the process dies mid-spin (Ctrl-C,
	// panic), since nothing here owns signal handling.
	s.render()

	go func(stop, stopped chan struct{}) {
		defer close(stopped)
		ticker := time.NewTicker(statusFrameInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.mu.Lock()
				if s.running {
					s.frame++
					s.render()
				}
				s.mu.Unlock()
			}
		}
	}(s.stop, s.stopped)
}

// Update changes the status message.
func (s *Status) Update(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent {
		return
	}

	s.message = message

	if s.noTTY {
		fmt.Fprintf(s.out, " %s...", message)
		return
	}

	if s.running {
		s.render()
	}
}

// finish stops the animation and replaces the status line with an optional
// final message.
func (s *Status) finish(success bool, finalMessage string) {
	s.mu.Lock()

	if s.silent {
		s.mu.Unlock()
		return
	}

	if s.noTTY {
		switch {
		case finalMessage != "":
			fmt.Fprintf(s.out, " %s\n", finalMessage)
		case success:
			fmt.Fprintln(s.out, " done")
		default:
			fmt.Fprintln(s.out, " failed")
		}
		s.mu.Unlock()
		return
	}

	// Only touch the terminal when a spinner is actually active: a stray
	// Done/Fail without Start (or a duplicate) must not erase whatever is
	// on the line now or repeat the final message.
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false

	var stopped chan struct{}
	if s.running {
		s.running = false
		close(s.stop)
		stopped = s.stopped
	}
	s.mu.Unlock()

	// Wait outside the lock so the animation goroutine can exit.
	if stopped != nil {
		<-stopped
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearLine()
	if finalMessage != "" {
		styles := theme.Current().Styles()
		sym := theme.Current().Symbols()
		if success {
			fmt.Fprintln(s.out, styles.Success.Render(sym.Success+" "+finalMessage))
		} else {
			fmt.Fprintln(s.out, styles.Error.Render(sym.Error+" "+finalMessage))
		}
	}
}

// Done completes the status with an optional final message.
// If finalMessage is empty, the status line is cleared.
func (s *Status) Done(finalMessage string) {
	s.finish(true, finalMessage)
}

// Fail completes the status with an error message.
func (s *Status) Fail(message string) {
	s.finish(false, message)
}

// Clear clears the status line without showing a final message.
func (s *Status) Clear() {
	s.Done("")
}

// RunStatus runs a function while showing a status spinner.
// The status line is cleared when the function completes.
func RunStatus[T any](out io.Writer, message string, fn func() (T, error)) (T, error) {
	noTTY := !ui.IsTTY(out)

	if noTTY {
		fmt.Fprintf(out, "%s... ", message)
		result, err := fn()
		if err != nil {
			fmt.Fprintln(out, "failed")
		} else {
			fmt.Fprintln(out, "done")
		}
		return result, err
	}

	s := NewStatus(out)
	s.Start(message)
	result, err := fn()
	if err != nil {
		s.Fail("")
	} else {
		s.Done("")
	}
	return result, err
}

// StatusLine is a simpler non-animated status that updates in place.
// Better for rapid updates where animation would be distracting.
type StatusLine struct {
	out     io.Writer
	noTTY   bool
	silent  bool
	lastLen int
	mu      sync.Mutex
}

// NewStatusLine creates a simple status line that updates in place.
func NewStatusLine(out io.Writer) *StatusLine {
	return &StatusLine{
		out:   out,
		noTTY: !ui.IsTTY(out),
	}
}

// SetSilent enables silent mode.
func (s *StatusLine) SetSilent(silent bool) {
	s.silent = silent
}

// Set updates the status line text.
func (s *StatusLine) Set(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent {
		return
	}

	if s.noTTY {
		fmt.Fprintln(s.out, message)
		return
	}

	// Clear previous line and write new
	clear := strings.Repeat(" ", s.lastLen)
	fmt.Fprintf(s.out, "\r%s\r%s", clear, message)
	s.lastLen = len(message)
}

// Clear clears the status line.
func (s *StatusLine) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.silent || s.noTTY {
		return
	}

	clear := strings.Repeat(" ", s.lastLen)
	fmt.Fprintf(s.out, "\r%s\r", clear)
	s.lastLen = 0
}

// Done clears the line and optionally prints a final message on a new line.
func (s *StatusLine) Done(message string) {
	s.Clear()
	if message != "" && !s.silent {
		fmt.Fprintln(s.out, message)
	}
}
