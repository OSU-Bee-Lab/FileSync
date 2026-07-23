package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/appconfig"
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

// showLocationsNotFoundPrompt is shown when a chosen local location (or
// another local location an operation depends on) can't be found on disk -
// e.g. an external drive that's unplugged. In practice this is
// local-drive-only: every caller resolves missing via missingLocalLocations,
// which explicitly skips remotes. It walks missing one location at a time,
// naming each one individually, since every caller now catches "not found"
// per-location rather than batching a scan/offload's whole selection into
// one message. There is no Cancel: each location must be either deselected
// or reconnected before this returns control to the caller.
// "Deselect and continue" drops that one location and moves to the next
// missing one (or finishes). "Reconnect" re-runs the presence check for
// that location (for after the drive has been plugged back in): if it now
// resolves, this moves to the next missing one (or finishes); otherwise the
// prompt stays up for the same location.
// Once every missing location has been resolved, onFound runs if none were
// deselected (everything reconnected); otherwise onDeselect runs with the
// locations the user chose to deselect, so the caller can drop them from its
// selection - it does not also run onFound, mirroring the original
// deselect-then-let-the-user-retry behavior.
func showLocationsNotFoundPrompt(s *state, missing []syncengine.Location, onDeselect func(deselected []syncengine.Location), onFound func()) {
	queue := append([]syncengine.Location{}, missing...)
	var deselected []syncengine.Location

	msgLabel := widget.NewLabel("")
	msgLabel.Wrapping = fyne.TextWrapWord

	var d dialog.Dialog
	var showNext func()

	finish := func() {
		d.Hide()
		if len(deselected) > 0 {
			if onDeselect != nil {
				onDeselect(deselected)
			}
			return
		}
		onFound()
	}

	showNext = func() {
		if len(queue) == 0 {
			finish()
			return
		}
		msgLabel.SetText(fmt.Sprintf("Location %q not found.\n\nPlug in the missing drive and press Reconnect, or deselect it to continue without it.", queue[0].Name))
	}

	deselectBtn := widget.NewButton("Deselect and continue", func() {
		deselected = append(deselected, queue[0])
		queue = queue[1:]
		showNext()
	})
	reconnectBtn := widget.NewButton("Reconnect", func() {
		if stillMissing := missingLocalLocations(queue[0]); len(stillMissing) == 0 {
			queue = queue[1:]
			showNext()
		}
	})
	reconnectBtn.Importance = widget.HighImportance

	d = dialog.NewCustomWithoutButtons("Location not found",
		container.NewVBox(msgLabel, container.NewCenter(container.NewHBox(deselectBtn, reconnectBtn))), s.win)
	showNext()
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
	// detectBadTimestamps and timestampTolerance gate/configure per-recorder
	// bad-first-timestamp detection - see recorder.DetectBadFirstTimestamp.
	// Dev-only for now (see devMode).
	detectBadTimestamps bool
	timestampTolerance  time.Duration
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

	detectTimestampsCheck := widget.NewCheck("Detect bad recorder timestamps", nil)
	detectTimestampsCheck.SetChecked(s.cfg.RecorderSettings.DetectBadTimestamps)
	// The match tolerance itself is set live on the timestamp review screen
	// (with a slider that re-judges every recorder as it moves), not here -
	// that's where seeing its effect is useful. It persists to
	// RecorderSettings.TimestampToleranceMinutes from there.
	detectTimestampsHint := widget.NewLabel("(flags a recorder's first file if its timestamp looks wrong - bad AM/PM, year, month, or day - once every file from that recorder has landed locally. Adjust the match tolerance on the review screen.)")
	detectTimestampsHint.Wrapping = fyne.TextWrapWord

	detectTimestampsBox := container.NewVBox(detectTimestampsCheck, detectTimestampsHint)

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

	// checkMissingDestinations pops the not-found prompt immediately for any
	// currently-selected destination that isn't present on disk (e.g. an
	// unplugged external drive), rather than waiting for Start to be
	// pressed. onOK runs once every destination is confirmed present
	// (nothing was missing, or every missing one got reconnected); if any
	// were deselected instead, this drops them from destGroup itself and
	// does not call onOK, matching showLocationsNotFoundPrompt's contract.
	checkMissingDestinations := func(onOK func()) {
		destinations := locationsFromNames(s.cfg.Locations, destGroup.Selected(), syncengine.LocationLocal)
		if missing := missingLocalLocations(destinations...); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func(deselected []syncengine.Location) {
				keep := make([]string, 0, len(destGroup.Selected()))
				for _, name := range destGroup.Selected() {
					if loc := findLocation(s.cfg.Locations, name); loc == nil || !containsLocation(deselected, *loc) {
						keep = append(keep, name)
					}
				}
				destGroup.SetSelected(keep)
				updateStartEnabled()
				refreshBrowserLocations()
			}, onOK)
			return
		}
		onOK()
	}

	destGroup.OnChanged = func([]string) {
		updateStartEnabled()
		refreshBrowserLocations()
		checkMissingDestinations(func() {})
	}
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
	// Catch a destination that's already missing when the screen opens
	// (e.g. persisted from RecorderSettings but its drive is unplugged
	// right now), not just once the user touches destGroup or presses Start.
	if len(destGroup.Selected()) > 0 {
		checkMissingDestinations(func() {})
	}

	startBtn.OnTapped = func() {
		// checkMissingDestinations is also run on every destGroup change and
		// at screen load, so this is a safety net for a drive that goes
		// missing in between rather than the primary catch.
		checkMissingDestinations(func() {
			destinations := locationsFromNames(s.cfg.Locations, destGroup.Selected(), syncengine.LocationLocal)
			uploads := locationsFromNames(s.cfg.Locations, uploadGroup.Selected(), syncengine.LocationRemote)
			startRecorderSync(s, browser.RelPath(), autoDeleteCheck, batchUploadCheck, detectTimestampsCheck, destinations, uploads)
		})
	}

	backBtn := widget.NewButton("Back", func() { showHome(s) })

	optionsCol := container.NewVBox(
		sectionHeader("Sync Locations"),
		widget.NewForm(
			&widget.FormItem{Text: "Local", Widget: destGroup.CanvasObject()},
			&widget.FormItem{Text: "Remote", Widget: uploadGroup.CanvasObject()},
			&widget.FormItem{Text: "", Widget: container.NewVBox(batchUploadCheck, batchUploadHint)},
			&widget.FormItem{Text: "", Widget: autoDeleteCheck},
			&widget.FormItem{Text: "", Widget: detectTimestampsBox},
		),
	)
	destCol := container.NewBorder(
		sectionHeader("Sync Destination"),
		nil, nil, nil,
		browser.CanvasObject(),
	)
	columns := container.NewHSplit(optionsCol, destCol)
	columns.SetOffset(0.35)

	top := widget.NewLabelWithStyle("Offload Recorders", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
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
func startRecorderSync(s *state, relPath string, autoDeleteCheck, batchUploadCheck, detectTimestampsCheck *widget.Check, destinations, uploads []syncengine.Location) {
	segments := splitSubpathUI(relPath)
	expName := ""
	var subpathParts []string
	if len(segments) > 0 {
		expName = segments[0]
		subpathParts = segments[1:]
	}
	subpath := strings.Join(subpathParts, "/")

	// Tolerance is no longer set here - it's driven live on the review screen
	// (and Load backfills a sane default), so read whatever's persisted and
	// pass it through as the review screen's starting value.
	tolerance := s.cfg.RecorderSettings.TimestampToleranceMinutes
	if tolerance <= 0 {
		tolerance = appconfig.DefaultTimestampToleranceMinutes
	}

	s.cfg.RecorderSettings.DestinationLocationIDs = idsFromLocations(destinations)
	s.cfg.RecorderSettings.UploadLocationIDs = idsFromLocations(uploads)
	s.cfg.RecorderSettings.AutoDeleteAfterVerify = autoDeleteCheck.Checked
	s.cfg.RecorderSettings.BatchUpload = batchUploadCheck.Checked
	s.cfg.RecorderSettings.DetectBadTimestamps = detectTimestampsCheck.Checked
	s.cfg.RecorderSettings.TimestampToleranceMinutes = tolerance
	s.saveConfig()

	showRecorderSync(s, recorderSyncParams{
		destinations:        destinations,
		uploads:             uploads,
		subpath:             subpath,
		experimentName:      expName,
		autoDelete:          autoDeleteCheck.Checked,
		batchUpload:         batchUploadCheck.Checked && len(uploads) > 0,
		detectBadTimestamps: detectTimestampsCheck.Checked,
		timestampTolerance:  time.Duration(tolerance) * time.Minute,
	})
}
