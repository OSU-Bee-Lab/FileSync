package ui

import (
	"context"
	"os"
	"sort"
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

	d = dialog.NewCustom("Location not found", "Cancel",
		container.NewVBox(msgLabel, container.NewHBox(deselectBtn, reconnectBtn)), s.win)
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

	subpathEntry := widget.NewEntry()
	subpathEntry.SetPlaceHolder("leave blank for root")

	expEntry := widget.NewEntry()
	expEntry.SetPlaceHolder("Experiment name")

	syncingToLabel := widget.NewLabel("")
	syncingToLabel.Wrapping = fyne.TextWrapWord
	updateSyncingTo := func() {
		exp := strings.TrimSpace(expEntry.Text)
		if exp == "" {
			syncingToLabel.SetText("")
			return
		}
		parts := append([]string{exp}, splitSubpathUI(subpathEntry.Text)...)
		syncingToLabel.SetText("Syncing to: " + strings.Join(parts, "/"))
	}

	autoDeleteCheck := widget.NewCheck("Delete from recorder after verified copy", nil)
	autoDeleteCheck.SetChecked(s.cfg.RecorderSettings.AutoDeleteAfterVerify)

	startBtn := widget.NewButton("Start Sync", nil)
	startBtn.Importance = widget.HighImportance

	updateStartEnabled := func() {
		destinations := locationsFromNames(s.cfg.Locations, destGroup.Selected(), syncengine.LocationLocal)
		if len(destinations) > 0 && strings.TrimSpace(expEntry.Text) != "" {
			startBtn.Enable()
		} else {
			startBtn.Disable()
		}
	}

	// newExpChip + expEntry form the "custom experiment name" row. Tapping an
	// existing-experiment chip below fills expEntry with that name and
	// disables it (grayed out) to make clear the custom text isn't the
	// active choice; newExpChip re-activates custom entry. Both use
	// toggleChip - the same selected-highlight widget as destGroup - so the
	// "active" choice reads with the same color everywhere.
	var newExpChip *toggleChip
	var expChips []*toggleChip
	unhighlightExpChips := func() {
		for _, c := range expChips {
			c.SetSelected(false)
		}
	}
	selectCustomExperiment := func() {
		unhighlightExpChips()
		newExpChip.SetSelected(true)
		expEntry.Enable()
		subpathEntry.SetText(s.cfg.RecorderSettings.Subpaths[strings.TrimSpace(expEntry.Text)])
		s.win.Canvas().Focus(expEntry)
		updateStartEnabled()
	}
	selectExistingExperiment := func(name string, chip *toggleChip) {
		expEntry.SetText(name)
		expEntry.Disable()
		newExpChip.SetSelected(false)
		unhighlightExpChips()
		chip.SetSelected(true)
		subpathEntry.SetText(s.cfg.RecorderSettings.Subpaths[name])
		updateStartEnabled()
	}
	newExpChip = newToggleChip("New Experiment", nil)
	newExpChip.onTapped = selectCustomExperiment
	selectCustomExperiment()

	expGrid := container.NewGridWithColumns(4)
	scanStatusLabel := widget.NewLabel("")
	scanStatusLabel.Wrapping = fyne.TextWrapWord

	buildExpGrid := func(names []string) {
		expChips = make([]*toggleChip, len(names))
		objs := make([]fyne.CanvasObject, len(names))
		for i, name := range names {
			name := name
			chip := newToggleChip(name, nil)
			chip.onTapped = func() { selectExistingExperiment(name, chip) }
			expChips[i] = chip
			objs[i] = chip
		}
		expGrid.Objects = objs
		expGrid.Refresh()
		if expEntry.Disabled() {
			for _, c := range expChips {
				if c.label == expEntry.Text {
					c.SetSelected(true)
				}
			}
		}
	}

	var scanGen int
	refreshExistingExperiments := func() {
		scanGen++
		gen := scanGen
		destinations := locationsFromNames(s.cfg.Locations, destGroup.Selected(), syncengine.LocationLocal)
		if len(destinations) == 0 {
			buildExpGrid(nil)
			scanStatusLabel.SetText("")
			return
		}
		scanStatusLabel.SetText("Scanning destination(s)...")
		go func() {
			ctx := context.Background()
			names := dedupeExperimentNames(ctx, destinations)
			fyne.Do(func() {
				if gen != scanGen {
					// Selection changed since this scan started; a newer
					// scan is already in flight (or none is needed).
					return
				}
				buildExpGrid(names)
				if len(names) == 0 {
					scanStatusLabel.SetText("No experiments found at destination(s).")
				} else {
					scanStatusLabel.SetText("Existing experiments at destination(s):")
				}
			})
		}()
	}

	destGroup.OnChanged = func([]string) { updateStartEnabled(); refreshExistingExperiments() }
	expEntry.OnChanged = func(string) { updateStartEnabled(); updateSyncingTo() }
	subpathEntry.OnChanged = func(string) { updateSyncingTo() }
	updateStartEnabled()
	updateSyncingTo()
	refreshExistingExperiments()

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
				startRecorderSync(s, expEntry, subpathEntry, autoDeleteCheck, destinations, uploads)
			})
			return
		}
		startRecorderSync(s, expEntry, subpathEntry, autoDeleteCheck, destinations, uploads)
	}

	backBtn := widget.NewButton("Cancel", func() { showHome(s) })

	top := container.NewVBox(
		widget.NewLabelWithStyle("Sync Recorders", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			&widget.FormItem{Text: "Destination(s)", Widget: destGroup.CanvasObject()},
			&widget.FormItem{Text: "", Widget: autoDeleteCheck},
		),
		widget.NewLabelWithStyle("Experiment", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewBorder(nil, nil, newExpChip, nil, expEntry),
		scanStatusLabel,
		expGrid,
	)
	bottom := container.NewVBox(
		widget.NewForm(&widget.FormItem{Text: "Subpath", Widget: subpathEntry}),
		syncingToLabel,
		widget.NewSeparator(),
		container.NewHBox(backBtn, startBtn),
	)
	content := container.NewBorder(top, bottom, nil, nil)
	s.setContent(container.NewPadded(content))
}

