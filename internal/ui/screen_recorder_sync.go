package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
)

// rowRenderer holds the persistent widgets for one row's container, so we
// can update in place without a widget.List's tap-highlight behavior (rows
// here are never selectable).
type rowRenderer struct {
	row       *recorderRow
	cell      fyne.CanvasObject
	bg        *canvas.Rectangle
	idText    *widget.RichText
	statusLbl *widget.Label
	bar       *widget.ProgressBar
	errBtn    *widget.Button
}

// recorderSyncScreen holds all live state for Screen 2, the active-sync
// screen: the redesigned version of what used to be the whole recorders
// screen. Every setting (destinations, upload destinations, experiment
// name, auto-delete) is locked in from params for the duration of this
// screen; there are no settings controls here. Offload starts
// automatically the instant a recognized recorder attaches.
type recorderSyncScreen struct {
	s         *state
	params    recorderSyncParams
	destRoots []string

	watchCtx    context.Context
	cancelWatch context.CancelFunc

	rows      []*recorderRow
	renderers []*rowRenderer
	rowsBox   *fyne.Container

	blinkOn         bool
	sortByPlugOrder bool // toggled via the "Sort: ..." button; see sortRows
	recordersIdle   atomic.Bool

	uploads    *recorderUploadPanel
	inactivity *recorderInactivityWatcher

	// endSyncMsgLabel and endSyncConfirmBtn, while non-nil, belong to an
	// open confirmEndSync dialog; the blink ticker and rebuildRows keep its
	// text and button styling current as rows finish syncing (see
	// refreshEndSyncDialog).
	endSyncMsgLabel   *widget.Label
	endSyncConfirmBtn *widget.Button
}

// showRecorderSync builds and shows Screen 2 for params, then launches its
// three long-lived goroutines (blink ticker, inactivity timer, volume
// watch).
func showRecorderSync(s *state, params recorderSyncParams) {
	watchCtx, cancelWatch := context.WithCancel(context.Background())

	destRoots := make([]string, len(params.destinations))
	for i, d := range params.destinations {
		destRoots[i] = d.RootPath
	}

	sc := &recorderSyncScreen{
		s:           s,
		params:      params,
		destRoots:   destRoots,
		watchCtx:    watchCtx,
		cancelWatch: cancelWatch,
		blinkOn:     true,
		rowsBox:     container.NewVBox(),
		uploads:     newRecorderUploadPanel(s.win),
		inactivity:  newRecorderInactivityWatcher(),
	}
	sc.recordersIdle.Store(true)

	go sc.runBlinkTicker()
	go sc.inactivity.run(sc.watchCtx, s, &sc.recordersIdle, func() {
		showInactivitySyncPrompt(s, sc.inactivity.signalActivity, sc.endSync)
	})
	go sc.watchVolumes()

	cancelBtn := widget.NewButton("End Sync", sc.confirmEndSync)

	rowsScroll := container.NewVScroll(sc.rowsBox)

	var sortToggleBtn *widget.Button
	sortToggleLabel := func() string {
		if sc.sortByPlugOrder {
			return "Sort: Plug-in order"
		}
		return "Sort: Progress"
	}
	sortToggleBtn = widget.NewButton(sortToggleLabel(), nil)
	sortToggleBtn.OnTapped = func() {
		sc.sortByPlugOrder = !sc.sortByPlugOrder
		sortToggleBtn.SetText(sortToggleLabel())
		sc.reorderRowsBox()
	}
	localHeader := container.NewBorder(nil, nil, nil, sortToggleBtn, sectionHeader("Local Sync"))
	localPanel := container.NewBorder(localHeader, nil, nil, nil, rowsScroll)

	var main fyne.CanvasObject = localPanel
	if len(params.uploads) > 0 {
		main = container.NewHSplit(localPanel, sc.uploads.panel())
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

func (sc *recorderSyncScreen) runBlinkTicker() {
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-sc.watchCtx.Done():
			return
		case <-ticker.C:
			fyne.Do(func() {
				sc.blinkOn = !sc.blinkOn
				for _, rr := range sc.renderers {
					if rr.row.status == jobDone {
						sc.refreshRow(rr)
					}
				}
				sc.refreshEndSyncDialog()
			})
		}
	}
}

