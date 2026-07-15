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
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
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

	// cancelBtn is the bottom-of-screen action button; its label and
	// OnTapped are kept current by refreshCancelBtn as syncing/uploading
	// activity starts and stops - see hasActiveTransfer.
	cancelBtn *widget.Button

	// endSyncMsgLabel and endSyncConfirmBtn, while non-nil, belong to an
	// open confirmEndSync dialog; the blink ticker and rebuildRows keep its
	// text and button styling current as rows finish syncing (see
	// refreshEndSyncDialog).
	endSyncMsgLabel   *widget.Label
	endSyncConfirmBtn *widget.Button

	// exitBtn sits beside cancelBtn, visible only while cancelBtn reads
	// "Batch Upload" - an escape hatch so committing to Batch Upload isn't
	// the only way off the idle screen. See refreshCancelBtn.
	exitBtn *widget.Button
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
	sc.uploads.onChange = sc.refreshCancelBtn

	go sc.runBlinkTicker()
	go sc.inactivity.run(sc.watchCtx, s, &sc.recordersIdle, func() {
		showInactivitySyncPrompt(s, sc.inactivity.signalActivity, sc.endSync)
	})
	go sc.watchVolumes()

	sc.cancelBtn = widget.NewButton("End Sync", sc.confirmEndSync)
	sc.exitBtn = widget.NewButton("Exit Sync", sc.confirmEndSync)
	sc.exitBtn.Importance = widget.DangerImportance
	sc.exitBtn.Hide()
	sc.refreshCancelBtn()

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

	// Batch mode uploads only after local sync finishes, on a separate
	// screen (see confirmBatchUpload) - there's never anything in flight
	// here, so the Upload Queue/Uploaded panels would only ever be empty.
	var main fyne.CanvasObject = localPanel
	if len(params.uploads) > 0 && !params.batchUpload {
		main = container.NewHSplit(localPanel, sc.uploads.panel())
	}

	identParts := append([]string{params.experimentName}, splitSubpathUI(params.subpath)...)
	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Syncing to: "+strings.Join(identParts, "/"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
		),
		container.NewHBox(sc.cancelBtn, sc.exitBtn),
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
	sc.refreshCancelBtn()
}

// hasActiveTransfer reports whether ending the session right now would
// interrupt something in flight: a recorder still copying locally, or a
// file still uploading to the cloud.
func (sc *recorderSyncScreen) hasActiveTransfer() bool {
	return sc.syncingCount() > 0 || len(sc.uploads.uploading) > 0
}

