package ui

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// colorRGBA builds an image/color.NRGBA, used for recorderRow status
// background tints (see rowBackgroundColor).
func colorRGBA(r, g, b, a uint8) color.Color {
	return color.NRGBA{R: r, G: g, B: b, A: a}
}

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
// disk - e.g. an external drive that's unplugged, or a cloud remote that's
// unreachable. "Cancel" (the dialog's built-in dismiss button) simply
// aborts the offload attempt. "Deselect and continue" drops the missing
// location(s) from the current selection (via onDeselect) and dismisses
// the prompt, leaving the location itself untouched - it's still offered
// next time, since accessibility is checked fresh at sync time rather than
// baked into the location's config. "Reconnect" re-runs the same presence
// check (for after the drive has been plugged back in): if everything now
// resolves it dismisses the prompt and runs onFound; if something's still
// missing it updates the message in place naming what's still absent,
// rather than closing.
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

// recorderInactivityTimeout is how long showRecorderSync waits for a new
// recorder to attach before pausing the session and prompting the user.
const recorderInactivityTimeout = 5 * time.Minute

// showInactivitySyncPrompt is shown when no new recorder has attached within
// recorderInactivityTimeout during an active sync session. "Continue Sync"
// dismisses the prompt and resets the timer; "End Sync" mirrors the screen's
// own End Sync button.
func showInactivitySyncPrompt(s *state, onContinue func(), onEnd func()) {
	var d dialog.Dialog
	endBtn := widget.NewButton("End Sync", func() {
		d.Hide()
		onEnd()
	})
	continueBtn := widget.NewButton("Continue Sync", func() {
		d.Hide()
		onContinue()
	})
	continueBtn.Importance = widget.HighImportance
	d = dialog.NewCustomWithoutButtons("Sync paused due to inactivity",
		container.NewVBox(
			widget.NewLabel("No new recorders have been added in the last 5 minutes."),
			container.NewHBox(endBtn, continueBtn),
		), s.win)
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

// containsLocation reports whether loc appears in locs, by ID.
func containsLocation(locs []syncengine.Location, loc syncengine.Location) bool {
	for _, l := range locs {
		if l.ID == loc.ID {
			return true
		}
	}
	return false
}

// selectedFromIDs converts a set of persisted Location IDs into the
// matching Location Names, for pre-populating a toggleGroup's selection
// from RecorderSettings.
func selectedFromIDs(locs []syncengine.Location, ids []string) []string {
	var out []string
	for _, id := range ids {
		if loc := findLocationByID(locs, id); loc != nil {
			out = append(out, loc.Name)
		}
	}
	return out
}

// locationsFromNames resolves a CheckGroup's selected Names back into
// Locations of the given kind.
func locationsFromNames(locs []syncengine.Location, names []string, kind syncengine.LocationKind) []syncengine.Location {
	var out []syncengine.Location
	for _, name := range names {
		if loc := findLocation(locs, name); loc != nil && loc.Kind == kind {
			out = append(out, *loc)
		}
	}
	return out
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

func idsFromLocations(locs []syncengine.Location) []string {
	ids := make([]string, len(locs))
	for i, l := range locs {
		ids[i] = l.ID
	}
	return ids
}

// recorderJobStatus is the row-level lifecycle state shown on Screen 2,
// distinct from recorder.OffloadStatus so the UI can represent states
// (Idle, Disconnected) that don't exist on the offload side.
type recorderJobStatus int

const (
	jobIdle recorderJobStatus = iota
	recorderJobSyncing
	jobConflict
	jobError
	jobDone
	jobDisconnected
)

// recorderRow is one attached (recognized) recorder's live UI state.
// Unrecognized volumes never get a row at all — see the VolumeAttached
// handling in showRecorderSync.
type recorderRow struct {
	volume    recorder.Volume
	driver    recorder.Driver
	id        string
	job       *recorder.OffloadJob
	status    recorderJobStatus
	statusMsg string
	progress  float64
	started   bool // a job was ever started for this row
	done      bool
}

// rowStatusText is the default display label for a state, used whenever a
// row doesn't have a more specific statusMsg set.
func rowStatusText(st recorderJobStatus) string {
	switch st {
	case jobIdle:
		return "Idle"
	case recorderJobSyncing:
		return "Syncing"
	case jobConflict:
		return "Conflict"
	case jobError:
		return "Error"
	case jobDone:
		return "Done"
	case jobDisconnected:
		return "Disconnected"
	default:
		return ""
	}
}

// rowStatusMessage is what's actually shown in the status column: row's own
// statusMsg if it set one (e.g. an error detail, or "Done (no files)"),
// otherwise the state's default label.
func rowStatusMessage(row *recorderRow) string {
	if row.statusMsg != "" {
		return row.statusMsg
	}
	return rowStatusText(row.status)
}

// rowBackgroundColor matches the reference (Python/tkinter) implementation's
// status palette: syncing = light teal, conflict = orange, error = red,
// done = blue, disconnected = pink, idle = untinted. blinkOn alternates the
// jobDone color between two shades so finished rows draw the eye.
func rowBackgroundColor(st recorderJobStatus, blinkOn bool) (r, g, b, a uint8) {
	switch st {
	case recorderJobSyncing:
		return 0xC1, 0xDB, 0xD9, 0xFF
	case jobConflict:
		return 0xE0, 0x7B, 0x4A, 0xFF
	case jobError:
		return 0xF0, 0x9B, 0x97, 0xFF
	case jobDone:
		if blinkOn {
			return 0x4A, 0x9D, 0xE0, 0xFF
		}
		return 0xAE, 0xD3, 0xF2, 0xFF
	case jobDisconnected:
		return 0xFF, 0xAD, 0xED, 0xFF
	default: // jobIdle
		return 0, 0, 0, 0
	}
}

// recorderRowLayout gives the recorder ID column a fixed share of the row's
// width (idColRatio), with a floor (minIDColWidth) so it doesn't get
// squeezed to nothing as the window narrows. The remaining width goes to
// the status/progress column.
type recorderRowLayout struct{}

const (
	idColRatio    = 0.15
	minIDColWidth = 90
)

func (recorderRowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) < 2 {
		return fyne.NewSize(0, 0)
	}
	idMin := objects[0].MinSize()
	restMin := objects[1].MinSize()
	h := idMin.Height
	if restMin.Height > h {
		h = restMin.Height
	}
	w := idMin.Width
	if w < minIDColWidth {
		w = minIDColWidth
	}
	return fyne.NewSize(w+restMin.Width, h)
}

func (recorderRowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) < 2 {
		return
	}
	idCol, rest := objects[0], objects[1]
	idWidth := size.Width * idColRatio
	if idWidth < minIDColWidth {
		idWidth = minIDColWidth
	}
	if idWidth > size.Width {
		idWidth = size.Width
	}
	idCol.Move(fyne.NewPos(0, 0))
	idCol.Resize(fyne.NewSize(idWidth, size.Height))
	rest.Move(fyne.NewPos(idWidth, 0))
	rest.Resize(fyne.NewSize(size.Width-idWidth, size.Height))
}

