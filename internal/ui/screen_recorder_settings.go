package ui

import (
	"os"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// splitSubpathUI mirrors recorder.splitSubpath's separator handling (accept
// "/" or "\" regardless of OS) for building the "Syncing to:" preview.
func splitSubpathUI(subpath string) []string {
	return strings.FieldsFunc(subpath, func(r rune) bool { return r == '/' || r == '\\' })
}

// missingLocalLocations reports which of locs (only LocationLocal ones are
// checked - a remote's availability doesn't depend on anything mounted on
// this machine) don't currently resolve to a present directory, e.g. an
// external drive that's been unplugged since the location was configured.
func missingLocalLocations(locs ...syncengine.Location) []syncengine.Location {
	var missing []syncengine.Location
	for _, l := range locs {
		if l.Kind != syncengine.LocationLocal {
			continue
		}
		if info, err := os.Stat(l.RootPath); err != nil || !info.IsDir() {
			missing = append(missing, l)
		}
	}
	return missing
}

// showLocationsNotFoundPrompt is shown when a recorder offload's chosen
// destination (or another local location it depends on) can't be found on
// disk - e.g. an external drive that's unplugged. In practice this is
// local-drive-only: its only caller resolves missing via
// missingLocalLocations, which explicitly skips remotes. "Cancel" (the
// dialog's built-in dismiss button) simply aborts the offload attempt.
// "Deselect and continue" drops the missing location(s) from the current
// selection (via onDeselect) and dismisses the prompt, leaving the location
// itself untouched - it's still offered next time, since accessibility is
// checked fresh at sync time rather than baked into the location's config.
// "Reconnect" re-runs the same presence check (for after the drive has been
// plugged back in): if everything now resolves it dismisses the prompt and
// runs onFound; if something's still missing it updates the message in
// place naming what's still absent, rather than closing.
func showLocationsNotFoundPrompt(s *state, missing []syncengine.Location, onDeselect func(), onFound func()) {
	msgLabel := widget.NewLabel("")
	msgLabel.Wrapping = fyne.TextWrapWord
	setMsg := func(missing []syncengine.Location) {
		names := make([]string, len(missing))
		for i, l := range missing {
			names[i] = l.Name
		}
		msgLabel.SetText("Location(s) not found:\n\n" + strings.Join(names, "\n") +
			"\n\nPlug in the missing drive(s) and press Reconnect, deselect them to continue without them, or cancel.")
	}
	setMsg(missing)

	var d dialog.Dialog
	deselectBtn := widget.NewButton("Deselect and continue", func() {
		d.Hide()
		if onDeselect != nil {
			onDeselect()
		}
	})
	reconnectBtn := widget.NewButton("Reconnect", func() {
		stillMissing := missingLocalLocations(missing...)
		if len(stillMissing) == 0 {
			d.Hide()
			onFound()
			return
		}
		missing = stillMissing
		setMsg(missing)
	})
	reconnectBtn.Importance = widget.HighImportance
	cancelBtn := widget.NewButton("Cancel", func() { d.Hide() })

	d = dialog.NewCustomWithoutButtons("Location not found",
		container.NewVBox(msgLabel, container.NewCenter(container.NewHBox(deselectBtn, reconnectBtn, cancelBtn))), s.win)
	d.Show()
}

// recorderSyncParams is the locked-in configuration chosen on the
// recorder-settings screen (Screen 1) and handed to the active-sync
// screen (Screen 2). Nothing on Screen 2 can change these; to change
// anything the user must Cancel Recorder Sync back to Screen 1 and start over.
type recorderSyncParams struct {
	destinations   []syncengine.Location // local, at least one
	uploads        []syncengine.Location // remote, may be empty (no cloud upload)
	subpath        string                // optional intermediate directories within the experiment, before recorderID (see SCHEMA.md)
	experimentName string
	autoDelete     bool
}

// showSyncRecorders is the entry point for the Sync Recorders feature: the
// settings screen (Screen 1) shown before any sync activity starts.
func showSyncRecorders(s *state) {
	// destGroup offers every configured Location - local and cloud alike -
	// as one combined destination picker. At sync time the selection is
	// split back out by Kind: local ones are copied to directly, cloud
	// ones (see recorderSyncParams.uploads) are uploaded to after the
	// local copy completes (internal/recorder.StartOffload always needs
	// at least one local destination to stage from).
	preselected := append(append([]string{}, selectedFromIDs(s.cfg.Locations, s.cfg.RecorderSettings.DestinationLocationIDs)...),
		selectedFromIDs(s.cfg.Locations, s.cfg.RecorderSettings.UploadLocationIDs)...)
	destGroup := newToggleGroup(locationNames(s.cfg.Locations), preselected)

	// browser replaces free-typed experiment name + subpath entry: its
	// root level *is* the experiment picker (each top-level folder is an
	// experiment), and drilling further in is the subpath. The current
	// browse depth is split back into experimentName/subpath at Start.
	browser := newDestFolderBrowser(s.win, true)

	syncingToLabel := widget.NewLabel("")
	syncingToLabel.Wrapping = fyne.TextWrapWord
	updateSyncingTo := func() {
		if browser.RelPath() == "" {
			syncingToLabel.SetText("")
			return
		}
		syncingToLabel.SetText("Syncing to: " + browser.RelPath())
	}

	autoDeleteCheck := widget.NewCheck("Delete from recorder after verified copy", nil)
	autoDeleteCheck.SetChecked(s.cfg.RecorderSettings.AutoDeleteAfterVerify)

	startBtn := widget.NewButton("Sync Here", nil)
	startBtn.Importance = widget.HighImportance

	updateStartEnabled := func() {
		destinations := locationsFromNames(s.cfg.Locations, destGroup.Selected(), syncengine.LocationLocal)
		if len(destinations) > 0 && browser.RelPath() != "" {
			startBtn.Enable()
		} else {
			startBtn.Disable()
		}
	}

	browser.OnPathChanged = func(string) { updateStartEnabled(); updateSyncingTo() }

	refreshBrowserLocations := func() {
		browser.SetLocations(locationsFromNamesAny(s.cfg.Locations, destGroup.Selected()))
	}

	destGroup.OnChanged = func([]string) { updateStartEnabled(); refreshBrowserLocations() }
	updateStartEnabled()
	updateSyncingTo()
	refreshBrowserLocations()

	startBtn.OnTapped = func() {
		selected := destGroup.Selected()
		destinations := locationsFromNames(s.cfg.Locations, selected, syncengine.LocationLocal)
		uploads := locationsFromNames(s.cfg.Locations, selected, syncengine.LocationRemote)

		if missing := missingLocalLocations(destinations...); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func() {
				// User chose to deselect the missing destination(s); drop
				// them from the current selection and let them retry from
				// this same screen rather than persistently disabling
				// anything.
				keep := make([]string, 0, len(selected))
				for _, name := range selected {
					if loc := findLocation(s.cfg.Locations, name); loc == nil || !containsLocation(missing, *loc) {
						keep = append(keep, name)
					}
				}
				destGroup.SetSelected(keep)
				updateStartEnabled()
			}, func() {
				startRecorderSync(s, browser.RelPath(), autoDeleteCheck, destinations, uploads)
			})
			return
		}
		startRecorderSync(s, browser.RelPath(), autoDeleteCheck, destinations, uploads)
	}

	backBtn := widget.NewButton("Cancel", func() { showHome(s) })

	top := container.NewVBox(
		widget.NewLabelWithStyle("Sync Recorders", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			&widget.FormItem{Text: "Destination(s)", Widget: destGroup.CanvasObject()},
			&widget.FormItem{Text: "", Widget: autoDeleteCheck},
		),
		widget.NewLabelWithStyle("Sync Destination", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	)
	bottom := container.NewVBox(
		syncingToLabel,
		widget.NewSeparator(),
		container.NewHBox(backBtn, startBtn),
	)
	content := container.NewBorder(top, bottom, nil, nil, browser.CanvasObject())
	s.setContent(container.NewPadded(content))
}

// startRecorderSync persists the chosen recorder settings and transitions
// to the active-sync screen with destinations/uploads already resolved.
// relPath is the folder chosen in the destination browser; its first
// segment is the experiment name and everything after it is the subpath.
func startRecorderSync(s *state, relPath string, autoDeleteCheck *widget.Check, destinations, uploads []syncengine.Location) {
	segments := splitSubpathUI(relPath)
	expName := ""
	var subpathParts []string
	if len(segments) > 0 {
		expName = segments[0]
		subpathParts = segments[1:]
	}
	subpath := strings.Join(subpathParts, "/")

	s.cfg.RecorderSettings.DestinationLocationIDs = idsFromLocations(destinations)
	s.cfg.RecorderSettings.UploadLocationIDs = idsFromLocations(uploads)
	s.cfg.RecorderSettings.AutoDeleteAfterVerify = autoDeleteCheck.Checked
	s.saveConfig()

	showRecorderSync(s, recorderSyncParams{
		destinations:   destinations,
		uploads:        uploads,
		subpath:        subpath,
		experimentName: expName,
		autoDelete:     autoDeleteCheck.Checked,
	})
}