func (sc *recorderSyncScreen) watchVolumes() {
	for ev := range recorder.WatchVolumes(sc.watchCtx, time.Second) {
		switch ev.Type {
		case recorder.VolumeAttached:
			sc.onVolumeAttached(ev.Volume)
		case recorder.VolumeDetached:
			sc.onVolumeDetached(ev.Volume)
		}
	}
}

func (sc *recorderSyncScreen) findRow(mountPoint string) (*recorderRow, int) {
	for i, r := range sc.rows {
		if r.volume.MountPoint == mountPoint {
			return r, i
		}
	}
	return nil, -1
}

// findDisconnectedRow looks up a still-tracked row by the recorder's own
// persistent identity (RecorderID + driver model), not by OS mount point.
// Mount points are assigned by the OS and get reused across unrelated
// physical devices - e.g. two recorders offloaded one after another
// through the same USB hub/card reader slot. Matching a reconnect on mount
// point alone (the old behavior) could silently treat a brand new recorder
// as a "reconnect" of whatever row last held that mount point, offloading
// its files under the wrong recorder ID. Only jobDisconnected rows are
// eligible: any row whose device is still attached, or whose job already
// finished/never started, has already been resolved by the attach/detach
// handlers below and is not a valid reconnect target.
func (sc *recorderSyncScreen) findDisconnectedRow(driver recorder.Driver, id string) *recorderRow {
	for _, r := range sc.rows {
		if r.status == jobDisconnected && r.id == id && r.driver != nil && r.driver.Name() == driver.Name() {
			return r
		}
	}
	return nil
}

func (sc *recorderSyncScreen) refreshRow(rr *rowRenderer) {
	label := rr.row.id
	if label == "" {
		label = rr.row.volume.MountPoint
	}
	rr.idText.Segments[0].(*widget.TextSegment).Text = label
	rr.idText.Refresh()
	rr.statusLbl.SetText(rowStatusMessage(rr.row))
	rr.bar.SetValue(rr.row.progress)
	r, g, b, a := rowBackgroundColor(rr.row.status, sc.blinkOn)
	rr.bg.FillColor = colorRGBA(r, g, b, a)
	rr.bg.Refresh()
	if rr.row.status == jobError && rr.row.statusMsg != "" {
		errText := rr.row.statusMsg
		rr.errBtn.OnTapped = func() { showErrorModal(sc.s.win, errText) }
		rr.errBtn.Show()
	} else {
		rr.errBtn.OnTapped = nil
		rr.errBtn.Hide()
	}
}

// sortRows normally puts finished (jobDone) recorders in the top rows,
// then orders the rest by descending progress (closest to finishing next),
// keeping relative order stable among otherwise-equal rows. When
// sortByPlugOrder is set it does nothing, leaving rows in attach order -
// new rows are always appended to rows in attach order, and reconnects
// update the existing row in place, so leaving rows untouched here is
// enough to preserve that order.
func (sc *recorderSyncScreen) sortRows() {
	if sc.sortByPlugOrder {
		return
	}
	sort.SliceStable(sc.rows, func(i, j int) bool {
		iDone := sc.rows[i].status == jobDone
		jDone := sc.rows[j].status == jobDone
		if iDone != jDone {
			return iDone
		}
		if iDone {
			return false
		}
		return sc.rows[i].progress > sc.rows[j].progress
	})
}

