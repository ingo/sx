package components

import (
	"io"
	"os"
)

// Spinner displays an animated spinner with a message until stopped.
//
// Built on Status (plain in-place ANSI) rather than a bubbletea program:
// bubbletea queries terminal capabilities at startup, and a short-lived
// spinner exits before the terminal replies, leaking the reply bytes to the
// user's terminal (SK-763). Delegating also removes the old implementation's
// unsynchronized result read when the program quit while the wrapped
// function was still running.
type Spinner struct {
	status  *Status
	message string
}

// NewSpinner creates a new spinner with the given message.
func NewSpinner(message string) *Spinner {
	return NewSpinnerWithOutput(message, os.Stdout)
}

// NewSpinnerWithOutput creates a new spinner with custom output.
func NewSpinnerWithOutput(message string, out io.Writer) *Spinner {
	return &Spinner{
		status:  NewStatus(out),
		message: message,
	}
}

// Start begins the spinner animation.
func (s *Spinner) Start() {
	s.status.Start(s.message)
}

// Stop stops the spinner and clears the line.
func (s *Spinner) Stop() {
	s.status.Clear()
}

// StopWithError stops the spinner and clears the line. The error is the
// caller's to report; the spinner line itself carries no final message.
func (s *Spinner) StopWithError(_ error) {
	s.status.Fail("")
}

// UpdateMessage updates the spinner message (for long-running operations).
func (s *Spinner) UpdateMessage(message string) {
	s.message = message
	s.status.Update(message)
}

// RunWithSpinner runs a function while showing a spinner.
// Returns the function's result and any error.
func RunWithSpinner[T any](message string, fn func() (T, error)) (T, error) {
	return RunWithSpinnerOutput(message, os.Stdout, fn)
}

// RunWithSpinnerOutput runs a function while showing a spinner on custom
// output. The spinner is torn down even if fn panics.
func RunWithSpinnerOutput[T any](message string, out io.Writer, fn func() (T, error)) (result T, err error) {
	s := NewStatus(out)
	s.Start(message)
	defer func() {
		if err != nil {
			s.Fail("")
		} else {
			s.Done("")
		}
	}()
	result, err = fn()
	return result, err
}
