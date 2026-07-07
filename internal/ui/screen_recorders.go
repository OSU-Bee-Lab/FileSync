package ui

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/recorder"
	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

// colorRGBA builds an image/color.NRGBA, used for recorderRow status
// background tints (see rowBackgroundColor).
func colorRGBA(r, g, b, a uint8) color.Color {
	return color.NRGBA{R: r, G: g, B: b, A: a}
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
// disk. "Cancel" (the dialog's built-in dismiss button) simply aborts the
// offload attempt. "Disable and continue" marks the missing location(s)
// disabled - so they stop being offered anywhere a Location is picked,
// see Location.Enabled - and dismisses the prompt. "Reconnect" re-runs the
// same presence check (for after the drive has been plugged back in): if
// everything now resolves it dismisses the prompt and runs onFound; if
// something's still missing it updates the message in place naming what's
// still absent, rather than closing.
func showLocationsNotFoundPrompt(s *state, missing []syncengine.Location, onDisable func(), onFound func()) {
	msgLabel := widget.NewLabel("")
	msgLabel.Wrapping = fyne.TextWrapWord
	setMsg := func(missing []syncengine.Location) {
		names := make([]string, len(missing))
		for i, l := range missing {
			names[i] = l.Name
		}
		msgLabel.SetText("Location(s) not found:\n\n" + strings.Join(names, "\n") +
			"\n\nPlug in the missing drive(s) and press Reconnect, disable them to continue without them, or cancel.")
	}
	setMsg(missing)

	var d dialog.Dialog
	disableBtn := widget.NewButton("Disable and continue", func() {
		for _, l := range missing {
			if loc := findLocationByID(s.cfg.Locations, l.ID); loc != nil {
				loc.Enabled = false
			}
		}
		s.saveConfig()
		d.Hide()
		if onDisable != nil {
			onDisable()
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
		container.NewVBox(msgLabel, container.NewHBox(disableBtn, reconnectBtn)), s.win)
	d.Show()
}

// recorderSyncParams is the locked-in configuration chosen on the
// recorder-settings screen (Screen 1) and handed to the active-sync
// screen (Screen 2). Nothing on Screen 2 can change these; to change
// anything the user must Cancel Sync back to Screen 1 and start over.
type recorderSyncParams struct {
	destinations   []syncengine.Location // local, at least one
	uploads        []syncengine.Location // remote, may be empty (no cloud upload)
	experimentName string
	autoDelete     bool
}

// showRecorders is the entry point for the recorder-offload feature: the
// settings screen (Screen 1) shown before any sync activity starts.
func showRecorders(s *state) {
	localOptions := locationNamesByKind(s.cfg.Locations, syncengine.LocationLocal)
	destGroup := widget.NewCheckGroup(localOptions, nil)
	destGroup.Selected = selectedFromIDs(s.cfg.Locations, s.cfg.RecorderSettings.DestinationLocationIDs, syncengine.LocationLocal)

	remoteOptions := locationNamesByKind(s.cfg.Locations, syncengine.LocationRemote)
	uploadGroup := widget.NewCheckGroup(remoteOptions, nil)
	uploadGroup.Selected = selectedFromIDs(s.cfg.Locations, s.cfg.RecorderSettings.UploadLocationIDs, syncengine.LocationRemote)

	expEntry := widget.NewEntry()
	expEntry.SetPlaceHolder("Experiment name")

	autoDeleteCheck := widget.NewCheck("Delete from recorder after verified copy", nil)
	autoDeleteCheck.SetChecked(s.cfg.RecorderSettings.AutoDeleteAfterVerify)

	startBtn := widget.NewButton("Start Sync", nil)
	startBtn.Importance = widget.HighImportance

	updateStartEnabled := func() {
		if len(destGroup.Selected) > 0 && strings.TrimSpace(expEntry.Text) != "" {
			startBtn.Enable()
		} else {
			startBtn.Disable()
		}
	}
	destGroup.OnChanged = func([]string) { updateStartEnabled() }
	expEntry.OnChanged = func(string) { updateStartEnabled() }
	updateStartEnabled()

	startBtn.OnTapped = func() {
		destinations := locationsFromNames(s.cfg.Locations, destGroup.Selected, syncengine.LocationLocal)
		uploads := locationsFromNames(s.cfg.Locations, uploadGroup.Selected, syncengine.LocationRemote)

		s.cfg.RecorderSettings.DestinationLocationIDs = idsFromLocations(destinations)
		s.cfg.RecorderSettings.UploadLocationIDs = idsFromLocations(uploads)
		s.cfg.RecorderSettings.AutoDeleteAfterVerify = autoDeleteCheck.Checked
		s.saveConfig()

		params := recorderSyncParams{
			destinations:   destinations,
			uploads:        uploads,
			experimentName: strings.TrimSpace(expEntry.Text),
			autoDelete:     autoDeleteCheck.Checked,
		}

		if missing := missingLocalLocations(destinations...); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func() {
				// Locations were disabled; re-show settings so the user
				// picks new ones.
				showRecorders(s)
			}, func() {
				showRecorderSync(s, params)
			})
			return
		}
		showRecorderSync(s, params)
	}

	backBtn := widget.NewButton("Cancel", func() { showHome(s) })

	content := container.NewVBox(
		widget.NewLabelWithStyle("Recorders", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewForm(
			&widget.FormItem{Text: "Destination(s)", Widget: destGroup},
			&widget.FormItem{Text: "Cloud upload(s)", Widget: uploadGroup},
			&widget.FormItem{Text: "Experiment", Widget: expEntry},
			&widget.FormItem{Text: "", Widget: autoDeleteCheck},
		),
		widget.NewSeparator(),
		container.NewHBox(backBtn, startBtn),
	)
	s.setContent(container.NewPadded(content))
}

// selectedFromIDs converts a set of persisted Location IDs into the
// matching Location Names of the given kind, for pre-populating a
// CheckGroup's Selected field from RecorderSettings.
func selectedFromIDs(locs []syncengine.Location, ids []string, kind syncengine.LocationKind) []string {
	var out []string
	for _, id := range ids {
		if loc := findLocationByID(locs, id); loc != nil && loc.Enabled && loc.Kind == kind {
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
	jobSyncing
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

func rowStatusText(st recorderJobStatus) string {
	switch st {
	case jobIdle:
		return "Idle"
	case jobSyncing:
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

// rowBackgroundColor matches the reference (Python/tkinter) implementation's
// status palette: syncing = light teal, conflict = orange, error = red,
// done = blue, disconnected = pink, idle = untinted.
func rowBackgroundColor(st recorderJobStatus) (r, g, b, a uint8) {
	switch st {
	case jobSyncing:
		return 0xC1, 0xDB, 0xD9, 0xFF
	case jobConflict:
		return 0xE0, 0x7B, 0x4A, 0xFF
	case jobError:
		return 0xE0, 0x40, 0x3B, 0xFF
	case jobDone:
		return 0x4A, 0x9D, 0xE0, 0xFF
	case jobDisconnected:
		return 0xFF, 0xAD, 0xED, 0xFF
	default: // jobIdle
		return 0, 0, 0, 0
	}
}

// uploadFileEntry is one file's cloud-upload state, shown grouped by
// recorder ID in the split upload panel.
type uploadFileEntry struct {
	recorderID string
	relPath    string
}

// showRecorderSync is the active-sync screen (Screen 2): the redesigned
// version of what used to be the whole recorders screen. Every setting
// (destinations, upload destinations, experiment name, auto-delete) is
// locked in from params for the duration of this screen; there are no
// settings controls here. Offload starts automatically the instant a
// recognized recorder attaches.
func showRecorderSync(s *state, params recorderSyncParams) {
	watchCtx, cancelWatch := context.WithCancel(context.Background())

	tagState := &recorder.TagState{
		Batch:    s.cfg.RecorderSettings.TagBatch,
		Counters: s.cfg.RecorderSettings.TagCounters,
	}
	if tagState.Batch == 0 {
		tagState.Batch = 1
	}
	saveTagState := func() {
		s.cfg.RecorderSettings.TagBatch = tagState.Batch
		s.cfg.RecorderSettings.TagCounters = tagState.Counters
		s.saveConfig()
	}

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

	// rowRenderer holds the persistent widgets for one row's container, so
	// we can update in place without a widget.List's tap-highlight
	// behavior (rows here are never selectable).
	type rowRenderer struct {
		row       *recorderRow
		bg        *canvas.Rectangle
		idLabel   *widget.Label
		statusLbl *widget.Label
		bar       *widget.ProgressBar
	}
	var renderers []*rowRenderer

	refreshRow := func(rr *rowRenderer) {
		label := rr.row.id
		if label == "" {
			label = rr.row.volume.MountPoint
		}
		rr.idLabel.SetText(label)
		rr.statusLbl.SetText(rowStatusText(rr.row.status))
		rr.bar.SetValue(rr.row.progress)
		r, g, b, a := rowBackgroundColor(rr.row.status)
		rr.bg.FillColor = colorRGBA(r, g, b, a)
		rr.bg.Refresh()
	}

	rebuildRows := func() {
		renderers = renderers[:0]
		objs := make([]fyne.CanvasObject, 0, len(rows))
		for _, row := range rows {
			row := row
			idLabel := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			statusLbl := widget.NewLabel("")
			bar := widget.NewProgressBar()
			top := container.NewBorder(nil, nil, idLabel, statusLbl)
			content := container.NewVBox(top, bar)
			bg := canvas.NewRectangle(colorRGBA(0, 0, 0, 0))
			cell := container.NewStack(bg, container.NewPadded(content))
			rr := &rowRenderer{row: row, bg: bg, idLabel: idLabel, statusLbl: statusLbl, bar: bar}
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
	}

	onUploadEvent := func(u recorder.UploadUpdate) {
		fyne.Do(func() {
			entry := uploadFileEntry{recorderID: u.RecorderID, relPath: u.RelPath}
			switch u.Event {
			case syncengine.UploadStarted:
				uploading = append(uploading, entry)
			case syncengine.UploadDone:
				uploading = removeUploadEntry(uploading, entry)
				uploaded = append(uploaded, entry)
			case syncengine.UploadFailed:
				uploading = removeUploadEntry(uploading, entry)
			}
			if uploadingList != nil {
				uploadingList.Refresh()
				uploadedList.Refresh()
			}
		})
	}

	beginOffload := func(row *recorderRow) {
		row.started = true
		row.status = jobSyncing
		job, progress := recorder.StartOffload(watchCtx, row.driver, row.volume, row.id, destRoots,
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
					case recorder.OffloadConflict:
						row.status = jobConflict
					case recorder.OffloadError:
						row.status = jobError
					case recorder.OffloadCanceled:
						row.status = jobError
					default:
						row.status = jobSyncing
						if p.BytesTotal > 0 {
							row.progress = float64(p.BytesDone) / float64(p.BytesTotal)
						}
					}
					refreshAllRows()
				})
			}
		}()
	}

	go func() {
		for ev := range recorder.WatchVolumes(watchCtx, time.Second) {
			ev := ev
			switch ev.Type {
			case recorder.VolumeAttached:
				driver := recorder.Detect(ev.Volume)
				if driver == nil {
					// Unrecognized volumes are ignored entirely - never
					// shown as a row.
					continue
				}
				row := &recorderRow{volume: ev.Volume, driver: driver, status: jobIdle}
				id, isNew, err := recorder.AssignOrReadID(driver, ev.Volume, tagState)
				if err != nil {
					row.status = jobError
					row.statusMsg = errString(err)
				} else {
					row.id = id
				}
				fyne.Do(func() {
					if isNew {
						saveTagState()
					}
					if existing, _ := findRow(ev.Volume.MountPoint); existing != nil {
						// Reconnect of a still-tracked (e.g. previously
						// disconnected) row: resume its job rather than
						// duplicating it.
						existing.volume = ev.Volume
						if existing.status == jobDisconnected {
							beginOffload(existing)
						}
						rebuildRows()
						return
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
						row.status = jobDisconnected
					}
					rebuildRows()
				})
			}
		}
	}()

	cancelBtn := widget.NewButton("Cancel Sync", func() {
		cancelWatch()
		showHome(s)
	})

	rowsBox = container.NewVBox()

	uploadingList = widget.NewList(
		func() int { return len(uploading) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			e := uploading[id]
			obj.(*widget.Label).SetText(fmt.Sprintf("%s: %s", e.recorderID, e.relPath))
		},
	)
	uploadedList = widget.NewList(
		func() int { return len(uploaded) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			e := uploaded[id]
			obj.(*widget.Label).SetText(fmt.Sprintf("%s: %s", e.recorderID, e.relPath))
		},
	)

	rowsScroll := container.NewVScroll(rowsBox)

	var main fyne.CanvasObject = rowsScroll
	if len(params.uploads) > 0 {
		uploadPanel := container.NewHSplit(
			container.NewBorder(widget.NewLabelWithStyle("Currently uploading", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), nil, nil, nil, uploadingList),
			container.NewBorder(widget.NewLabelWithStyle("Uploaded", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), nil, nil, nil, uploadedList),
		)
		main = container.NewHSplit(rowsScroll, uploadPanel)
	}

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle(fmt.Sprintf("Experiment: %s", params.experimentName), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
		),
		cancelBtn,
		nil, nil,
		main,
	)
	s.setContent(container.NewPadded(content))
}

func removeUploadEntry(list []uploadFileEntry, e uploadFileEntry) []uploadFileEntry {
	for i, x := range list {
		if x == e {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}
