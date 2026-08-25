package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/ui"
)

// Terminal responses to the OSC 11 (background color) and DA1 (device
// attributes) queries that lipgloss sends when resolving theme colors. When
// the terminal answers after lipgloss's query timeout, these bytes are left
// unread in the tty input queue and get prepended to the user's typed
// response. Captured from a real reproduction of SK-762.
const strayTerminalResponses = "\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\\x1b[?65;4;6;18;22c" +
	"\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\\x1b[?65;4;6;18;22c"

func discardOutput() *ui.Output {
	return ui.NewOutput(&bytes.Buffer{}, &bytes.Buffer{})
}

func TestConfirmUninstall(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain yes", "y\n", true},
		{"plain yes word", "yes\n", true},
		{"plain no", "n\n", false},
		{"empty defaults to no", "\n", false},
		{"yes with stray terminal responses", strayTerminalResponses + "y\n", true},
		{"no with stray terminal responses", strayTerminalResponses + "n\n", false},
		{"only stray responses defaults to no", strayTerminalResponses + "\n", false},
		{"yes with bare control residue from torn reply", "\x07y\n", true},
		{"yes without trailing newline", "y", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := confirmUninstall(discardOutput(), strings.NewReader(tt.input))
			if got != tt.want {
				t.Errorf("confirmUninstall(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestConfirmSelfUninstall(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain yes", "y\n", true},
		{"yes with stray terminal responses", strayTerminalResponses + "y\n", true},
		{"only stray responses defaults to no", strayTerminalResponses + "\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := confirmSelfUninstall(discardOutput(), strings.NewReader(tt.input))
			if got != tt.want {
				t.Errorf("confirmSelfUninstall(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestStdPrompterSequentialPromptsShareBufferedInput(t *testing.T) {
	p := NewStdPrompter(strings.NewReader("Alice\ny\n"), &bytes.Buffer{})
	name, err := p.Prompt("Name? ")
	if err != nil || name != "Alice" {
		t.Fatalf("first Prompt = %q, %v; want \"Alice\", nil", name, err)
	}
	ok, err := p.Confirm("Proceed?")
	if err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}
	if !ok {
		t.Errorf("Confirm after Prompt = false, want true (second line lost to a discarded buffer)")
	}
}

func TestStdPrompterConfirmWithStrayTerminalResponses(t *testing.T) {
	p := NewStdPrompter(strings.NewReader(strayTerminalResponses+"y\n"), &bytes.Buffer{})
	got, err := p.Confirm("Proceed?")
	if err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}
	if !got {
		t.Errorf("Confirm with stray terminal responses before 'y' = false, want true")
	}
}
