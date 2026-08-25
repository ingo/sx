package commands

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/sleuth-io/sx/v2/internal/ui/theme"
)

// Prompter provides an interface for interactive prompts
// This abstraction allows for:
// 1. Easy testing via mocking
// 2. Future UI improvements (e.g., switching to a TUI library) without changing all call sites
type Prompter interface {
	Prompt(message string) (string, error)
	PromptWithDefault(message, defaultValue string) (string, error)
	Confirm(message string) (bool, error)
}

// StdPrompter implements Prompter using standard I/O
type StdPrompter struct {
	in  io.Reader
	out io.Writer
}

// NewStdPrompter creates a new standard I/O prompter
func NewStdPrompter(in io.Reader, out io.Writer) *StdPrompter {
	return &StdPrompter{
		// Wrap once so buffered lookahead survives across sequential
		// prompts instead of being discarded with a per-call reader.
		in:  bufio.NewReader(in),
		out: out,
	}
}

// readPromptLine reads one line of user input. Terminal replies to queries
// (OSC 11 background color, DA1) that arrive after lipgloss's query timeout
// are left unread in the tty input queue and end up prepended to the typed
// line, so strip escape sequences before interpreting it (SK-762). Bare
// control bytes (e.g. a BEL terminator severed from its sequence when the
// query timeout tears a reply in two) survive ansi.Strip, so drop those too.
func readPromptLine(in io.Reader) (string, error) {
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	response, err := reader.ReadString('\n')
	// EOF with a partial line still carries the user's answer (e.g.
	// `printf 'y' | sx uninstall`); any other error fails closed even if
	// bytes were read — a torn tty read must not confirm anything.
	if err != nil && (!errors.Is(err, io.EOF) || response == "") {
		return "", err
	}
	return strings.TrimSpace(ansi.Strip(response)), nil
}

// confirmAnswer reduces a prompt line to the ASCII answer a y/N prompt
// expects. Escape-sequence residue that ansi.Strip cannot attribute (bare
// C0 terminators, 8-bit C1 introducers from torn terminal replies) is not
// valid UTF-8 text, so anything outside printable ASCII is dropped. Only
// for yes/no answers — free-text responses must round-trip verbatim.
func confirmAnswer(line string) string {
	var b strings.Builder
	for i := range len(line) {
		if line[i] >= 0x20 && line[i] <= 0x7e {
			b.WriteByte(line[i])
		}
	}
	return strings.ToLower(strings.TrimSpace(b.String()))
}

// Prompt displays a prompt and reads user input
func (p *StdPrompter) Prompt(message string) (string, error) {
	fmt.Fprint(p.out, message)
	return readPromptLine(p.in)
}

// PromptWithDefault displays a prompt with a default value
func (p *StdPrompter) PromptWithDefault(message, defaultValue string) (string, error) {
	prompt := fmt.Sprintf("%s [%s]: ", message, defaultValue)
	response, err := p.Prompt(prompt)
	if err != nil {
		return "", err
	}
	if response == "" {
		return defaultValue, nil
	}
	return response, nil
}

// Confirm asks a yes/no question
func (p *StdPrompter) Confirm(message string) (bool, error) {
	styled := theme.Current().Styles().Info.Render(message)
	response, err := p.Prompt(styled + " (Y/n): ")
	if err != nil {
		return false, err
	}
	response = confirmAnswer(response)
	return response == "" || response == "y" || response == "yes", nil
}
