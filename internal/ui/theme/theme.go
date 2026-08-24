// Package theme provides theming support for the Skills CLI UI.
package theme

import (
	"image/color"
	"os"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/mattn/go-isatty"
)

// AdaptiveColor defines a color with variants for light and dark terminal
// backgrounds. Lipgloss v2 dropped its own AdaptiveColor, so resolution
// against the detected background happens here.
type AdaptiveColor struct {
	Light string
	Dark  string
}

// Color resolves the variant matching the detected terminal background,
// downsampled to the terminal's color profile.
func (c AdaptiveColor) Color() color.Color {
	hex := c.Light
	if IsDarkBackground() {
		hex = c.Dark
	}
	return Profile().Convert(lipgloss.Color(hex))
}

var (
	detectOnce sync.Once
	darkBG     bool
	profile    colorprofile.Profile
)

func detect() {
	detectOnce.Do(func() {
		darkBG = true
		profile = colorprofile.Detect(os.Stdout, os.Environ())
		// Matches ui.IsTTY, which theme can't import without a cycle.
		isTTY := func(f *os.File) bool {
			return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
		}
		// Querying the background color does terminal I/O (OSC 11) and puts
		// stdin into raw mode, so only do it when the answer can matter
		// (colors enabled, both ends TTYs) and when it is safe: a background
		// job touching the terminal would be suspended by SIGTTOU.
		if profile >= colorprofile.ANSI &&
			isTTY(os.Stdin) && isTTY(os.Stdout) &&
			isForeground() {
			darkBG = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
		}
	})
}

// IsDarkBackground reports whether the terminal has a dark background.
// Defaults to dark when detection is not possible (non-TTY).
func IsDarkBackground() bool {
	detect()
	return darkBG
}

// Profile returns the detected terminal color profile.
func Profile() colorprofile.Profile {
	detect()
	return profile
}

// ColorPalette defines the colors used by a theme.
type ColorPalette struct {
	// Primary accent color (e.g., cyan for Claude Code)
	Primary AdaptiveColor
	// Secondary accent color (e.g., blue)
	Secondary AdaptiveColor

	// Status colors
	Success AdaptiveColor
	Error   AdaptiveColor
	Warning AdaptiveColor
	Info    AdaptiveColor

	// Text colors
	Text         AdaptiveColor
	TextMuted    AdaptiveColor
	TextFaint    AdaptiveColor
	TextEmphasis AdaptiveColor

	// UI element colors
	Border    AdaptiveColor
	Highlight AdaptiveColor
}

// Symbols defines the glyphs used for various states.
type Symbols struct {
	Success    string
	Error      string
	Warning    string
	Info       string
	Arrow      string
	Bullet     string
	Pending    string
	InProgress string
}

// Styles contains pre-composed lipgloss styles.
type Styles struct {
	// Message styles
	Success lipgloss.Style
	Error   lipgloss.Style
	Warning lipgloss.Style
	Info    lipgloss.Style

	// Layout styles
	Header    lipgloss.Style
	SubHeader lipgloss.Style

	// Text styles
	Bold     lipgloss.Style
	Muted    lipgloss.Style
	Faint    lipgloss.Style
	Emphasis lipgloss.Style

	// List styles
	ListItem   lipgloss.Style
	ListBullet lipgloss.Style
	Selected   lipgloss.Style
	Cursor     lipgloss.Style

	// Key-Value styles
	Key       lipgloss.Style
	Value     lipgloss.Style
	Separator lipgloss.Style

	// Progress/status styles
	Spinner  lipgloss.Style
	Progress lipgloss.Style
}

// Theme defines the visual styling for the CLI.
type Theme interface {
	// Name returns the theme identifier
	Name() string
	// Palette returns the color palette
	Palette() ColorPalette
	// Styles returns pre-composed lipgloss styles
	Styles() Styles
	// Symbols returns the glyphs used for various states
	Symbols() Symbols
}

var (
	currentTheme Theme
	themeMu      sync.RWMutex
)

func init() {
	// Set default theme
	currentTheme = NewClaudeCodeTheme()
}

// Current returns the active theme (thread-safe).
func Current() Theme {
	themeMu.RLock()
	defer themeMu.RUnlock()
	return currentTheme
}

// Set sets the active theme (thread-safe).
func Set(t Theme) {
	themeMu.Lock()
	defer themeMu.Unlock()
	currentTheme = t
}
