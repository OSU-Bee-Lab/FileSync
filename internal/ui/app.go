// Package ui is the Fyne presentation layer for ExpSync. It never imports
// rclone directly - it only calls internal/syncengine's exported API - so
// the rclone dependency stays confined to one package.
package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/appconfig"
)

// windowSize is the one fixed size ExpSync's single window should ever have.
// Fyne's glfw driver (at least on macOS with multiple displays attached) can
// recompute the window to span the whole virtual desktop instead of the
// requested size - this has been observed both on first show and after
// later content swaps (screen changes, dialogs). Rather than guard against
// that in each spot it can happen, every screen must route content changes
// through state.setContent, which re-asserts this size every time. Any
// future additional windows should follow the same pattern (set content,
// then Resize to a fixed size) instead of relying on Fyne's auto-fit.
var windowSize = fyne.NewSize(920, 640)

// state is threaded through every screen: the window to draw into and the
// currently loaded/persisted app config (locations, defaults).
type state struct {
	win fyne.Window
	cfg appconfig.Config
}

// setContent replaces the window's content and re-asserts windowSize
// immediately after. Screens must call this instead of s.win.SetContent
// directly - see the comment on windowSize for why.
func (s *state) setContent(content fyne.CanvasObject) {
	s.win.SetContent(content)
	s.win.Resize(windowSize)
}

func (s *state) saveConfig() {
	if err := appconfig.Save(s.cfg); err != nil {
		dialog.ShowError(err, s.win)
	}
}

// Run builds and shows the ExpSync window. Blocks until the window closes.
func Run() {
	a := app.NewWithID("com.osubeelab.expsync")
	w := a.NewWindow("ExpSync")

	cfg, err := appconfig.Load()
	s := &state{win: w, cfg: cfg}
	if err != nil {
		// Not fatal - fall back to defaults and let the user fix it by
		// re-saving from the Locations screen.
		s.cfg = appconfig.Default()
	}

	// Content must be set before Resize/CenterOnScreen - otherwise Fyne has
	// no size hints yet and (at least on macOS with multiple displays) can
	// compute a window spanning the whole virtual desktop instead of the
	// requested size.
	showHome(s)
	w.SetFixedSize(false)
	w.Resize(windowSize)
	w.CenterOnScreen()
	w.ShowAndRun()
}

func showHome(s *state) {
	title := widget.NewLabelWithStyle("ExpSync", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	subtitle := widget.NewLabel("Schema-scoped rclone sync for bioacoustics experiment data")

	backupBtn := widget.NewButton("Backup / Sync", func() { showBackup(s) })
	backupBtn.Importance = widget.HighImportance
	downloadBtn := widget.NewButton("Download", func() { showDownload(s) })
	locationsBtn := widget.NewButton("Manage Locations", func() { showLocations(s) })

	if len(s.cfg.Locations) < 2 {
		backupBtn.Disable()
	}
	if len(s.cfg.Locations) < 1 {
		downloadBtn.Disable()
	}

	body := container.NewVBox(
		title,
		subtitle,
		widget.NewSeparator(),
		backupBtn,
		downloadBtn,
		locationsBtn,
	)
	s.setContent(container.NewPadded(container.NewVBox(widget.NewLabel(""), body)))
}
