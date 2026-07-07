package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/recorder"
	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

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
// see Location.Enabled - clears destSelect, and dismisses the prompt so the
// user can pick a different destination. "Reconnect" re-runs the same
// presence check (for after the drive has been plugged back in): if
// everything now resolves it dismisses the prompt and runs onFound; if
// something's still missing it updates the message in place naming what's
// still absent, rather than closing.
func showLocationsNotFoundPrompt(s *state, destSelect *widget.Select, missing []syncengine.Location, onFound func()) {
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
		destSelect.Options = locationNamesByKind(s.cfg.Locations, syncengine.LocationLocal)
		destSelect.ClearSelected()
		destSelect.Refresh()
		d.Hide()
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

// recorderRow is one attached device's live UI state. A device with no
// matching Driver is still shown (as "Unrecognized"), with no action
// available on it, so the list always reflects everything the OS has
// mounted.
type recorderRow struct {
	volume   recorder.Volume
	driver   recorder.Driver
	id       string
	job      *recorder.OffloadJob
	status   string
	progress float64
	isError  bool
	done     bool
}

// showRecorders is the recorder-offload screen: watches for attached
// recorders (no hub/port position involved — see internal/recorder),
// assigns/reads each one's persistent tag-file ID, and offloads its files
// into destRoot/experimentName/recorderID/... with a verified, resumable
// copy. Completed files are queued for upload to the optional cloud
// destination as they land, rather than waiting for a whole recorder or
// session to finish.
func showRecorders(s *state) {
	watchCtx, cancelWatch := context.WithCancel(context.Background())

	destSelect := widget.NewSelect(locationNamesByKind(s.cfg.Locations, syncengine.LocationLocal), nil)
	if loc := findLocationByID(s.cfg.Locations, s.cfg.RecorderSettings.DestinationLocationID); loc != nil && loc.Enabled {
		destSelect.Selected = loc.Name
	}

	uploadOptions := append([]string{"(none)"}, locationNamesByKind(s.cfg.Locations, syncengine.LocationRemote)...)
	uploadSelect := widget.NewSelect(uploadOptions, nil)
	uploadSelect.Selected = "(none)"

	expEntry := widget.NewEntry()
	expEntry.SetPlaceHolder("Experiment name")

	autoDeleteCheck := widget.NewCheck("Delete from recorder after verified copy", nil)
	autoDeleteCheck.SetChecked(s.cfg.RecorderSettings.AutoDeleteAfterVerify)
	autoDeleteCheck.OnChanged = func(v bool) {
		s.cfg.RecorderSettings.AutoDeleteAfterVerify = v
		s.saveConfig()
	}

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
	var list *widget.List

	currentDest := func() *syncengine.Location {
		return findLocation(s.cfg.Locations, destSelect.Selected)
	}
	currentUploadDest := func() *syncengine.Location {
		if uploadSelect.Selected == "" || uploadSelect.Selected == "(none)" {
			return nil
		}
		return findLocation(s.cfg.Locations, uploadSelect.Selected)
	}

	beginOffload := func(row *recorderRow, dest syncengine.Location) {
		destRoot := dest.RootPath
		experimentName := expEntry.Text
		uploadDest := currentUploadDest()
		autoDelete := autoDeleteCheck.Checked

		job, progress := recorder.StartOffload(watchCtx, row.driver, row.volume, row.id, destRoot, experimentName, uploadDest, autoDelete)
		row.job = job
		row.status = "Starting..."
		list.Refresh()

		go func() {
			for p := range progress {
				p := p
				fyne.Do(func() {
					switch p.Status {
					case recorder.OffloadDone:
						row.status = "Done"
						row.done = true
						row.isError = false
						row.progress = 1
					case recorder.OffloadError:
						row.status = fmt.Sprintf("Error: %s", errString(p.Err))
						row.isError = true
					case recorder.OffloadCanceled:
						row.status = "Canceled"
						row.isError = true
					default:
						row.status = fmt.Sprintf("Copying %d/%d files (%s)", p.FilesDone, p.FilesTotal, humanBytes(p.BytesDone))
						if p.BytesTotal > 0 {
							row.progress = float64(p.BytesDone) / float64(p.BytesTotal)
						}
					}
					list.Refresh()
				})
			}
		}()
	}

	startRow := func(row *recorderRow) {
		dest := currentDest()
		if dest == nil || expEntry.Text == "" || row.driver == nil || row.job != nil {
			return
		}
		s.cfg.RecorderSettings.DestinationLocationID = dest.ID
		s.saveConfig()

		if missing := missingLocalLocations(*dest); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, destSelect, missing, func() {
				beginOffload(row, *dest)
			})
			return
		}
		beginOffload(row, *dest)
	}

	list = widget.NewList(
		func() int { return len(rows) },
		func() fyne.CanvasObject {
			idLabel := widget.NewLabel("")
			idLabel.Truncation = fyne.TextTruncateEllipsis
			statusLabel := widget.NewLabel("")
			bar := widget.NewProgressBar()
			btn := widget.NewButton("Offload", nil)
			center := container.NewVBox(statusLabel, bar)
			return container.NewBorder(nil, nil, idLabel, btn, center)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			row := rows[id]
			border := obj.(*fyne.Container)
			center := border.Objects[0].(*fyne.Container)
			idLabel := border.Objects[1].(*widget.Label)
			btn := border.Objects[2].(*widget.Button)
			statusLabel := center.Objects[0].(*widget.Label)
			bar := center.Objects[1].(*widget.ProgressBar)

			label := row.id
			if row.driver == nil {
				label = "Unrecognized device"
			}
			idLabel.SetText(fmt.Sprintf("%s\n%s", label, row.volume.MountPoint))
			statusLabel.SetText(row.status)
			bar.SetValue(row.progress)

			btn.OnTapped = func() { startRow(row); list.Refresh() }
			if row.driver == nil || row.job != nil || currentDest() == nil || expEntry.Text == "" {
				btn.Disable()
			} else {
				btn.Enable()
			}
		},
	)

	findRow := func(mountPoint string) (*recorderRow, int) {
		for i, r := range rows {
			if r.volume.MountPoint == mountPoint {
				return r, i
			}
		}
		return nil, -1
	}

	go func() {
		for ev := range recorder.WatchVolumes(watchCtx, time.Second) {
			ev := ev
			switch ev.Type {
			case recorder.VolumeAttached:
				driver := recorder.Detect(ev.Volume)
				row := &recorderRow{volume: ev.Volume, driver: driver, status: "Idle"}
				if driver != nil {
					id, isNew, err := recorder.AssignOrReadID(driver, ev.Volume, tagState)
					if err != nil {
						row.status = fmt.Sprintf("Error reading ID: %s", err)
						row.isError = true
					} else {
						row.id = id
						fyne.Do(func() {
							if isNew {
								saveTagState()
							}
						})
					}
				}
				fyne.Do(func() {
					if existing, _ := findRow(ev.Volume.MountPoint); existing != nil {
						return
					}
					rows = append(rows, row)
					list.Refresh()
				})
			case recorder.VolumeDetached:
				fyne.Do(func() {
					if _, i := findRow(ev.Volume.MountPoint); i >= 0 {
						rows = append(rows[:i], rows[i+1:]...)
						list.Refresh()
					}
				})
			}
		}
	}()

	backBtn := widget.NewButton("Back", func() {
		cancelWatch()
		showHome(s)
	})

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Recorders", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewForm(
				&widget.FormItem{Text: "Destination", Widget: destSelect},
				&widget.FormItem{Text: "Cloud upload", Widget: uploadSelect},
				&widget.FormItem{Text: "Experiment", Widget: expEntry},
				&widget.FormItem{Text: "", Widget: autoDeleteCheck},
			),
			widget.NewSeparator(),
		),
		backBtn,
		nil, nil,
		container.NewVScroll(list),
	)
	s.setContent(container.NewPadded(content))
}
