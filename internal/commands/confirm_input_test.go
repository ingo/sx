package commands

import (
	"bytes"
	"errors"
	"path/filepath"
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
		// A bare 8-bit OSC introducer (\x9d) is not tested as recoverable:
		// ansi.Strip correctly parses everything after it as escape-sequence
		// payload, so the answer is formally ambiguous and the prompt fails
		// closed to No — the safe default for a destructive operation.
		{"8-bit C1 introducer swallows the line, defaults to no", "\x9dy\n", false},
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

// errAfterReader yields its data, then fails with a non-EOF error.
type errAfterReader struct {
	data string
	done bool
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("tty read failed")
	}
	r.done = true
	return copy(p, r.data), errors.New("tty read failed")
}

// TestConfirmUninstallFailsClosedOnReadError: a partial answer followed by a
// real read error (not EOF) must not confirm a destructive operation.
func TestConfirmUninstallFailsClosedOnReadError(t *testing.T) {
	if confirmUninstall(discardOutput(), &errAfterReader{data: "y"}) {
		t.Error("confirmUninstall = true on partial read with non-EOF error, want false (fail closed)")
	}
}

// TestStdPrompterPromptPreservesNonUTF8Bytes: free-text prompt responses
// must round-trip verbatim (minus escape sequences), not be rewritten to
// U+FFFD replacement runes.
func TestStdPrompterPromptPreservesNonUTF8Bytes(t *testing.T) {
	p := NewStdPrompter(strings.NewReader("caf\xe9\n"), &bytes.Buffer{})
	got, err := p.Prompt("Name? ")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if got != "caf\xe9" {
		t.Errorf("Prompt = %q, want %q (latin-1 byte mangled)", got, "caf\xe9")
	}
}

// setupUninstallSandbox isolates HOME/config/cache so the uninstall command
// finds no config, which with --all routes through handleAllFlagWithoutAssets
// and its confirmation prompt.
func setupUninstallSandbox(t *testing.T) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(homeDir, ".cache"))
	t.Setenv("SX_CONFIG_DIR", filepath.Join(homeDir, ".config", "sx"))
	t.Setenv("SX_CACHE_DIR", filepath.Join(homeDir, ".cache", "sx"))
}

// TestUninstallCommandReadsConfirmationFromCommandInput pins the cobra
// wiring: the confirmation must read from cmd.SetIn (not os.Stdin) and
// tolerate stray terminal replies before the typed answer.
func TestUninstallCommandReadsConfirmationFromCommandInput(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantCancelled bool
	}{
		{"stray responses then yes proceeds", strayTerminalResponses + "y\n", false},
		{"stray responses then no cancels", strayTerminalResponses + "n\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupUninstallSandbox(t)

			cmd := NewUninstallCommand()
			var out, errOut bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			cmd.SetIn(strings.NewReader(tt.input))
			cmd.SetArgs([]string{"--all"})

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute returned error: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
			}

			cancelled := strings.Contains(out.String(), "Uninstall cancelled")
			if cancelled != tt.wantCancelled {
				t.Errorf("cancelled = %v, want %v\nstdout: %s", cancelled, tt.wantCancelled, out.String())
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