// uploadFileEntry is one file's cloud-upload state, shown grouped by
// recorder ID in the split upload panel.
type uploadFileEntry struct {
	recorderID string
	relPath    string
	bytesDone  int64
	bytesTotal int64
	err        error // set when the upload failed after retries
}

func (e uploadFileEntry) label() string {
	return e.relPath
}

// showRecorderSync is the active-sync screen (Screen 2): the redesigned
// version of what used to be the whole recorders screen. Every setting
// (destinations, upload destinations, experiment name, auto-delete) is
// locked in from params for the duration of this screen; there are no
// settings controls here. Offload starts automatically the instant a
// recognized recorder attaches.
func showRecorderSync(s *state, params recorderSyncParams) {
	watchCtx, cancelWatch := context.WithCancel(context.Background())

	var rows []*recorderRow
	var rowsBox *fyne.Container

	var uploading []uploadFileEntry
	var uploaded []uploadFileEntry
	var uploadingList, uploadedList *widget.List

	destRoots := make([]string, len(params.destinations))
	for i, d := range params.destinations {
		destRoots[i] = d.RootPath
	}

	findRow := func(mountPoint string) (*recorderRow, int) {
		for i, r := range rows {
			if r.volume.MountPoint == mountPoint {
				return r, i
			}
		}
		return nil, -1
	}

	// findDisconnectedRow looks up a still-tracked row by the recorder's own
	// persistent identity (RecorderID + driver model), not by OS mount
	// point. Mount points are assigned by the OS and get reused across
	// unrelated physical devices - e.g. two recorders offloaded one after
	// another through the same USB hub/card reader slot. Matching a
	// reconnect on mount point alone (the old behavior) could silently
	// treat a brand new recorder as a "reconnect" of whatever row last held
	// that mount point, offloading its files under the wrong recorder ID.
	// Only jobDisconnected rows are eligible: any row whose device is still
	// attached, or whose job already finished/never started, has already
	// been resolved by the attach/detach handlers below and is not a valid
	// reconnect target.
	findDisconnectedRow := func(driver recorder.Driver, id string) *recorderRow {
		for _, r := range rows {
			if r.status == jobDisconnected && r.id == id && r.driver != nil && r.driver.Name() == driver.Name() {
				return r
			}
		}
		return nil
	}

	// rowRenderer holds the persistent widgets for one row's container, so
	// we can update in place without a widget.List's tap-highlight
	// behavior (rows here are never selectable).
	type rowRenderer struct {
		row       *recorderRow
		cell      fyne.CanvasObject
		bg        *canvas.Rectangle
		idText    *widget.RichText
		statusLbl *widget.Label
		bar       *widget.ProgressBar
		errBtn    *widget.Button
	}
	var renderers []*rowRenderer
	var blinkOn = true

	refreshRow := func(rr *rowRenderer) {
		label := rr.row.id
		if label == "" {
			label = rr.row.volume.MountPoint
		}
		rr.idText.Segments[0].(*widget.TextSegment).Text = label
		rr.idText.Refresh()
		rr.statusLbl.SetText(rowStatusMessage(rr.row))
		rr.bar.SetValue(rr.row.progress)
		r, g, b, a := rowBackgroundColor(rr.row.status, blinkOn)
		rr.bg.FillColor = colorRGBA(r, g, b, a)
		rr.bg.Refresh()
		if rr.row.status == jobError && rr.row.statusMsg != "" {
			errText := rr.row.statusMsg
			rr.errBtn.OnTapped = func() { showErrorModal(s.win, errText) }
			rr.errBtn.Show()
		} else {
			rr.errBtn.OnTapped = nil
			rr.errBtn.Hide()
		}
	}

	// sortByPlugOrder, toggled via the "Sort: ..." button in the local
	// sync panel's header, switches sortRows between the default
	// finished-first/by-progress order and plain plug-in (attachment)
	// order - new rows are always appended to rows in attach order, and
	// reconnects update the existing row in place, so leaving rows
	// untouched here is enough to preserve that order.
	var sortByPlugOrder bool

	// sortRows normally puts finished (jobDone) recorders in the top rows,
	// then orders the rest by descending progress (closest to finishing
	// next), keeping relative order stable among otherwise-equal rows. When
	// sortByPlugOrder is set it does nothing, leaving rows in attach order.
	sortRows := func() {
		if sortByPlugOrder {
			return
		}
		sort.SliceStable(rows, func(i, j int) bool {
			iDone := rows[i].status == jobDone
			jDone := rows[j].status == jobDone
			if iDone != jDone {
				return iDone
			}
			if iDone {
				return false
			}
			return rows[i].progress > rows[j].progress
		})
	}

	// reorderRowsBox re-sorts rows and reassembles rowsBox from the existing
	// renderers/cells (no widget recreation), so it's cheap enough to call
	// on every status change.
	reorderRowsBox := func() {
		sortRows()
		rendererFor := make(map[*recorderRow]*rowRenderer, len(renderers))
		for _, rr := range renderers {
			rendererFor[rr.row] = rr
		}
		newRenderers := make([]*rowRenderer, 0, len(rows))
		objs := make([]fyne.CanvasObject, 0, len(rows))
		for _, row := range rows {
			rr := rendererFor[row]
			if rr == nil {
				continue
			}
			newRenderers = append(newRenderers, rr)
			objs = append(objs, rr.cell)
		}
		renderers = newRenderers
		rowsBox.Objects = objs
		rowsBox.Refresh()
	}

	// recordersIdle mirrors "no recorders are actively syncing" (every row
	// is jobDone, or there are no rows at all) into a value the inactivity
	// timer goroutine below can read without racing on rows itself, since
	// rows is otherwise only ever touched from the fyne UI thread.
	var recordersIdle atomic.Bool
	recordersIdle.Store(true)
	updateRecordersIdle := func() {
		idle := true
		for _, r := range rows {
			if r.status != jobDone {
				idle = false
				break
			}
		}
		recordersIdle.Store(idle)
	}

	rebuildRows := func() {
		sortRows()
		updateRecordersIdle()
		renderers = renderers[:0]
		objs := make([]fyne.CanvasObject, 0, len(rows))
		for _, row := range rows {
			row := row
			idText := widget.NewRichTextWithText("")
			idText.Wrapping = fyne.TextWrapOff
			idText.Truncation = fyne.TextTruncateEllipsis
			idText.Segments[0].(*widget.TextSegment).Style = widget.RichTextStyle{
				SizeName:  theme.SizeNameSubHeadingText,
				TextStyle: fyne.TextStyle{Bold: true},
			}
			idCol := container.NewVBox(idText)
			statusLbl := widget.NewLabel("")
			bar := widget.NewProgressBar()
			errBtn := widget.NewButtonWithIcon("", theme.ErrorIcon(), nil)
			errBtn.Importance = widget.DangerImportance
			errBtn.Hide()
			trailing := container.NewHBox(errBtn, statusLbl)
			rest := container.NewVBox(container.NewBorder(nil, nil, nil, trailing), bar)
			content := container.New(recorderRowLayout{}, idCol, rest)
			bg := canvas.NewRectangle(colorRGBA(0, 0, 0, 0))
			cell := container.NewStack(bg, container.NewPadded(content))
			rr := &rowRenderer{row: row, cell: cell, bg: bg, idText: idText, statusLbl: statusLbl, bar: bar, errBtn: errBtn}
			refreshRow(rr)
			renderers = append(renderers, rr)
			objs = append(objs, cell)
		}
		rowsBox.Objects = objs
		rowsBox.Refresh()
	}

	refreshAllRows := func() {
		for _, rr := range renderers {
			refreshRow(rr)
		}
		updateRecordersIdle()
		reorderRowsBox()
	}

	go func() {
		ticker := time.NewTicker(700 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				fyne.Do(func() {
					blinkOn = !blinkOn
					for _, rr := range renderers {
						if rr.row.status == jobDone {
							refreshRow(rr)
						}
					}
				})
			}
		}
	}()

	onUploadEvent := func(u recorder.UploadUpdate) {
		fyne.Do(func() {
			switch u.Event {
			case syncengine.UploadQueued:
				// Added to the queue as soon as the file is offloaded and
				// verified, even though the upload itself may not start yet
				// (see uploadSem in offload.go) - previously the list only
				// picked files up once a slot freed, so it silently topped
				// out at maxConcurrentUploads entries no matter how many
				// files were actually waiting.
				uploading = append(uploading, uploadFileEntry{
					recorderID: u.RecorderID, relPath: u.RelPath,
					bytesTotal: u.BytesTotal,
				})
			case syncengine.UploadStarted:
				// Entry already added at UploadQueued; nothing to do.
			case syncengine.UploadProgress:
				if i := findUploadEntry(uploading, u.RecorderID, u.RelPath); i >= 0 {
					uploading[i].bytesDone = u.BytesDone
					uploading[i].bytesTotal = u.BytesTotal
				}
			case syncengine.UploadDone:
				uploading = removeUploadEntry(uploading, u.RecorderID, u.RelPath)
				uploaded = append(uploaded, uploadFileEntry{
					recorderID: u.RecorderID, relPath: u.RelPath,
					bytesDone: u.BytesTotal, bytesTotal: u.BytesTotal,
				})
			case syncengine.UploadFailed:
				uploading = removeUploadEntry(uploading, u.RecorderID, u.RelPath)
				// Surface the failure in the "Uploaded" list (flagged red,
				// with the error detail) instead of letting it vanish
				// silently - previously a failed upload disappeared from
				// both lists with no indication to the user.
				uploaded = append(uploaded, uploadFileEntry{
					recorderID: u.RecorderID, relPath: u.RelPath,
					bytesTotal: u.BytesTotal, err: u.Err,
				})
			}
			if uploadingList != nil {
				uploadingList.Refresh()
				uploadedList.Refresh()
			}
		})
	}

	beginOffload := func(row *recorderRow) {
		row.started = true
		row.status = recorderJobSyncing
		row.statusMsg = ""
		job, progress := recorder.StartOffload(watchCtx, row.driver, row.volume, row.id, destRoots, params.subpath,
			params.experimentName, params.uploads, params.autoDelete, onUploadEvent)
		row.job = job
		rebuildRows()

		go func() {
			for p := range progress {
				p := p
				fyne.Do(func() {
					switch p.Status {
					case recorder.OffloadDone:
						row.status = jobDone
						row.done = true
						row.progress = 1
						if p.FilesTotal == 0 {
							row.statusMsg = "Done (no files)"
						}
					case recorder.OffloadConflict:
						row.status = jobConflict
					case recorder.OffloadError:
						row.status = jobError
						row.statusMsg = errString(p.Err)
					case recorder.OffloadCanceled:
						// A cancel is expected and races with the detach
						// handler's own jobDisconnected assignment (see
						// VolumeDetached above, which calls job.Cancel()) -
						// don't clobber that with jobError just because this
						// event happened to arrive after it.
						if row.status != jobDisconnected {
							row.status = jobError
							row.statusMsg = errString(p.Err)
						}
					default:
						row.status = recorderJobSyncing
						if p.BytesTotal > 0 {
							row.progress = float64(p.BytesDone) / float64(p.BytesTotal)
						}
						row.statusMsg = ""
						if p.CurrentFile != "" {
							phase := p.Phase
							if phase == "" {
								phase = "syncing"
							}
							row.statusMsg = fmt.Sprintf("%s%s: %s", strings.ToUpper(phase[:1]), phase[1:], p.CurrentFile)
						}
					}
					refreshAllRows()
				})
			}
		}()
	}

	endSync := func() {
		cancelWatch()
		showHome(s)
	}

	// resetInactivity signals the inactivity-timer goroutine below that a
	// new recorder was attached (or the user chose to keep waiting), so the
	// 5-minute countdown restarts.
	resetInactivity := make(chan struct{}, 1)
	signalActivity := func() {
		select {
		case resetInactivity <- struct{}{}:
		default:
		}
	}

	go func() {
		// pollInterval checks recordersIdle far more often than the timeout
		// itself fires, so the countdown starts promptly once the last
		// active recorder finishes (or is removed) rather than only on the
		// next explicit signalActivity call.
		const pollInterval = 2 * time.Second
		poll := time.NewTicker(pollInterval)
		defer poll.Stop()

		var timer *time.Timer
		var timerC <-chan time.Time
		stopTimer := func() {
			if timer != nil {
				timer.Stop()
				timer = nil
				timerC = nil
			}
		}
		restartTimer := func() {
			stopTimer()
			timer = time.NewTimer(recorderInactivityTimeout)
			timerC = timer.C
		}

		running := false
		for {
			select {
			case <-watchCtx.Done():
				stopTimer()
				return
			case <-resetInactivity:
				// A recorder was attached or removed, or the user chose to
				// keep waiting: restart the countdown only if it's actually
				// applicable (nothing left actively syncing); otherwise make
				// sure it stays off until things go idle again.
				if recordersIdle.Load() {
					restartTimer()
					running = true
				} else {
					stopTimer()
					running = false
				}
			case <-poll.C:
				idle := recordersIdle.Load()
				if idle && !running {
					restartTimer()
					running = true
				} else if !idle && running {
					stopTimer()
					running = false
				}
			case <-timerC:
				stopTimer()
				running = false
				fyne.Do(func() {
					showInactivitySyncPrompt(s, signalActivity, endSync)
				})
			}
		}
	}()

	go func() {
		for ev := range recorder.WatchVolumes(watchCtx, time.Second) {
			ev := ev
			switch ev.Type {
			case recorder.VolumeAttached:
				signalActivity()
				driver := recorder.Detect(ev.Volume)
				if driver == nil {
					// Unrecognized volumes are ignored entirely - never
					// shown as a row.
					continue
				}
				row := &recorderRow{volume: ev.Volume, driver: driver, status: jobIdle}
				id, err := driver.RecorderID(ev.Volume)
				if err != nil {
					row.status = jobError
					row.statusMsg = errString(err)
				} else {
					row.id = id
				}
				fyne.Do(func() {
					if err == nil {
						if existing := findDisconnectedRow(driver, id); existing != nil {
							// Reconnect of a still-tracked, previously
							// disconnected row, confirmed by the recorder's
							// own ID (not just the mount point it landed
							// on): resume its job rather than duplicating
							// it.
							existing.volume = ev.Volume
							existing.driver = driver
							beginOffload(existing)
							rebuildRows()
							return
						}
					}
					rows = append(rows, row)
					rebuildRows()
					if row.status != jobError {
						beginOffload(row)
					}
				})
			case recorder.VolumeDetached:
				fyne.Do(func() {
					row, i := findRow(ev.Volume.MountPoint)
					if i < 0 {
						return
					}
					switch {
					case row.done:
						rows = append(rows[:i], rows[i+1:]...)
					case !row.started:
						rows = append(rows[:i], rows[i+1:]...)
					default:
						// Cancel the in-flight job rather than leaving it
						// running against a mount point that may be
						// reassigned to a different physical recorder any
						// time after this point (e.g. a jostled hub, or
						// another recorder plugged into the same slot).
						// StartOffload also re-verifies the recorder's own
						// ID before each file as a second layer of defense,
						// but this is what makes that abandonment prompt
						// instead of racing.
						if row.job != nil {
							row.job.Cancel()
						}
						row.status = jobDisconnected
						row.statusMsg = ""
					}
					rebuildRows()
					signalActivity()
				})
			}
		}
	}()

	// confirmEndSync warns before ending the session if any row is actively
	// mid-transfer (recorderJobSyncing), since ending the sync cancels that
	// job in progress rather than merely closing the screen. Other states
	// (idle, done, error, disconnected) end silently, as before.
	confirmEndSync := func() {
		syncing := false
		for _, r := range rows {
			if r.status == recorderJobSyncing {
				syncing = true
				break
			}
		}
		if !syncing {
			endSync()
			return
		}
		dialog.NewConfirm("Recorder still syncing",
			"At least one recorder is still syncing. Ending now will cancel its transfer.\n\nEnd sync anyway?",
			func(ok bool) {
				if ok {
					endSync()
				}
			}, s.win).Show()
	}

	cancelBtn := widget.NewButton("End Sync", confirmEndSync)

	rowsBox = container.NewVBox()

	uploadingList = widget.NewList(
		func() int { return len(uploading) },
		func() fyne.CanvasObject { return createBackingBarItem() },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			e := uploading[id]
			prog := 0.0
			if e.bytesTotal > 0 {
				prog = float64(e.bytesDone) / float64(e.bytesTotal)
			}
			summary := fmt.Sprintf("%s / %s", humanBytes(e.bytesDone), humanBytes(e.bytesTotal))
			updateBackingBarItem(obj, e.label(), summary, prog, nil, false, false, false, s.win)
		},
	)
	uploadedList = widget.NewList(
		func() int { return len(uploaded) },
		func() fyne.CanvasObject { return createBackingBarItem() },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			e := uploaded[id]
			summary := humanBytes(e.bytesTotal)
			if e.err != nil {
				summary = "Failed"
			}
			updateBackingBarItem(obj, e.label(), summary, 1.0, e.err, e.err != nil, false, false, s.win)
		},
	)

	rowsScroll := container.NewVScroll(rowsBox)

	var sortToggleBtn *widget.Button
	sortToggleLabel := func() string {
		if sortByPlugOrder {
			return "Sort: Plug-in order"
		}
		return "Sort: Progress"
	}
	sortToggleBtn = widget.NewButton(sortToggleLabel(), nil)
	sortToggleBtn.OnTapped = func() {
		sortByPlugOrder = !sortByPlugOrder
		sortToggleBtn.SetText(sortToggleLabel())
		reorderRowsBox()
	}
	localHeader := container.NewBorder(nil, nil, nil, sortToggleBtn, sectionHeader("Local Sync"))
	localPanel := container.NewBorder(localHeader, nil, nil, nil, rowsScroll)

	var main fyne.CanvasObject = localPanel
	if len(params.uploads) > 0 {
		uploadPanel := container.NewVSplit(
			container.NewBorder(sectionHeader("Upload queue"), nil, nil, nil, uploadingList),
			container.NewBorder(sectionHeader("Uploaded"), nil, nil, nil, uploadedList),
		)
		uploadPanel.SetOffset(0.5)
		main = container.NewHSplit(localPanel, uploadPanel)
	}

	identParts := append([]string{params.experimentName}, splitSubpathUI(params.subpath)...)
	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Syncing to: "+strings.Join(identParts, "/"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
		),
		cancelBtn,
		nil, nil,
		main,
	)
	s.setContent(container.NewPadded(content))
}

func findUploadEntry(list []uploadFileEntry, recorderID, relPath string) int {
	for i, x := range list {
		if x.recorderID == recorderID && x.relPath == relPath {
			return i
		}
	}
	return -1
}

func removeUploadEntry(list []uploadFileEntry, recorderID, relPath string) []uploadFileEntry {
	if i := findUploadEntry(list, recorderID, relPath); i >= 0 {
		return append(list[:i], list[i+1:]...)
	}
	return list
}
