// Package ui is the Fyne presentation layer for FileSync. It never imports
// rclone directly - it only calls internal/syncengine's exported API - so
// the rclone dependency stays confined to one package.
package ui

import (
	"context"
	_ "embed"
	"net/url"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/appconfig"
	"github.com/OSU-Bee-Lab/filesync/internal/appversion"
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
	// syncOneWay and the fields below cache the Sync Locations screen's
	// mode toggle and, in One Way Sync mode, its chosen source folder and
	// destination locations/folder — mirrors syncExperimentsLocationNames'
	// role for the All-Way Sync mode. One Way pushes the source folder onto
	// one or more destination locations at once (all at the same relPath),
	// so the destination is a set of location names, not a single one.
	syncOneWay           bool
	syncOneWayFromFolder string
	syncOneWayToNames    []string
	syncOneWayToRelPath  string
	// syncRole is One Way Sync's Audio/Results toggle (see roleToggle) -
	// session-only, like syncOneWay. Zero value RoleAudio matches the
	// toggle's default selection. All-Way Sync has no such toggle: it
	// offers both roles at once and converges each within itself.
	syncRole syncengine.LocationRole

	// pullFilesSourceName, pullFilesDestFolder, pullFilesRelPath, and
	// pullFilesFullIdent cache the Pull Files screen's source/destination
	// choice, browsed scope, and "Use full ident" toggle so they survive a
	// round trip through Scan and back (same role as syncOneWay* above).
	// pullFilesFullIdent defaults to true (set in Run) to match the
	// screen's own initial checked state.
	pullFilesSourceName string
	pullFilesDestFolder string
	pullFilesRelPath    string
	pullFilesFullIdent  bool

	// manageFilesOp, manageFilesFrom, manageFilesTo, manageFilesDeleteConfirm,
	// and manageFilesPickerTarget cache the Manage Files setup screen's
	// operation choice, From/To paths, delete-confirmation text, and which
	// picker pane is active, so they survive a round trip through Preview
	// (or the Retime review screen) and back - same role as pullFiles*
	// above. manageFilesOp/manageFilesPickerTarget default to "" and are
	// treated as "Rename / Move / Merge"/"From" respectively when empty.
	manageFilesOp            string
	manageFilesFrom          string
	manageFilesTo            string
	manageFilesDeleteConfirm string
	manageFilesPickerTarget  string

	// availableUpdate holds the result of the GitHub release check kicked
	// off in Run, once it completes - nil until then, and nil forever if
	// already up to date or the check failed. showHome reads it each time
	// it's (re)built, so the "New Version Available" link appears next time
	// the user lands on the home screen after the check finishes, without
	// needing to interrupt whatever screen they're currently on.
	availableUpdate atomic.Pointer[appversion.Update]

	// quitCheck, while non-nil, is asked whether quitting the app right now
	// would interrupt or abandon anything, so the window-close handler can
	// warn before actually closing. Only the screens that can leave
	// something in that state (progressScreen, recorderSyncScreen) set it,
	// once they've built their content - setContent
	// clear it back to nil on every screen change first, so navigating away
	// (e.g. via Back) always reverts to "nothing to warn about" unless the
	// new screen re-sets it itself.
	quitCheck func() quitState
}

// quitState is what a currently-shown screen reports about the consequence
// of quitting the app right now.
type quitState struct {
	// active means a transfer is actively running - quitting now would
	// interrupt it mid-copy, the same danger showDangerConfirm already
	// guards on the Cancel/End Sync buttons.
	active bool
	// pending means nothing is actively transferring, but data was already
	// offloaded from a recorder onto the local drive and hasn't gone
	// through its batch sync yet. Safe on disk, but quitting now leaves it
	// unsynced to the other Locations until the user comes back for it.
	pending bool
}

// boundedWidthLayout caps the reported minimum width of its content to
// maxWidth and centers it within whatever width it's actually given. Used in
// two places: as centerMaxWidth, for wrapping an individual screen's
// narrow, form-only content (entries/selects that would look absurd
// stretched edge to edge across a maximized window) so it stays a
// comfortable reading width and centers instead of hugging the left edge;
// and, historically, as the layout under every screen via setContent - see
// growingWidthLayout below for why that's no longer the default.
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

// centerMaxWidth wraps content so its width never exceeds maxWidth,
// centering it within whatever space its parent actually gives it. Every
// screen now fills the full (possibly maximized) window width via
// setContent/growingWidthLayout, so a screen whose content is just a narrow
// form (entries, selects, labels - nothing designed to make use of extra
// width) should wrap that content in centerMaxWidth before handing it to
// setContent, rather than letting its fields stretch edge to edge across a
// wide window.
func centerMaxWidth(content fyne.CanvasObject, maxWidth float32) fyne.CanvasObject {
	return container.New(&boundedWidthLayout{maxWidth: maxWidth}, content)
}

// currentOrDefaultSize returns w's current content size, or fallback if the
// window hasn't been shown yet or has somehow shrunk below it. setContent
// uses this (captured before SetContent) to restore whatever size the
// window actually had - rather than always stomping it back to a fixed
// windowSize - so that a maximized or user-resized window stays that size
// across screen changes instead of snapping back to windowSize on every
// navigation.
func currentOrDefaultSize(w fyne.Window, fallback fyne.Size) fyne.Size {
	cur := w.Canvas().Size()
	if cur.Width < fallback.Width || cur.Height < fallback.Height {
		return fallback
	}
	return cur
}

