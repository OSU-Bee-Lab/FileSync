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

// state is threaded through every screen: the window to draw into and the
// currently loaded/persisted app config (locations, defaults).
type state struct {
	win fyne.Window
	cfg appconfig.Config
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
	w.Resize(fyne.NewSize(920, 640))
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
	s.win.SetContent(container.NewPadded(container.NewVBox(widget.NewLabel(""), body)))
}
