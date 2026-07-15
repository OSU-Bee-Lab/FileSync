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
	// batchUpload, when true and uploads is non-empty, defers cloud upload
	// until the user presses Batch Upload on Screen 2 instead of uploading
	// each file as soon as it's verified complete locally.
	batchUpload bool
}

// showSyncRecorders is the entry point for the Sync Recorders feature: the
// settings screen (Screen 1) shown before any sync activity starts.
func showSyncRecorders(s *state) {
	// destGroup offers local Locations only - files are always copied off
	// the recorder to at least one of these directly
	// (internal/recorder.StartOffload always needs a local destination to
	// stage from). uploadGroup offers cloud Locations, uploaded to only
	// after the local copy completes.
	destGroup := newToggleGroup(locationNamesByKind(s.cfg.Locations, syncengine.LocationLocal),
		selectedFromIDs(s.cfg.Locations, s.cfg.RecorderSettings.DestinationLocationIDs))
	uploadGroup := newToggleGroup(locationNamesByKind(s.cfg.Locations, syncengine.LocationRemote),
		selectedFromIDs(s.cfg.Locations, s.cfg.RecorderSettings.UploadLocationIDs))

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

	autoDeleteCheck := widget.NewCheck("Remove files from recorders after sync", nil)
	autoDeleteCheck.SetChecked(s.cfg.RecorderSettings.AutoDeleteAfterVerify)

	// batchUploadCheck only makes sense once a cloud destination is picked;
	// see uploadGroup.OnChanged below, which enables/disables it rather than
	// hiding it - the hint stays visible either way so its explanation isn't
	// popping in and out as the user picks a cloud upload. Defaults on:
	// batching is the faster choice whenever there are many files, and
	// per-file upload-as-it-lands is the exception a user opts into by
	// unchecking it.
	batchUploadCheck := widget.NewCheck("Batch upload after local sync", nil)
	batchUploadCheck.SetChecked(s.cfg.RecorderSettings.BatchUpload)
	batchUploadCheck.Disable()
	// Hint rides in parens directly under the checkbox's own label rather
	// than a separate form row - widget.Check's label is a single-line
	// canvas.Text that can't wrap itself, so the wrapping continuation
	// lives in this adjacent Label instead (see the VBox pairing them
	// below - an HBox here would leave the Label with no bound width to
	// wrap against, breaking it into one character per line).
	batchUploadHint := widget.NewLabel("(faster when syncing many files - uploads everything at once instead of as each file lands.)")
	batchUploadHint.Wrapping = fyne.TextWrapWord

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

	// The browser shows the union of destination and upload locations'
	// existing folder structure (see dest_folder_browser.go), so it needs
	// refreshing whenever either group's selection changes.
	refreshBrowserLocations := func() {
		names := append(append([]string{}, destGroup.Selected()...), uploadGroup.Selected()...)
		browser.SetLocations(locationsFromNamesAny(s.cfg.Locations, names))
	}

	destGroup.OnChanged = func([]string) { updateStartEnabled(); refreshBrowserLocations() }
	uploadGroup.OnChanged = func(sel []string) {
		refreshBrowserLocations()
		if len(sel) > 0 {
			batchUploadCheck.Enable()
		} else {
			batchUploadCheck.Disable()
		}
	}
	updateStartEnabled()
	updateSyncingTo()
	uploadGroup.OnChanged(uploadGroup.Selected())

	startBtn.OnTapped = func() {
		destinations := locationsFromNames(s.cfg.Locations, destGroup.Selected(), syncengine.LocationLocal)
		uploads := locationsFromNames(s.cfg.Locations, uploadGroup.Selected(), syncengine.LocationRemote)

		if missing := missingLocalLocations(destinations...); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func() {
				// User chose to deselect the missing destination(s); drop
				// them from the current selection and let them retry from
				// this same screen rather than persistently disabling
				// anything.
				keep := make([]string, 0, len(destGroup.Selected()))
				for _, name := range destGroup.Selected() {
					if loc := findLocation(s.cfg.Locations, name); loc == nil || !containsLocation(missing, *loc) {
						keep = append(keep, name)
					}
				}
				destGroup.SetSelected(keep)
				updateStartEnabled()
			}, func() {
				startRecorderSync(s, browser.RelPath(), autoDeleteCheck, batchUploadCheck, destinations, uploads)
			})
			return
		}
		startRecorderSync(s, browser.RelPath(), autoDeleteCheck, batchUploadCheck, destinations, uploads)
	}

	backBtn := widget.NewButton("Back", func() { showHome(s) })

	optionsCol := container.NewVBox(
		sectionHeader("Sync Locations"),
		widget.NewForm(
			&widget.FormItem{Text: "Local", Widget: destGroup.CanvasObject()},
			&widget.FormItem{Text: "Remote", Widget: uploadGroup.CanvasObject()},
			&widget.FormItem{Text: "", Widget: container.NewVBox(batchUploadCheck, batchUploadHint)},
			&widget.FormItem{Text: "", Widget: autoDeleteCheck},
		),
	)
	destCol := container.NewBorder(
		sectionHeader("Sync Destination"),
		nil, nil, nil,
		browser.CanvasObject(),
	)
	columns := container.NewHSplit(optionsCol, destCol)
	columns.SetOffset(0.35)

	top := widget.NewLabelWithStyle("Sync Recorders", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	bottom := container.NewVBox(
		syncingToLabel,
		widget.NewSeparator(),
		container.NewHBox(backBtn, startBtn),
	)
	content := container.NewBorder(top, bottom, nil, nil, columns)
	s.setContent(container.NewPadded(content))
}

// startRecorderSync persists the chosen recorder settings and transitions
// to the active-sync screen with destinations/uploads already resolved.
// relPath is the folder chosen in the destination browser; its first
// segment is the experiment name and everything after it is the subpath.
func startRecorderSync(s *state, relPath string, autoDeleteCheck, batchUploadCheck *widget.Check, destinations, uploads []syncengine.Location) {
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
	s.cfg.RecorderSettings.BatchUpload = batchUploadCheck.Checked
	s.saveConfig()

	showRecorderSync(s, recorderSyncParams{
		destinations:   destinations,
		uploads:        uploads,
		subpath:        subpath,
		experimentName: expName,
		autoDelete:     autoDeleteCheck.Checked,
		batchUpload:    batchUploadCheck.Checked && len(uploads) > 0,
	})
}