// startRecorderSync persists the chosen recorder settings and transitions
// to the active-sync screen with destinations/uploads already resolved.
func startRecorderSync(s *state, expEntry, subpathEntry *widget.Entry, autoDeleteCheck *widget.Check, destinations, uploads []syncengine.Location) {
	subpath := strings.TrimSpace(subpathEntry.Text)
	expName := strings.TrimSpace(expEntry.Text)

	s.cfg.RecorderSettings.DestinationLocationIDs = idsFromLocations(destinations)
	s.cfg.RecorderSettings.UploadLocationIDs = idsFromLocations(uploads)
	s.cfg.RecorderSettings.AutoDeleteAfterVerify = autoDeleteCheck.Checked
	if s.cfg.RecorderSettings.Subpaths == nil {
		s.cfg.RecorderSettings.Subpaths = make(map[string]string)
	}
	s.cfg.RecorderSettings.Subpaths[expName] = subpath
	s.saveConfig()

	showRecorderSync(s, recorderSyncParams{
		destinations:   destinations,
		uploads:        uploads,
		subpath:        subpath,
		experimentName: strings.TrimSpace(expEntry.Text),
		autoDelete:     autoDeleteCheck.Checked,
	})
}

// dedupeExperimentNames lists experiments present at each of locs (always
// at the location's root - experiment directories are never nested under
// subpath) and returns the union of names, deduped and sorted. Locations
// that fail to list (e.g. unreachable remote) are silently skipped rather
// than aborting the whole scan, since this is an informational listing,
// not a precondition for starting a sync.
func dedupeExperimentNames(ctx context.Context, locs []syncengine.Location) []string {
	seen := make(map[string]bool)
	for _, loc := range locs {
		exps, err := syncengine.ListExperiments(ctx, loc)
		if err != nil {
			continue
		}
		for _, e := range exps {
			seen[e.Name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