// growingWidthLayout caps the reported minimum width of its content to
// maxWidth - same as boundedWidthLayout, so a wide child (an untruncated
// long path label, say) still can't force the window itself wider than
// windowSize, which is what stretches it across displays on multi-monitor
// setups (see windowSize) - but unlike boundedWidthLayout it always lays its
// child out at the container's full given size rather than clamping it.
// This is what lets a screen's content actually grow to fill a
// maximized/resized window instead of staying capped at windowSize; a
// screen that wants to stay a fixed comfortable width regardless (a narrow
// form) should wrap that content in centerMaxWidth (above) before passing it
// to setContent.
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

// setContent replaces the window's content and re-asserts the window's
// pre-swap size immediately after (see currentOrDefaultSize). Screens must
// call this instead of s.win.SetContent directly - see the comment on
// windowSize for why. Content is wrapped in a growingWidthLayout so it fills
// however wide the window actually is (including maximized) instead of
// staying capped at windowSize; wrap content in centerMaxWidth first if it
// shouldn't stretch that wide (see centerMaxWidth).
func (s *state) setContent(content fyne.CanvasObject) {
	s.quitCheck = nil
	stopAudio()
	clearAudioRefreshers()
	size := currentOrDefaultSize(s.win, windowSize)
	growing := container.New(&growingWidthLayout{maxWidth: windowSize.Width}, content)
	s.win.SetContent(growing)
	s.win.Resize(size)
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
		s := &state{win: w, cfg: cfg, pullFilesFullIdent: true}
		w.SetCloseIntercept(func() { confirmQuit(s) })
		if err != nil {
			// Not fatal - fall back to defaults and let the user fix it by
			// re-saving from the Locations screen.
			s.cfg = appconfig.Default()
		}
		syncengine.SetDebugLogging(s.cfg.DebugMode)
		syncengine.SetCheckers(s.cfg.Checkers)
		syncengine.SetBwLimitMiBPerSec(s.cfg.BwLimitMiBPerSec)
		syncengine.SetTransfers(s.cfg.Transfers)
		syncengine.SetCopyRetries(s.cfg.CopyRetries)
		syncengine.SetHTTP2Enabled(s.cfg.HTTP2Enabled)

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if update, err := appversion.CheckForUpdate(ctx); err == nil && update != nil {
				s.availableUpdate.Store(update)
			}
		}()

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

		// Exit is the recommended way out of this dialog, so it gets the
		// primary styling and the rightmost slot; Continue is the discouraged
		// escape hatch and is styled as the hazard it is (two instances
		// copying to the same destination can corrupt each other's work).
		closeBtn := widget.NewButton("Exit", func() { w.Close() })
		closeBtn.Importance = widget.HighImportance

		continueBtn := widget.NewButton("Open Anyway", func() {
			startApp()
		})
		continueBtn.Importance = widget.DangerImportance

		content := container.NewVBox(
			msg,
			container.NewHBox(layout.NewSpacer(), actionRow(continueBtn, closeBtn), layout.NewSpacer()),
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

// confirmQuit is the main window's close handler (set via
// SetCloseIntercept). It asks the currently-shown screen, if any, whether
// quitting right now would interrupt or abandon anything (see quitCheck) and
// warns accordingly before actually closing - an active transfer gets the
// same "this will interrupt it" danger warning as the Cancel/End Sync
// buttons, while offloaded-but-unsynced recorder data gets a softer warning
// since nothing is lost, just left unsynced.
func confirmQuit(s *state) {
	var qs quitState
	if s.quitCheck != nil {
		qs = s.quitCheck()
	}
	switch {
	case qs.active:
		showDangerConfirm("Sync still in progress",
			"A transfer is still running. Quitting now will interrupt it before it finishes.",
			"Quit", "Keep Syncing", func(ok bool) {
				if ok {
					s.win.Close()
				}
			}, s.win)
	case qs.pending:
		showDangerConfirm("Recorder files not yet synced",
			"Files offloaded from a recorder are safely on this computer's drive, but haven't been synced to your other Locations yet. Quitting now will leave them unsynced until you come back and finish the batch sync.",
			"Quit", "Go Back", func(ok bool) {
				if ok {
					s.win.Close()
				}
			}, s.win)
	default:
		s.win.Close()
	}
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
		manageFilesBtn,
		pullFilesBtn,
	)
	body.Add(locationsBtn)
	body.Add(settingsBtn)

	main := container.NewPadded(container.NewVBox(widget.NewLabel(""), body))

	versionText := canvas.NewText("v"+appversion.Version, theme.Color(theme.ColorNameDisabled))
	versionText.Alignment = fyne.TextAlignTrailing
	bottomRightItems := []fyne.CanvasObject{versionText}
	if update := s.availableUpdate.Load(); update != nil {
		link := widget.NewHyperlink("New Version Available", nil)
		link.OnTapped = func() {
			if u, err := url.Parse(update.URL); err == nil {
				fyne.CurrentApp().OpenURL(u)
			}
		}
		bottomRightItems = append(bottomRightItems, link)
	}
	bottomRight := container.NewPadded(container.NewVBox(bottomRightItems...))

	s.setContent(container.NewBorder(nil, container.NewHBox(layout.NewSpacer(), bottomRight), nil, nil, centerMaxWidth(main, windowSize.Width)))
}
