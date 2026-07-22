// Package ui is the Fyne presentation layer for FileSync. It never imports
// rclone directly - it only calls internal/syncengine's exported API - so
// the rclone dependency stays confined to one package.
package ui

import (
	_ "embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/appconfig"
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

//go:embed Icon.png
var logoIconBytes []byte

// windowSize is the one fixed size FileSync's single window should ever have.
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

	// syncExperimentsLocationNames and syncExperimentsExpNames cache the
	// last-selected locations and experiments on the Sync Experiments
	// screen (N-way: two or more locations, no designated source) so
	// they're still populated if the user navigates away and back.
	syncExperimentsLocationNames []string
	syncExperimentsExpNames      []string
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

// growingWidthLayout keeps the same MinSize cap as boundedWidthLayout (so it
// still can't force the window wider than windowSize on its own - the
// multi-monitor stretch bug windowSize documents), but unlike
// boundedWidthLayout it does not clamp the width handed to its child at
// layout time. Use it for screens whose layout should grow to fill however
// wide the user actually resizes the window instead of staying capped at
// windowSize.
type growingWidthLayout struct{ maxWidth float32 }

func (l *growingWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
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

func (l *growingWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Resize(size)
		o.Move(fyne.NewPos(0, 0))
	}
}

// setContentResizable is like setContent, but its content grows to fill
// however wide the user resizes the window rather than staying capped at
// windowSize. Only use it for screens that were built to make good use of
// the extra width (e.g. the sync progress screen's Experiments/Folders/Files
// columns) - anything with fillable widgets not designed for a wide layout
// should keep using setContent.
func (s *state) setContentResizable(content fyne.CanvasObject) {
	bounded := container.New(&growingWidthLayout{maxWidth: windowSize.Width}, content)
	s.win.SetContent(bounded)
	s.win.Resize(windowSize)
}

func (s *state) saveConfig() {
	if err := appconfig.Save(s.cfg); err != nil {
		dialog.ShowError(err, s.win)
	}
}

// Run builds and shows the FileSync window. Blocks until the window closes.
func Run() {
	a := app.NewWithID("com.osubeelab.filesync")
	a.Settings().SetTheme(newLightenedTheme())
	w := a.NewWindow("FileSync")

	startApp := func() {
		cfg, err := appconfig.Load()
		s := &state{win: w, cfg: cfg}
		if err != nil {
			// Not fatal - fall back to defaults and let the user fix it by
			// re-saving from the Locations screen.
			s.cfg = appconfig.Default()
		}
		syncengine.SetDebugLogging(s.cfg.DebugMode)
		syncengine.SetCheckers(s.cfg.Checkers)
		syncengine.SetBwLimitMiBPerSec(s.cfg.BwLimitMiBPerSec)
		syncengine.SetTransfers(s.cfg.Transfers)

		// Content must be set before Resize/CenterOnScreen - otherwise Fyne
		// has no size hints yet and (at least on macOS with multiple
		// displays) can compute a window spanning the whole virtual desktop
		// instead of the requested size.
		showHome(s)
		w.SetFixedSize(false)
		w.Resize(windowSize)
		w.CenterOnScreen()
	}

	// Two instances copying to the same destination via rclone would race
	// each other, so warn before opening a second window rather than risk
	// that silently.
	lock, ok, err := appconfig.AcquireInstanceLock()
	if err == nil && !ok {
		msg := widget.NewLabel("An instance of FileSync is already open. Running multiple instances of FileSync will cause issues if multiple syncs are run simultaneously. Running another instance is not recommended.")
		msg.Wrapping = fyne.TextWrapWord

		closeBtn := widget.NewButton("Exit", func() { w.Close() })
		closeBtn.Importance = widget.LowImportance

		continueBtn := widget.NewButton("Continue", func() {
			startApp()
		})
		continueBtn.Importance = widget.DangerImportance

		content := container.NewVBox(
			msg,
			container.NewHBox(layout.NewSpacer(), closeBtn, continueBtn, layout.NewSpacer()),
		)
		w.Resize(fyne.NewSize(420, 200))
		w.SetContent(content)
		w.CenterOnScreen()
		w.ShowAndRun()
		return
	}
	if lock != nil {
		defer lock.Release()
	}

	startApp()
	w.ShowAndRun()
}

func showHome(s *state) {
	logo := canvas.NewImageFromResource(fyne.NewStaticResource("Icon.png", logoIconBytes))
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(120, 120))

	titleText := canvas.NewText("FileSync", theme.Color(theme.ColorNameForeground))
	titleText.TextStyle = fyne.TextStyle{Bold: true}
	titleText.TextSize = 24
	titleText.Alignment = fyne.TextAlignCenter

	title := container.NewVBox(
		container.NewCenter(logo),
		container.NewCenter(titleText),
	)

	syncExperimentsBtn := widget.NewButton("Sync Locations", func() { showSyncExperiments(s) })
	syncExperimentsBtn.Importance = widget.HighImportance
	pullFilesBtn := widget.NewButton("Pull Files", func() { showPullFiles(s) })
	manageFilesBtn := widget.NewButton("Manage Files", func() { showManageFiles(s) })
	syncRecordersBtn := widget.NewButton("Offload Recorders", func() { showSyncRecorders(s) })
	syncRecordersBtn.Importance = widget.HighImportance
	locationsBtn := widget.NewButton("Edit Sync Locations", func() { showLocations(s) })
	settingsBtn := widget.NewButton("Settings", func() { showSettings(s) })

	if len(s.cfg.Locations) < 2 {
		syncExperimentsBtn.Disable()
	}
	if len(s.cfg.Locations) < 1 {
		pullFilesBtn.Disable()
		manageFilesBtn.Disable()
	}

	body := container.NewVBox(
		title,
		widget.NewSeparator(),
		syncRecordersBtn,
		syncExperimentsBtn,
		pullFilesBtn,
		manageFilesBtn,
	)
	body.Add(locationsBtn)
	body.Add(settingsBtn)
	s.setContent(container.NewPadded(container.NewVBox(widget.NewLabel(""), body)))
}
