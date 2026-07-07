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

	// backupSrcName and backupDstName cache the last-selected From/To
	// locations on the Sync screen so they're still populated if the user
	// navigates away and back.
	backupSrcName string
	backupDstName string
}

// boundedWidthLayout caps the reported minimum width of its content to
// maxWidth. Fyne sets a window's minimum size from its content's minimum size,
// so any single wide child (a long path label, a wide entry, long form hint
// text) would otherwise force the window wider than windowSize - which on
// multi-monitor setups stretches it across displays. Capping the min width
// here fixes that once for every screen instead of per-widget. The child is
// always laid out at the container's full width, so fillable widgets (entries,
// forms) simply fill it; text that overflows is the child's concern (set
// Truncation on labels that hold long paths).
type boundedWidthLayout struct{ maxWidth float32 }

func (l *boundedWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var min fyne.Size
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		min = min.Max(o.MinSize())
	}
	if min.Width > l.maxWidth {
		min.Width = l.maxWidth
	}
	return min
}

func (l *boundedWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	// Cap the content's actual width at maxWidth and center it. Fyne's glfw
	// driver can hand us a size far wider than windowSize (the multi-monitor
	// stretch described on windowSize); without this cap, fillable widgets
	// like text entries would expand to that full width and look absurdly
	// wide. Clamping here keeps every screen's content at most windowSize wide
	// regardless of the window the driver actually gives us.
	w := size.Width
	if w > l.maxWidth {
		w = l.maxWidth
	}
	x := (size.Width - w) / 2
	for _, o := range objects {
		o.Resize(fyne.NewSize(w, size.Height))
		o.Move(fyne.NewPos(x, 0))
	}
}

// setContent replaces the window's content and re-asserts windowSize
// immediately after. Screens must call this instead of s.win.SetContent
// directly - see the comment on windowSize for why. Content is wrapped in a
// boundedWidthLayout so no screen can stretch the window past windowSize.
func (s *state) setContent(content fyne.CanvasObject) {
	bounded := container.New(&boundedWidthLayout{maxWidth: windowSize.Width}, content)
	s.win.SetContent(bounded)
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

	backupBtn := widget.NewButton("Sync", func() { showBackup(s) })
	downloadBtn := widget.NewButton("Download", func() { showDownload(s) })
	recordersBtn := widget.NewButton("Recorders", func() { showRecorders(s) })
	recordersBtn.Importance = widget.HighImportance
	locationsBtn := widget.NewButton("Manage Sync Locations", func() { showLocations(s) })

	if len(s.cfg.Locations) < 2 {
		backupBtn.Disable()
	}
	if len(s.cfg.Locations) < 1 {
		downloadBtn.Disable()
	}

	body := container.NewVBox(
		title,
		widget.NewSeparator(),
		recordersBtn,
		backupBtn,
		downloadBtn,
		locationsBtn,
	)
	s.setContent(container.NewPadded(container.NewVBox(widget.NewLabel(""), body)))
}