// refreshCancelBtn keeps the bottom-of-screen button's label and action
// current: "Cancel Sync" while something's actively transferring, "Batch
// Upload" once idle in batch mode (moves to the next step rather than
// ending), or "End Sync" once idle otherwise.
func (sc *recorderSyncScreen) refreshCancelBtn() {
	if sc.cancelBtn == nil {
		return
	}
	switch {
	case sc.hasActiveTransfer():
		sc.cancelBtn.SetText("Cancel Sync")
		sc.cancelBtn.OnTapped = sc.confirmEndSync
		sc.cancelBtn.Importance = widget.MediumImportance
		sc.exitBtn.Hide()
	case sc.params.batchUpload && len(sc.params.uploads) > 0:
		sc.cancelBtn.SetText("Batch Upload")
		sc.cancelBtn.OnTapped = sc.confirmBatchUpload
		sc.cancelBtn.Importance = widget.HighImportance
		sc.exitBtn.Show()
	default:
		sc.cancelBtn.SetText("End Sync")
		sc.cancelBtn.OnTapped = sc.confirmEndSync
		sc.cancelBtn.Importance = widget.MediumImportance
		sc.exitBtn.Hide()
	}
	sc.cancelBtn.Refresh()
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
		sc.params.experimentName, sc.params.uploads, sc.params.autoDelete, sc.params.batchUpload, sc.uploads.onUploadEvent)
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
					row.statusMsg = ""
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
// row. Unrecognized volumes are ignored entirely - never shown as a row. A
// volume that matches more than one registered driver (recorder.Detect
// returns an error) is a driver-implementation conflict, not something the
// user can fix by replugging, so it's still shown as a row - red, with the
// conflict named - rather than silently dropped or silently resolved by
// picking one driver.
func (sc *recorderSyncScreen) onVolumeAttached(vol recorder.Volume) {
	sc.inactivity.signalActivity()
	driver, err := recorder.Detect(vol)
	if driver == nil && err == nil {
		return
	}
	row := &recorderRow{volume: vol, driver: driver, status: jobIdle}
	var id string
	if err != nil {
		row.status = jobError
		row.statusMsg = errString(err)
	} else {
		id, err = driver.RecorderID(vol)
		if err != nil {
			row.status = jobError
			row.statusMsg = errString(err)
		} else {
			row.id = id
		}
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

// endSyncMessage describes the current syncing/uploading state for the
// confirmEndSync dialog, so it can be recomputed live as rows finish and
// uploads drain.
func endSyncMessage(syncing, uploading int) string {
	n := syncing + uploading
	if n == 0 {
		return "No syncs are active; you may now exit without issue."
	}
	var parts []string
	if syncing == 1 {
		parts = append(parts, "1 recorder")
	} else if syncing > 1 {
		parts = append(parts, fmt.Sprintf("%d recorders", syncing))
	}
	if uploading == 1 {
		parts = append(parts, "1 upload")
	} else if uploading > 1 {
		parts = append(parts, fmt.Sprintf("%d uploads", uploading))
	}
	verb := "is"
	if n != 1 {
		verb = "are"
	}
	return fmt.Sprintf("%s still active. Ending now will interrupt %s.", strings.Join(parts, " and "), verb)
}

// refreshEndSyncDialog keeps an open confirmEndSync dialog's message and
// button styling current as rows transition in and out of jobSyncing and
// uploads finish; a no-op when no such dialog is open. Once nothing is
// active, ending the session is no longer destructive, so the confirm
// button drops its danger (red) styling back to the default.
func (sc *recorderSyncScreen) refreshEndSyncDialog() {
	if sc.endSyncMsgLabel == nil {
		return
	}
	syncing, uploading := sc.syncingCount(), len(sc.uploads.uploading)
	sc.endSyncMsgLabel.SetText(endSyncMessage(syncing, uploading))
	if sc.endSyncConfirmBtn != nil {
		if syncing+uploading == 0 {
			sc.endSyncConfirmBtn.Importance = widget.HighImportance
		} else {
			sc.endSyncConfirmBtn.Importance = widget.DangerImportance
		}
		sc.endSyncConfirmBtn.Refresh()
	}
}

// confirmEndSync warns before ending the session if anything is actively
// transferring (see hasActiveTransfer), since ending the session cancels
// that job in progress rather than merely closing the screen. Other states
// (idle, done, error, disconnected) end silently, as before. While the
// dialog is open, its message updates live (see refreshEndSyncDialog) as
// syncing recorders finish and uploads drain.
func (sc *recorderSyncScreen) confirmEndSync() {
	if !sc.hasActiveTransfer() {
		sc.endSync()
		return
	}
	sc.endSyncMsgLabel, sc.endSyncConfirmBtn = showDangerConfirm("Sync still in progress",
		endSyncMessage(sc.syncingCount(), len(sc.uploads.uploading)), "End Sync", "Return to Sync Screen",
		func(ok bool) {
			sc.endSyncMsgLabel = nil
			sc.endSyncConfirmBtn = nil
			if ok {
				sc.endSync()
			}
		}, sc.s.win)
}

// confirmBatchUpload runs a Quick Scan-equivalent existence check between
// the local destinations (already fully synced from every recorder that
// passed through this session, via recorder.StartOffload) and the
// configured remote uploads, then uploads whatever's missing. This is the
// same scan/transfer machinery Sync Experiments' Quick Scan uses (see
// runNWayScan/runNWayTransfers in screen_sync_experiments.go), just
// restricted to one experiment and labeled "Batch Upload" throughout.
// Reached only once params.batchUpload is set and every row is idle (see
// refreshCancelBtn), so there is nothing to warn about interrupting.
func (sc *recorderSyncScreen) confirmBatchUpload() {
	locs := append(append([]syncengine.Location{}, sc.params.destinations...), sc.params.uploads...)
	runBatchUploadScan(sc.s, sc.endSync, locs, sc.params.experimentName)
}

// runBatchUploadScan is runNWayScan (screen_sync_experiments.go) narrowed
// to a single experiment and Quick Scan mode. onDone is called once the
// upload screen is left (Back, or scan/transfer completion) - Batch Upload
// is a terminal action for Sync Recorders' Screen 2, same as End Sync.
func runBatchUploadScan(s *state, onDone func(), locs []syncengine.Location, name string) {
	fset := s.cfg.DefaultFilter
	var result syncengine.NWayScanResult

	task := scanTask{
		Label: name,
		Locs:  locs,
		Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
			r, err := syncengine.ScanNWayWithProgress(ctx, locs, name, fset, progress, syncengine.NWayQuickScan)
			if err != nil {
				return syncengine.ScanResult{}, err
			}
			result = r
			return syncengine.NWayDisplayScanResult(r), nil
		},
	}

	showSyncFlowExtras(s, []scanTask{task}, onDone, syncFlowExtras{
		onNWaySync:   func() { runBatchUploadTransfers(s, onDone, name, result) },
		syncingTitle: "Batch Upload",
		quickScan:    true,
	})
}

// runBatchUploadTransfers builds the minimal transfer plan from
// runBatchUploadScan's result and runs only the local -> remote legs: the
// local destinations already agree with each other directly from the
// recorder offload copy, so no local<->local pass is needed here (unlike
// Sync Experiments' general N-way case).
func runBatchUploadTransfers(s *state, onDone func(), name string, result syncengine.NWayScanResult) {
	pairs := syncengine.BuildNWayTransferPlan(result, syncengine.PreferLocalSource)
	var tasks []scanTask
	for _, pair := range pairs {
		if pair.Dest.Kind != syncengine.LocationRemote {
			continue
		}
		pair := pair
		transferResult := syncengine.ScanResultFromNWayTransfers(pair)
		tasks = append(tasks, scanTask{
			Label: fmt.Sprintf("%s: %s → %s", name, pair.Source.Name, pair.Dest.Name),
			Locs:  []syncengine.Location{pair.Source, pair.Dest},
			Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
				return transferResult, nil
			},
			Start: func(ctx context.Context, expected syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
				return syncengine.StartSyncExperiments(ctx, pair.Source, pair.Dest, name, expected)
			},
		})
	}
	if len(tasks) == 0 {
		onDone()
		return
	}
	showSyncFlowExtras(s, tasks, onDone, syncFlowExtras{autoSync: true, quickScan: true, syncingTitle: "Batch Upload"})
}
