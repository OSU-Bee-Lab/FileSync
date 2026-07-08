package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// showSettings is FileSync's app-level preferences screen (as opposed to
// per-Location or per-experiment settings, which live on their own screens).
func showSettings(s *state) {
	debugCheck := widget.NewCheck("Debug mode (verbose console logging of scan/copy progress and rclone)", nil)
	debugCheck.SetChecked(s.cfg.DebugMode)
	debugCheck.OnChanged = func(checked bool) {
		s.cfg.DebugMode = checked
		syncengine.SetDebugLogging(checked)
		s.saveConfig()
	}

	hint := widget.NewLabel("When enabled, FileSync prints what it's scanning/copying to the console " +
		"(stdout/stderr) - useful for diagnosing a sync or scan that seems stuck.")
	hint.Wrapping = fyne.TextWrapWord

	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), widget.NewSeparator()),
		backBtn,
		nil, nil,
		container.NewVBox(debugCheck, hint),
	)
	s.setContent(container.NewPadded(content))
}