// reorderRowsBox re-sorts rows and reassembles rowsBox from the existing
// renderers/cells (no widget recreation), so it's cheap enough to call on
// every status change.
func (sc *recorderSyncScreen) reorderRowsBox() {
	sc.sortRows()
	rendererFor := make(map[*recorderRow]*rowRenderer, len(sc.renderers))
	for _, rr := range sc.renderers {
		rendererFor[rr.row] = rr
	}
	newRenderers := make([]*rowRenderer, 0, len(sc.rows))
	objs := make([]fyne.CanvasObject, 0, len(sc.rows))
	for _, row := range sc.rows {
		rr := rendererFor[row]
		if rr == nil {
			continue
		}
		newRenderers = append(newRenderers, rr)
		objs = append(objs, rr.cell)
	}
	sc.renderers = newRenderers
	sc.rowsBox.Objects = objs
	sc.rowsBox.Refresh()
}

// updateRecordersIdle mirrors "no recorders are actively syncing" (every
// row is jobDone, or there are no rows at all) into recordersIdle, a value
// the inactivity-timer goroutine can read without racing on rows itself,
// since rows is otherwise only ever touched from the Fyne UI thread.
func (sc *recorderSyncScreen) updateRecordersIdle() {
	idle := true
	for _, r := range sc.rows {
		if r.status != jobDone {
			idle = false
			break
		}
	}
	sc.recordersIdle.Store(idle)
}

func (sc *recorderSyncScreen) rebuildRows() {
	sc.sortRows()
	sc.updateRecordersIdle()
	sc.renderers = sc.renderers[:0]
	objs := make([]fyne.CanvasObject, 0, len(sc.rows))
	for _, row := range sc.rows {
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
		sc.refreshRow(rr)
		sc.renderers = append(sc.renderers, rr)
		objs = append(objs, cell)
	}
	sc.rowsBox.Objects = objs
	sc.rowsBox.Refresh()
	sc.refreshEndSyncDialog()
}

func (sc *recorderSyncScreen) refreshAllRows() {
	for _, rr := range sc.renderers {
		sc.refreshRow(rr)
	}
	sc.updateRecordersIdle()
	sc.reorderRowsBox()
	sc.refreshEndSyncDialog()
}

func (sc *recorderSyncScreen) beginOffload(row *recorderRow) {
	row.started = true
	row.status = jobSyncing
	row.statusMsg = ""
	job, progress := recorder.StartOffload(sc.watchCtx, row.driver, row.volume, row.id, sc.destRoots, sc.params.subpath,
		sc.params.experimentName, sc.params.uploads, sc.params.autoDelete, sc.uploads.onUploadEvent)
	row.job = job
	sc.rebuildRows()

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
					// onVolumeDetached, which calls job.Cancel()) - don't
					// clobber that with jobError just because this event
					// happened to arrive after it.
					if row.status != jobDisconnected {
						row.status = jobError
						row.statusMsg = errString(p.Err)
					}
				default:
					row.status = jobSyncing
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
				sc.refreshAllRows()
			})
		}
	}()
}

// onVolumeAttached handles a newly attached volume: detects its driver and
// resolves its recorder ID, then either resumes a previously disconnected
// row matched by device identity (see findDisconnectedRow) or starts a new
// row. Unrecognized volumes are ignored entirely - never shown as a row.
func (sc *recorderSyncScreen) onVolumeAttached(vol recorder.Volume) {
	sc.inactivity.signalActivity()
	driver := recorder.Detect(vol)
	if driver == nil {
		return
	}
	row := &recorderRow{volume: vol, driver: driver, status: jobIdle}
	id, err := driver.RecorderID(vol)
	if err != nil {
		row.status = jobError
		row.statusMsg = errString(err)
	} else {
		row.id = id
	}
	fyne.Do(func() {
		if err == nil {
			if existing := sc.findDisconnectedRow(driver, id); existing != nil {
				// Reconnect of a still-tracked, previously disconnected
				// row, confirmed by the recorder's own ID (not just the
				// mount point it landed on): resume its job rather than
				// duplicating it.
				existing.volume = vol
				existing.driver = driver
				sc.beginOffload(existing)
				sc.rebuildRows()
				return
			}
		}
		sc.rows = append(sc.rows, row)
		sc.rebuildRows()
		if row.status != jobError {
			sc.beginOffload(row)
		}
	})
}

