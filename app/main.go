package main

import (
	"embed"
	"log"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	_ "github.com/sleuth-io/sx/v2/internal/clients/claude_code"    // Register Claude Code client
	_ "github.com/sleuth-io/sx/v2/internal/clients/cline"          // Register Cline client
	_ "github.com/sleuth-io/sx/v2/internal/clients/codex"          // Register Codex client
	_ "github.com/sleuth-io/sx/v2/internal/clients/cursor"         // Register Cursor client
	_ "github.com/sleuth-io/sx/v2/internal/clients/gemini"         // Register Gemini Code Assist client
	_ "github.com/sleuth-io/sx/v2/internal/clients/github_copilot" // Register GitHub Copilot client
	_ "github.com/sleuth-io/sx/v2/internal/clients/kiro"           // Register Kiro client
	_ "github.com/sleuth-io/sx/v2/internal/clients/openclaw"       // Register OpenClaw client
	_ "github.com/sleuth-io/sx/v2/internal/clients/opencode"       // Register OpenCode client
	"github.com/sleuth-io/sx/v2/internal/config"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	// Native menu. macOS gets the standard app menu (with Settings… living
	// there per platform convention), an Edit menu so clipboard shortcuts
	// work in the webview, and the standard Window menu; other platforms
	// fold Settings/Quit into File. File's New items and Help are the same
	// everywhere — menu clicks become frontend events, since the frontend
	// owns the create flows.
	emit := func(event string) func(*menu.CallbackData) {
		return func(*menu.CallbackData) { app.emitMenuEvent(event) }
	}
	appMenu := menu.NewMenu()
	if goruntime.GOOS == "darwin" {
		axisMenu := appMenu.AddSubmenu("axis")
		axisMenu.AddText("About axis", nil, func(*menu.CallbackData) {
			app.ShowAbout()
		})
		axisMenu.AddText("Check for Updates…", nil, func(*menu.CallbackData) {
			app.CheckForUpdatesInteractively()
		})
		axisMenu.AddSeparator()
		axisMenu.AddText("Settings…", keys.CmdOrCtrl(","), func(*menu.CallbackData) {
			app.OpenSettings()
		})
		axisMenu.AddSeparator()
		axisMenu.AddText("Hide axis", keys.CmdOrCtrl("h"), func(*menu.CallbackData) {
			app.HideApp()
		})
		axisMenu.AddText("Hide Others", keys.Combo("h", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(*menu.CallbackData) {
			hideOtherApplications()
		})
		axisMenu.AddText("Show All", nil, func(*menu.CallbackData) {
			unhideAllApplications()
		})
		axisMenu.AddSeparator()
		axisMenu.AddText("Quit axis", keys.CmdOrCtrl("q"), func(*menu.CallbackData) {
			app.Quit()
		})
	}

	fileMenu := appMenu.AddSubmenu("File")
	fileMenu.AddText("New Skill…", keys.CmdOrCtrl("n"), emit("new-skill"))
	fileMenu.AddText("New Collection…", keys.Combo("n", keys.CmdOrCtrlKey, keys.ShiftKey), emit("new-collection"))
	fileMenu.AddText("New Library…", nil, emit("new-library"))
	if goruntime.GOOS != "darwin" {
		fileMenu.AddSeparator()
		fileMenu.AddText("Settings…", keys.CmdOrCtrl(","), func(*menu.CallbackData) {
			app.OpenSettings()
		})
		fileMenu.AddSeparator()
		fileMenu.AddText("Quit", keys.CmdOrCtrl("q"), func(*menu.CallbackData) {
			app.Quit()
		})
	}

	// Edit precedes View per the platform-standard macOS menu order
	// (App, File, Edit, View, Window, Help).
	if goruntime.GOOS == "darwin" {
		appMenu.Append(menu.EditMenu())
	}

	// View holds the command palette so the menu bar advertises the ⌘K
	// accelerator — the platform-native way users discover shortcuts.
	// The frontend also handles the raw keydown; its toggle is debounced
	// so double delivery (native accelerator + webview key) is one toggle.
	viewMenu := appMenu.AddSubmenu("View")
	viewMenu.AddText("Command Palette…", keys.CmdOrCtrl("k"), emit("command-palette"))

	if goruntime.GOOS == "darwin" {
		appMenu.Append(menu.WindowMenu())
	}

	helpMenu := appMenu.AddSubmenu("Help")
	helpMenu.AddText("axis Documentation", nil, func(*menu.CallbackData) {
		_ = config.OpenBrowser("https://github.com/ingo/sx#readme")
	})
	// On macOS these live in the app menu per platform convention; the
	// Windows/Linux home for both is Help.
	if goruntime.GOOS != "darwin" {
		helpMenu.AddSeparator()
		helpMenu.AddText("Check for Updates…", nil, func(*menu.CallbackData) {
			app.CheckForUpdatesInteractively()
		})
		helpMenu.AddText("About axis", nil, func(*menu.CallbackData) {
			app.ShowAbout()
		})
	}

	err := wails.Run(&options.App{
		Title:     "axis",
		Width:     1200,
		Height:    800,
		MinWidth:  880,
		MinHeight: 560,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 250, G: 250, B: 249, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Menu:             appMenu,
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
		},
		Bind: []any{
			app,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			About:                &mac.AboutInfo{Title: "axis", Message: "Your team's library for AI assets"},
			WebviewIsTransparent: false,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
