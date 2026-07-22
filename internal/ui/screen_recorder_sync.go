package ui

import (
	"context"
	"fmt"
	"path/filepath"
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

	// batchUploadPaths accumulates, across every recorder offloaded in this
	// session, the relative path (subpath/recorderID/DestRelPath, relative
	// to the experiment root - the same rooting ScanNWayWithProgress uses)
	// of every file that reached verified-complete. In batch-upload mode
	// (params.batchUpload), confirmBatchUpload restricts the upload to
	// exactly this set, so files already local but never uploaded from an
	// earlier, unrelated session aren't swept in - see runBatchUploadTransfers.
	batchUploadPaths map[string]bool

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

	// timestampsHandled guards checkTimestampsThen from showing the review
	// screen more than once per session - confirmEndSync/confirmBatchUpload
	// can each call it, and either can be retried (e.g. backing out of the
	// End Sync confirm dialog and pressing it again).
	timestampsHandled bool
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
		s:                s,
		params:           params,
		destRoots:        destRoots,
		watchCtx:         watchCtx,
		cancelWatch:      cancelWatch,
		blinkOn:          true,
		rowsBox:          container.NewVBox(),
		uploads:          newRecorderUploadPanel(s.win),
		inactivity:       newRecorderInactivityWatcher(),
		batchUploadPaths: map[string]bool{},
	}
	sc.recordersIdle.Store(true)
	sc.uploads.onChange = sc.refreshCancelBtn

	go sc.runBlinkTicker()
	go sc.inactivity.run(sc.watchCtx, s, &sc.recordersIdle, func() {
		showInactivitySyncPrompt(s, sc.inactivity.signalActivity, sc.confirmEndSync)
	})
	go sc.watchVolumes()

	sc.cancelBtn = widget.NewButton("End Sync", sc.confirmEndSync)
	// Exit Sync deliberately calls doConfirmEndSync directly, not
	// confirmEndSync - it's the escape hatch off the idle screen without
	// committing to Batch Upload, so it should just end the session, not
	// also run the timestamp check (that's what Check Times/Batch Upload
	// are for).
	sc.exitBtn = widget.NewButton("Exit Sync", sc.doConfirmEndSync)
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