// onVolumeDetached handles a volume going away: a row that already
// finished, or one whose job never started, is simply dropped; a row
// mid-transfer is marked jobDisconnected and its job canceled rather than
// left running against a mount point that may be reassigned to a
// different physical recorder any time after this point (e.g. a jostled
// hub, or another recorder plugged into the same slot). StartOffload also
// re-verifies the recorder's own ID before each file as a second layer of
// defense, but this is what makes that abandonment prompt instead of
// racing.
func (sc *recorderSyncScreen) onVolumeDetached(vol recorder.Volume) {
	fyne.Do(func() {
		row, i := sc.findRow(vol.MountPoint)
		if i < 0 {
			return
		}
		switch {
		case row.done:
			sc.rows = append(sc.rows[:i], sc.rows[i+1:]...)
		case !row.started:
			sc.rows = append(sc.rows[:i], sc.rows[i+1:]...)
		default:
			if row.job != nil {
				row.job.Cancel()
			}
			row.status = jobDisconnected
			row.statusMsg = ""
		}
		sc.rebuildRows()
		sc.inactivity.signalActivity()
	})
}

func (sc *recorderSyncScreen) endSync() {
	sc.cancelWatch()
	showHome(sc.s)
}

// syncingCount returns how many rows are actively mid-transfer.
func (sc *recorderSyncScreen) syncingCount() int {
	n := 0
	for _, r := range sc.rows {
		if r.status == jobSyncing {
			n++
		}
	}
	return n
}

// endSyncMessage describes the current syncing state for the confirmEndSync
// dialog, so it can be recomputed live as rows finish.
func endSyncMessage(n int) string {
	if n == 0 {
		return "No syncs are active; you may now exit without issue."
	}
	if n == 1 {
		return "1 recorder is still syncing. Ending now will interrupt its transfer."
	}
	return fmt.Sprintf("%d recorders are still syncing. Ending now will interrupt their transfers.", n)
}

// refreshEndSyncDialog keeps an open confirmEndSync dialog's message and
// button styling current as rows transition in and out of jobSyncing; a
// no-op when no such dialog is open. Once nothing is syncing, ending the
// session is no longer destructive, so the confirm button drops its danger
// (red) styling back to the default.
func (sc *recorderSyncScreen) refreshEndSyncDialog() {
	if sc.endSyncMsgLabel == nil {
		return
	}
	n := sc.syncingCount()
	sc.endSyncMsgLabel.SetText(endSyncMessage(n))
	if sc.endSyncConfirmBtn != nil {
		if n == 0 {
			sc.endSyncConfirmBtn.Importance = widget.HighImportance
		} else {
			sc.endSyncConfirmBtn.Importance = widget.DangerImportance
		}
		sc.endSyncConfirmBtn.Refresh()
	}
}

// confirmEndSync warns before ending the session if any row is actively
// mid-transfer (jobSyncing), since ending the sync cancels that job in
// progress rather than merely closing the screen. Other states (idle,
// done, error, disconnected) end silently, as before. While the dialog is
// open, its message updates live (see refreshEndSyncDialog) as syncing
// recorders finish.
func (sc *recorderSyncScreen) confirmEndSync() {
	if sc.syncingCount() == 0 {
		sc.endSync()
		return
	}
	sc.endSyncMsgLabel, sc.endSyncConfirmBtn = showDangerConfirm("Recorder still syncing",
		endSyncMessage(sc.syncingCount()), "End Sync", "Return to Sync Screen",
		func(ok bool) {
			sc.endSyncMsgLabel = nil
			sc.endSyncConfirmBtn = nil
			if ok {
				sc.endSync()
			}
		}, sc.s.win)
}