// refreshCancelBtn keeps the bottom-of-screen button's label, action, and
// enablement current. As long as a bad-timestamp check is still pending
// (detection is on and checkTimestampsThen hasn't shown its review yet, see
// timestampsHandled), the button always reads "Check Times" - even while a
// recorder is still syncing or a background upload is still draining - and
// is simply disabled for the duration of any such transfer (see
// hasActiveTransfer), rather than being replaced by "Cancel Sync": tapping
// it may land on the review screen rather than actually
// batch-uploading/ending, so it shouldn't be actionable, but it also
// shouldn't disappear and reappear as transfers start and stop. Once
// nothing is pending, this falls back to the original three states:
// "Cancel Sync" while something's actively transferring, "Batch Upload"
// once idle in batch mode (moves to the next step rather than ending), or
// "End Sync" once idle otherwise.
func (sc *recorderSyncScreen) refreshCancelBtn() {
	if sc.cancelBtn == nil {
		return
	}
	pendingTimestampCheck := sc.params.detectBadTimestamps && !sc.timestampsHandled
	switch {
	case pendingTimestampCheck:
		sc.cancelBtn.SetText("Check Times")
		if sc.params.batchUpload && len(sc.params.uploads) > 0 {
			sc.cancelBtn.OnTapped = sc.confirmBatchUpload
			sc.cancelBtn.Importance = widget.HighImportance
			sc.exitBtn.Show()
		} else {
			sc.cancelBtn.OnTapped = sc.confirmEndSync
			sc.cancelBtn.Importance = widget.MediumImportance
			sc.exitBtn.Hide()
		}
		if sc.hasActiveTransfer() {
			sc.cancelBtn.Disable()
		} else {
			sc.cancelBtn.Enable()
		}
	case sc.hasActiveTransfer():
		sc.cancelBtn.SetText("Cancel Sync")
		sc.cancelBtn.OnTapped = sc.confirmEndSync
		sc.cancelBtn.Importance = widget.DangerImportance
		sc.cancelBtn.Enable()
		sc.exitBtn.Hide()
	case sc.params.batchUpload && len(sc.params.uploads) > 0:
		sc.cancelBtn.SetText("Batch Upload")
		sc.cancelBtn.OnTapped = sc.confirmBatchUpload
		sc.cancelBtn.Importance = widget.HighImportance
		sc.cancelBtn.Enable()
		sc.exitBtn.Show()
	default:
		sc.cancelBtn.SetText("End Sync")
		sc.cancelBtn.OnTapped = sc.confirmEndSync
		sc.cancelBtn.Importance = widget.MediumImportance
		sc.cancelBtn.Enable()
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

// recordBatchUploadPaths adds every verified-complete file from one
// recorder's just-finished offload into sc.batchUploadPaths, keyed relative
// to the experiment root (subpath/recorderID/DestRelPath) - the same
// rooting ScanNWayWithProgress/BuildNWayTransferPlan use for
// NWayTransfer.RelPath, since that scan is rooted at the experiment
// directory itself, not the location root. See runBatchUploadTransfers.
func (sc *recorderSyncScreen) recordBatchUploadPaths(recorderID string, files map[string]recorder.FileOffloadProgress) {
	for relPath, fp := range files {
		if fp.State != recorder.StateComplete {
			continue
		}
		parts := append(append([]string{}, splitSubpathUI(sc.params.subpath)...), recorderID, relPath)
		sc.batchUploadPaths[filepath.ToSlash(filepath.Join(parts...))] = true
	}
}

func (sc *recorderSyncScreen) beginOffload(row *recorderRow) {
	row.started = true
	row.status = jobSyncing
	row.statusMsg = ""
	// Captured now, while the volume is still attached, so bad-timestamp
	// detection can run later purely from these (see the OffloadDone case
	// below) without needing the recorder itself, which may already be
	// disconnected or wiped by the time every file has landed.
	if sc.params.detectBadTimestamps {
		row.sourceFiles, _ = row.driver.SourceFiles(row.volume)
		row.destDirs = recorder.DestDirs(sc.destRoots, sc.params.subpath, sc.params.experimentName, row.id)
	}
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
					if sc.params.batchUpload {
						sc.recordBatchUploadPaths(row.id, p.Files)
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

// checkTimestampsThen runs the bad-timestamp check computing a session-wide
// consensus date (see recorder.ConsensusDate) and, for each recorder,
// checking its earliest file against that consensus and every OTHER
// recorder's earliest file (see recorder.CheckRecorderTimestamp's doc for
// why cross-recorder, rather than a recorder's own other files, is the only
// valid ground truth) - then either shows the timestamp review screen (whose
// own Continue action calls next) or, if there's nothing to review, calls
// next immediately.
//
// This runs at the point the user actually commits to leaving Screen 2 -
// End Sync, Exit Sync, or Batch Upload - rather than as soon as every
// recorder goes idle: reviewing corrections is only useful once the user is
// done attaching recorders, and running it earlier would repeatedly
// interrupt a session where more recorders are still expected. For Batch
// Upload specifically, this also means the correction lands before any
// remote upload starts, so a corrected filename - not the original bad one -
// is what ever reaches the cloud. Guarded by timestampsHandled so it only
// ever shows the review once per session, even if called again (e.g. the
// user backs out of a confirm dialog and retries).
func (sc *recorderSyncScreen) checkTimestampsThen(next func()) {
	if sc.timestampsHandled || !sc.params.detectBadTimestamps {
		next()
		return
	}

	type parsedRow struct {
		row    *recorderRow
		parser recorder.TimestampParser
		start  time.Time
	}
	var eligible []parsedRow
	for _, r := range sc.rows {
		if r.status != jobDone || len(r.sourceFiles) == 0 {
			continue
		}
		parser, ok := r.driver.(recorder.TimestampParser)
		if !ok {
			continue
		}
		var start time.Time
		found := false
		for _, f := range r.sourceFiles {
			if t, ok := parser.ParseTimestamp(f.DestRelPath); ok && (!found || t.Before(start)) {
				start = t
				found = true
			}
		}
		if !found {
			continue
		}
		eligible = append(eligible, parsedRow{r, parser, start})
	}
	if len(eligible) == 0 {
		// Nothing this driver set supports checking - proceed as if
		// detection were off.
		next()
		return
	}
	sc.timestampsHandled = true

	// destDirsByID lets afterFix (below) find a recorder's destDirs from
	// just the timestampReviewRow applyAndContinue hands it - destDirs
	// itself isn't part of timestampReviewRow since Manage Files' Retime
	// has no equivalent (it applies via rclone Locations instead).
	destDirsByID := make(map[string][]string, len(eligible))

	inputs := make([]timestampReviewInput, 0, len(eligible))
	for _, e := range eligible {
		parser, sourceFiles, destDirs := e.parser, e.row.sourceFiles, e.row.destDirs
		destDirsByID[e.row.id] = destDirs
		inputs = append(inputs, timestampReviewInput{
			recorderID:  e.row.id,
			parser:      parser,
			sourceFiles: sourceFiles,
			start:       e.start,
			// Sync Recorders renames the local destination dirs directly - the
			// files are always local here - rather than Manage Files' rclone
			// rename across arbitrary Locations.
			apply: func(correct func(time.Time) time.Time) error {
				return recorder.ApplyTimestampFix(destDirs, parser, sourceFiles, correct)
			},
		})
	}

	reviewRows := buildTimestampReviewRows(inputs, sc.params.timestampTolerance)
	if len(reviewRows) == 0 {
		sc.timestampsHandled = false
		next()
		return
	}

	continueLabel := "End Sync"
	if sc.params.batchUpload && len(sc.params.uploads) > 0 {
		continueLabel = "Batch Upload"
	}
	exitWarning := "Exiting now will not apply any timestamp corrections - every recorder's files keep their original names."
	if sc.params.batchUpload && len(sc.params.uploads) > 0 {
		exitWarning += " Nothing will be uploaded to the remote destination either."
	}

	showTimestampReview(timestampReviewHost{
		s:             sc.s,
		win:           sc.s.win,
		continueLabel: continueLabel,
		onContinue:    next,
		exitLabel:     "Exit Sync",
		exitWarning:   exitWarning,
		onExit:        sc.doConfirmEndSync,
		afterFix: func(row timestampReviewRow, delta time.Duration) {
			if !sc.params.batchUpload && len(sc.params.uploads) > 0 {
				reuploadCorrectedFiles(sc, row, destDirsByID[row.recorderID], delta)
			}
		},
	}, reviewRows, sc.params.timestampTolerance)
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
	showSyncExperiments(sc.s)
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

// confirmEndSync is the End Sync/Exit Sync button handler. If anything is
// actively transferring it defers straight to doConfirmEndSync (see
// hasActiveTransfer) - interrupting a transfer is the more urgent thing to
// confirm, and there's nothing yet for the timestamp check to review, since
// checkTimestampsThen only considers rows that have already finished (see
// updateRecordersIdle). Otherwise it runs the timestamp check first, so a
// pending correction is reviewed before the session actually ends.
func (sc *recorderSyncScreen) confirmEndSync() {
	if sc.hasActiveTransfer() {
		sc.doConfirmEndSync()
		return
	}
	sc.checkTimestampsThen(sc.doConfirmEndSync)
}

// doConfirmEndSync warns before ending the session if anything is actively
// transferring (see hasActiveTransfer), since ending the session cancels
// that job in progress rather than merely closing the screen. Other states
// (idle, done, error, disconnected) end silently, as before. While the
// dialog is open, its message updates live (see refreshEndSyncDialog) as
// syncing recorders finish and uploads drain.
func (sc *recorderSyncScreen) doConfirmEndSync() {
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

// confirmBatchUpload is the Batch Upload button handler: it runs the
// timestamp check first (see checkTimestampsThen), so any correction lands
// before doConfirmBatchUpload starts the actual upload - the corrected
// filename, not the original bad one, is what reaches the remote.
func (sc *recorderSyncScreen) confirmBatchUpload() {
	sc.checkTimestampsThen(sc.doConfirmBatchUpload)
}

// doConfirmBatchUpload runs a Quick Scan-equivalent existence check between
// the local destinations (already fully synced from every recorder that
// passed through this session, via recorder.StartOffload) and the
// configured remote uploads, then uploads whatever's missing. This is the
// same scan/transfer machinery Sync Experiments' Quick Scan uses (see
// runNWayScan/runNWayTransfers in screen_sync_experiments.go), just
// restricted to one experiment and labeled "Batch Upload" throughout.
// Reached only once params.batchUpload is set and every row is idle (see
// refreshCancelBtn), so there is nothing to warn about interrupting.
func (sc *recorderSyncScreen) doConfirmBatchUpload() {
	locs := append(append([]syncengine.Location{}, sc.params.destinations...), sc.params.uploads...)
	runBatchUploadScan(sc.s, sc.endSync, locs, sc.params.experimentName, sc.batchUploadPaths)
}

// runBatchUploadScan is runNWayScan (screen_sync_experiments.go) narrowed
// to a single experiment and Quick Scan mode. onDone is called once the
// upload screen is left (Back, or scan/transfer completion) - Batch Upload
// is a terminal action for Sync Recorders' Screen 2, same as End Sync.
// allowedPaths restricts the eventual transfer to exactly the files this
// session's offloads just wrote (see recordBatchUploadPaths) - the scan
// itself still covers the whole experiment (so existing/converged files
// are correctly detected as already in sync), but runBatchUploadTransfers
// filters the resulting plan down to allowedPaths before transferring.
func runBatchUploadScan(s *state, onDone func(), locs []syncengine.Location, name string, allowedPaths map[string]bool) {
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
			// Restrict to allowedPaths right after the scan, before
			// anything is displayed - both NWayDisplayScanResult (the
			// counts/list shown on this screen) and the later transfer plan
			// (runBatchUploadTransfers) derive entirely from r.Files, so
			// filtering it here is enough for both to only ever reflect
			// files this session's recorder offloads actually just wrote.
			filtered := r.Files[:0:0]
			for _, f := range r.Files {
				if allowedPaths[f.RelPath] {
					filtered = append(filtered, f)
				}
			}
			r.Files = filtered
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
// Sync Experiments' general N-way case). result.Files has already been
// restricted to allowedPaths by runBatchUploadScan, so every pair built
// here only ever contains files this session's recorder offloads actually
// just wrote - files already sitting locally under this experiment from an
// earlier, unrelated session are never swept into this batch; that's Sync
// Experiments' job.
func runBatchUploadTransfers(s *state, onDone func(), name string, result syncengine.NWayScanResult) {
	pairs := syncengine.BuildNWayTransferPlan(result, syncengine.PreferLocalSource)
	var tasks []scanTask
	for _, pair := range pairs {
		if pair.Dest.Kind != syncengine.LocationRemote {
			continue
		}
		pair := pair
		if len(pair.Files) == 0 {
			continue
		}
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
