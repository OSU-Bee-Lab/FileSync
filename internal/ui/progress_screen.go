package ui

import (
	"context"
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// This file holds the scan/sync screen's struct, layout construction, and
// top-level refresh orchestration. See progress_model.go for the underlying
// data types/mutations (including syncMetrics/computeSyncMetrics),
// progress_widgets.go for shared render primitives, progress_rows.go for the
// Folders/Files row-list construction, and progress_run.go for the scan/sync
// goroutine drivers that mutate progressScreen's expStates and call back
// into its refresh methods.

// syncFlowExtras adapts showSyncFlow's two extra modes beyond the plain
// pairwise scan-then-sync flow, both used by N-way sync (see
// screen_sync_experiments.go):
type syncFlowExtras struct {
	// nway, when non-nil, marks this session as an N-way scan: conflict rows
	// caption and click through to the resolver, and Sync is gated until
	// every conflict carries an explicit resolution.
	nway *nwayResolver
	// onNWaySync replaces the built-in Sync behavior (pairwise conflict
	// prompt + in-place copy run). The N-way session's tasks have no Start —
	// applying resolutions and building the real transfer plan happens in
	// this callback, which launches a fresh transfer session.
	onNWaySync func()
	// autoSync starts the copy run as soon as the scan phase completes
	// cleanly. Used by the N-way transfer session, whose "scan" is an
	// instant replay of an already-reviewed, already-confirmed plan — a
	// second Sync press there would be pure ceremony.
	autoSync bool
	// syncingTitle overrides the phaseSyncing title text ("Syncing" by
	// default) — e.g. "Quick Syncing"/"Full Syncing" for the N-way sync
	// flows, so the header reflects which button the user pressed.
	syncingTitle string
	// quickScan marks a scan that never verified file content (see
	// syncengine.NWayQuickScan): renderMetrics must not claim files are
	// "already synced", since that was never checked — only that a path
	// exists at every location.
	quickScan bool
	// onScanDone, if set, is called once every task's scan has finished
	// cleanly (no cancellation, no per-experiment error) instead of the
	// default "Ready to Sync" render. Used by N-way's quick-sync scan to
	// jump straight into the per-(source, dest) transfer-plan session —
	// direction isn't known until the diff completes, so this is the
	// earliest point the split can be shown. If any experiment errored,
	// this is skipped and the aggregate scan screen renders as usual so
	// the user can see what went wrong.
	onScanDone func()
	// finishedTitle/finishedMessage, when set, mark a session with no tasks
	// at all — e.g. N-way's transfer plan turning up empty because every
	// location already agrees. showSyncFlowExtras skips the scan phase
	// entirely (there is nothing to scan) and renders straight into the
	// phaseSyncComplete chrome with this title and message, so the user
	// sees a normal finished-sync screen (with a working Back/"Done") rather
	// than a blocking dialog or a scan that never progresses.
	finishedTitle   string
	finishedMessage string
}

// progressScreen holds all state and widgets for the shared scan/sync
// screen (Sync Experiments, Sync Recorders, Pull Files, and N-way scan/
// transfer all funnel through here via showSyncFlow/showSyncFlowExtras).
type progressScreen struct {
	s      *state
	tasks  []scanTask
	onBack func()
	extras syncFlowExtras

	phase           syncPhase
	selectedExpIdx  int
	selectedFoldIdx int
	expStates       []*expUIState
	scanResults     []syncengine.ScanResult

	// activeCancel cancels every task/job currently in flight for the
	// current run (scan or sync). Created on the UI goroutine before the
	// run's worker goroutine is launched — see progress_run.go.
	activeCancel context.CancelFunc

	// cancelling is true from the moment the user confirms "Cancel Sync"
	// until the in-flight jobs actually wind down (runSync resets it on the
	// next run). It exists because that wind-down isn't instant — jobs keep
	// reporting progress, and refreshUI/applyPhaseChrome run on every one of
	// those updates — so without this flag the next refresh would just
	// redraw "Cancel Sync" as if nothing happened.
	cancelling bool

	titleLabel *widget.Label
	speedLabel *widget.Label
	retryLabel *canvas.Text

	overallBar    *widget.ProgressBar
	overallBarInf *widget.ProgressBarInfinite

	expValue   *widget.Label
	filesValue *widget.Label
	bytesValue *widget.Label

	errorLabel       *widget.Label
	finishedMsgLabel *widget.Label

	expList *widget.List

	fileUnsyncedList, fileSyncedList *widget.List
	fileUnsyncedRows, fileSyncedRows []barRow
	applyFilesMode                   func(hasUnsynced, hasSynced bool)

	foldUnsyncedList, foldSyncedList *widget.List
	foldUnsyncedRows, foldSyncedRows []barRow
	applyFoldMode                    func(hasUnsynced, hasSynced bool)

	cancelBtn, backBtn, syncBtn, scanBtn, resolveBtn *widget.Button
}

// isSyncing reports whether a real copy has run (or is running). Progress
// bars only fill during/after an actual sync; a scan leaves them white
// because nothing has been transferred yet.
func (ps *progressScreen) isSyncing() bool {
	return ps.phase == phaseSyncing || ps.phase == phaseSyncComplete || ps.phase == phaseSyncCancelled
}

// showSyncFlow shows the plain pairwise scan-then-sync flow (no N-way
// extras).
func showSyncFlow(s *state, tasks []scanTask, onBack func()) {
	showSyncFlowExtras(s, tasks, onBack, syncFlowExtras{})
}

// showSyncFlowExtras constructs the screen, wires up its layout, and kicks
// off the initial scan.
func showSyncFlowExtras(s *state, tasks []scanTask, onBack func(), extras syncFlowExtras) {
	ps := &progressScreen{
		s:               s,
		tasks:           tasks,
		onBack:          onBack,
		extras:          extras,
		phase:           phaseScanRunning,
		selectedExpIdx:  -1,
		selectedFoldIdx: -1,
		expStates:       make([]*expUIState, len(tasks)),
		scanResults:     make([]syncengine.ScanResult, len(tasks)),
	}
	for i, t := range tasks {
		ps.expStates[i] = &expUIState{label: t.Label, status: statusWaiting}
	}

	content := ps.buildLayout()
	ps.s.setContent(container.NewPadded(content))

	if len(tasks) == 0 {
		// Nothing to scan at all (e.g. N-way found every location already
		// agrees) — skip the scan phase outright and land straight on the
		// finished chrome instead of a scan that runs zero tasks and never
		// visibly progresses.
		ps.phase = phaseSyncComplete
		ps.refreshUI()
		return
	}

	ps.expList.Select(0)
	ps.refreshUI()

	ps.scanBtn.OnTapped = ps.runScan
	ps.runScan()
}

// buildLayout constructs every widget, wires selection/tap handlers, and
// returns the screen's root content. It does not start the scan — the
// caller kicks that off once the screen is live.
func (ps *progressScreen) buildLayout() fyne.CanvasObject {
	s := ps.s

	ps.titleLabel = widget.NewLabelWithStyle("Scanning...", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	ps.speedLabel = widget.NewLabel("")
	ps.speedLabel.Hide()

	// retryLabel surfaces transient copy errors (dropped connections,
	// timeouts) that rclone is retrying on its own - see
	// syncengine.ProgressSnapshot.Retrying. It's amber, not red: these
	// aren't failures yet, and the progress bar keeps running normally
	// underneath while a retry is pending.
	ps.retryLabel = canvas.NewText("", color.NRGBA{R: 217, G: 119, B: 6, A: 255})
	ps.retryLabel.TextStyle = fyne.TextStyle{Bold: true}
	ps.retryLabel.Hide()

	ps.overallBar = widget.NewProgressBar()
	ps.overallBarInf = widget.NewProgressBarInfinite()

	ps.expValue = widget.NewLabelWithStyle("0 / 0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	ps.filesValue = widget.NewLabelWithStyle("0 / 0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	ps.bytesValue = widget.NewLabelWithStyle("0 B / 0 B", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	expBlurb := metricPanel("Experiments", ps.expValue, color.NRGBA{R: 232, G: 240, B: 254, A: 255})
	filesBlurb := metricPanel("Files", ps.filesValue, color.NRGBA{R: 255, G: 239, B: 219, A: 255})
	bytesBlurb := metricPanel("Bytes", ps.bytesValue, color.NRGBA{R: 243, G: 232, B: 255, A: 255})

	metrics := container.NewGridWithColumns(3, expBlurb, filesBlurb, bytesBlurb)

	ps.errorLabel = widget.NewLabel("")
	ps.errorLabel.Wrapping = fyne.TextWrapWord
	ps.errorLabel.Hide()

	ps.finishedMsgLabel = widget.NewLabel("")
	ps.finishedMsgLabel.Wrapping = fyne.TextWrapWord
	ps.finishedMsgLabel.Hide()

	ps.expList = widget.NewList(
		func() int { return len(ps.expStates) },
		func() fyne.CanvasObject { return createBackingBarItem() },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			exp := ps.expStates[id]
			prog := 0.0
			if ps.isSyncing() {
				if exp.copyTotalBytes > 0 {
					prog = float64(exp.copyBytesDone) / float64(exp.copyTotalBytes)
				} else {
					prog = 1.0
				}
				// Same 99%-hold as the overall bar: don't claim 100% for an
				// individual experiment until the sync phase itself confirms
				// completion.
				if ps.phase == phaseSyncing && prog > 0.99 {
					prog = 0.99
				}
			} else if exp.totalBytes > 0 {
				prog = float64(exp.bytesDone) / float64(exp.totalBytes)
			}
			summary := fmt.Sprintf("%d%%", int(prog*100))
			isSelected := ps.selectedExpIdx == int(id)
			updateBackingBarItem(obj, exp.label, summary, prog, exp.err, exp.hasError, false, isSelected, s.win)
		},
	)

	// Files column.
	ps.fileUnsyncedList = ps.makeBarList(&ps.fileUnsyncedRows, nil)
	ps.fileSyncedList = ps.makeBarList(&ps.fileSyncedRows, nil)
	filesSplit, applyFilesMode := buildSplit(ps.fileUnsyncedList, ps.fileSyncedList)
	ps.applyFilesMode = applyFilesMode

	if ps.extras.nway != nil {
		// Clicking a conflict row opens the resolver at that file (rows only
		// carry conflictRelPath once their experiment's scan has completed).
		ps.fileUnsyncedList.OnSelected = func(id widget.ListItemID) {
			defer ps.fileUnsyncedList.UnselectAll()
			if int(id) < 0 || int(id) >= len(ps.fileUnsyncedRows) {
				return
			}
			row := ps.fileUnsyncedRows[id]
			if row.conflictRelPath == "" || ps.selectedExpIdx < 0 || ps.selectedExpIdx >= len(ps.expStates) {
				return
			}
			key := nwayConflictKey{expName: ps.expStates[ps.selectedExpIdx].label, relPath: row.conflictRelPath}
			showNWayResolveDialog(s, ps.extras.nway, &key)
		}
	}

	// Folders column.
	foldSelected := func(r barRow) bool { return r.refIdx == ps.selectedFoldIdx }
	ps.foldUnsyncedList = ps.makeBarList(&ps.foldUnsyncedRows, foldSelected)
	ps.foldSyncedList = ps.makeBarList(&ps.foldSyncedRows, foldSelected)
	foldSplit, applyFoldMode := buildSplit(ps.foldUnsyncedList, ps.foldSyncedList)
	ps.applyFoldMode = applyFoldMode

	foldSelect := func(rows *[]barRow, other *widget.List) func(widget.ListItemID) {
		return func(id widget.ListItemID) {
			if int(id) < 0 || int(id) >= len(*rows) {
				return
			}
			other.UnselectAll()
			ps.selectedFoldIdx = (*rows)[id].refIdx
			ps.refreshFolders()
			ps.refreshFiles()
		}
	}
	ps.foldUnsyncedList.OnSelected = foldSelect(&ps.foldUnsyncedRows, ps.foldSyncedList)
	ps.foldSyncedList.OnSelected = foldSelect(&ps.foldSyncedRows, ps.foldUnsyncedList)

	ps.expList.OnSelected = func(id widget.ListItemID) {
		ps.selectedExpIdx = int(id)
		ps.selectedFoldIdx = 0
		ps.foldUnsyncedList.UnselectAll()
		ps.foldSyncedList.UnselectAll()
		ps.expList.Refresh()
		ps.refreshFolders()
		ps.refreshFiles()
		ps.syncErrorLabelForSelection()
	}

	columns := container.NewGridWithColumns(3,
		createColumn("Experiments", ps.expList),
		createColumn("Folders", foldSplit),
		createColumn("Files", filesSplit),
	)

	ps.cancelBtn = widget.NewButton("Cancel", func() {
		cancelNow := func() {
			// All running tasks' contexts (scan or sync) are children of
			// the single context created for this run, so cancelling it
			// cascades to every task/job currently in flight, however many
			// are running concurrently.
			if ps.activeCancel != nil {
				ps.activeCancel()
			}
		}
		if ps.phase != phaseSyncing {
			cancelNow()
			return
		}
		_, _ = showDangerConfirm("Sync still in progress",
			"A sync is still in progress. Interrupting it now will leave partial files at the destination — these will not be restored by the next Quick Sync and will require a Full Sync to identify and resolve.",
			"Cancel Sync", "Continue Syncing",
			func(ok bool) {
				if ok {
					ps.cancelling = true
					cancelNow()
					ps.refreshUI()
				}
			}, s.win)
	})

	// Batch Upload never returns to a prior screen - like End Sync, onBack
	// here ends the recorder-sync session outright - so its Back button is
	// labeled "Exit Sync" to reflect that.
	backLabel := "Back"
	if ps.extras.syncingTitle == "Batch Upload" {
		backLabel = "Exit Sync"
	}
	ps.backBtn = widget.NewButton(backLabel, ps.onBack)
	if backLabel == "Exit Sync" {
		ps.backBtn.Importance = widget.DangerImportance
	} else {
		ps.backBtn.Importance = widget.MediumImportance
	}

	ps.syncBtn = widget.NewButton("Sync", func() {
		if ps.extras.onNWaySync != nil {
			ps.extras.onNWaySync()
			return
		}
		if conflicts := collectConflicts(ps.tasks, ps.scanResults); len(conflicts) > 0 {
			showConflictsPrompt(s, conflicts, ps.runSync)
			return
		}
		ps.runSync()
	})
	ps.syncBtn.Importance = widget.HighImportance
	ps.syncBtn.Hide()

	ps.resolveBtn = widget.NewButton("Resolve conflicts…", func() {
		if ps.extras.nway != nil {
			showNWayResolveDialog(s, ps.extras.nway, nil)
		}
	})
	ps.resolveBtn.Importance = widget.WarningImportance
	ps.resolveBtn.Hide()

	ps.scanBtn = widget.NewButton("Scan", nil) // OnTapped set by the caller after runScan is defined
	ps.scanBtn.Importance = widget.MediumImportance
	ps.scanBtn.Hide()

	progressContainer := container.NewStack(ps.overallBar, ps.overallBarInf)

	header := container.NewVBox(
		container.NewHBox(ps.titleLabel, ps.speedLabel, ps.retryLabel),
		progressContainer,
		metrics,
		ps.errorLabel,
		ps.finishedMsgLabel,
		widget.NewSeparator(),
	)

	content := container.NewBorder(
		header,
		container.NewHBox(ps.cancelBtn, ps.scanBtn, ps.resolveBtn, ps.syncBtn, ps.backBtn),
		nil, nil,
		columns,
	)

	if ps.extras.nway != nil {
		// Any resolution change re-captions conflict rows and re-evaluates
		// the Resolve/Sync gate.
		ps.extras.nway.onChange = func() {
			ps.refreshFiles()
			ps.refreshUI()
		}
	}

	return content
}

// syncErrorLabelForSelection shows/hides the error banner for the currently
// selected experiment. Called both right after a selection change and on
// every refreshUI, since the selected experiment's error can appear later
// (e.g. mid-scan).
func (ps *progressScreen) syncErrorLabelForSelection() {
	if ps.selectedExpIdx < 0 || ps.selectedExpIdx >= len(ps.expStates) {
		ps.errorLabel.Hide()
		return
	}
	exp := ps.expStates[ps.selectedExpIdx]
	if exp.err != nil {
		ps.errorLabel.SetText(fmt.Sprintf("Error in %s: %s", exp.label, exp.err.Error()))
		ps.errorLabel.Show()
	} else {
		ps.errorLabel.Hide()
	}
}

// gateNWaySync applies the N-way-specific Resolve/Sync gating shown once a
// scan completes: Sync stays unreachable until every conflict carries an
// explicit resolution — there is deliberately no default.
func (ps *progressScreen) gateNWaySync() {
	ps.resolveBtn.Hide()
	if ps.extras.nway == nil {
		return
	}
	unresolved := ps.extras.nway.unresolvedCount()
	switch {
	case unresolved > 0:
		ps.titleLabel.SetText(fmt.Sprintf("Scan complete — %d conflict(s) to resolve", unresolved))
		ps.syncBtn.Hide()
		ps.resolveBtn.SetText(fmt.Sprintf("Resolve %d conflict(s)…", unresolved))
		ps.resolveBtn.Show()
	case ps.extras.nway.conflictCount() > 0:
		ps.resolveBtn.SetText("Review conflict resolutions")
		ps.resolveBtn.Show()
		fallthrough
	default:
		// Overwrite/rename/delete resolutions are real work even when the
		// scan itself found nothing to copy.
		if ps.extras.nway.hasActionable() {
			ps.syncBtn.Enable()
		}
	}
}

// applyPhaseChrome switches title text, progress-bar visibility, and button
// visibility/enablement based on the current phase.
func (ps *progressScreen) applyPhaseChrome() {
	if ps.phase != phaseSyncComplete {
		ps.finishedMsgLabel.Hide()
	}
	switch ps.phase {
	case phaseScanRunning:
		ps.titleLabel.SetText("Scanning...")
		ps.overallBar.Hide()
		ps.overallBarInf.Show()
		ps.overallBarInf.Start()
		ps.syncBtn.Hide()
		ps.scanBtn.Hide()
		ps.resolveBtn.Hide()
		ps.cancelBtn.Importance = widget.MediumImportance
		ps.cancelBtn.SetText("Cancel")
		ps.cancelBtn.Show()
		ps.cancelBtn.Enable()
		ps.cancelBtn.Refresh()
		ps.backBtn.Disable()
	case phaseScanComplete:
		ps.titleLabel.SetText("Ready to Sync")
		ps.overallBarInf.Stop()
		ps.overallBarInf.Hide()
		ps.overallBar.Hide()
		ps.scanBtn.Hide()
		ps.syncBtn.Show()
		var totalCopyToSync int
		for _, e := range ps.expStates {
			for _, f := range e.folders {
				for _, file := range f.files {
					if file.action == syncengine.ActionCopy {
						totalCopyToSync++
					}
				}
			}
		}
		if totalCopyToSync > 0 {
			ps.syncBtn.Enable()
		} else {
			ps.syncBtn.Disable()
		}
		ps.gateNWaySync()
		ps.cancelBtn.Hide()
		ps.backBtn.Enable()
	case phaseScanCancelled:
		ps.titleLabel.SetText("Scan Cancelled")
		ps.overallBarInf.Stop()
		ps.overallBarInf.Hide()
		ps.overallBar.Hide()
		ps.syncBtn.Hide()
		ps.resolveBtn.Hide()
		ps.scanBtn.Show()
		ps.scanBtn.Enable()
		ps.cancelBtn.Hide()
		ps.backBtn.Enable()
	case phaseSyncing:
		title := "Syncing"
		if ps.extras.syncingTitle != "" {
			title = ps.extras.syncingTitle
		}
		ps.titleLabel.SetText(title)
		ps.overallBarInf.Hide()
		ps.overallBar.Show()
		ps.scanBtn.Hide()
		ps.syncBtn.Hide()
		ps.resolveBtn.Hide()
		ps.cancelBtn.Importance = widget.DangerImportance
		ps.cancelBtn.Show()
		if ps.cancelling {
			ps.cancelBtn.SetText("Cancelling...")
			ps.cancelBtn.Disable()
		} else {
			ps.cancelBtn.SetText("Cancel Sync")
			ps.cancelBtn.Enable()
		}
		ps.cancelBtn.Refresh()
		ps.backBtn.Disable()
	case phaseSyncComplete:
		if ps.extras.finishedTitle != "" {
			ps.titleLabel.SetText(ps.extras.finishedTitle)
		} else {
			var hasAnyErrors bool
			for _, e := range ps.expStates {
				if e.hasError {
					hasAnyErrors = true
					break
				}
			}
			if hasAnyErrors {
				ps.titleLabel.SetText("Sync Completed with Errors")
			} else {
				ps.titleLabel.SetText("Sync Complete")
			}
		}
		if ps.extras.finishedMessage != "" {
			ps.finishedMsgLabel.SetText(ps.extras.finishedMessage)
			ps.finishedMsgLabel.Show()
		} else {
			ps.finishedMsgLabel.Hide()
		}
		ps.overallBarInf.Hide()
		ps.overallBar.Show()
		ps.overallBar.SetValue(1.0)
		ps.scanBtn.Hide()
		ps.syncBtn.Hide()
		ps.resolveBtn.Hide()
		ps.cancelBtn.Hide()
		ps.backBtn.SetText("Done")
		ps.backBtn.Importance = widget.MediumImportance
		ps.backBtn.Refresh()
		ps.backBtn.Enable()
	case phaseSyncCancelled:
		ps.titleLabel.SetText("Sync Cancelled")
		ps.overallBarInf.Hide()
		ps.overallBar.Show()
		ps.scanBtn.Hide()
		ps.syncBtn.Show()
		ps.syncBtn.Enable()
		ps.resolveBtn.Hide()
		ps.cancelBtn.Hide()
		ps.backBtn.Enable()
	}
}

// renderMetrics writes computeSyncMetrics' aggregate into the three metric
// labels (and, once syncing, the overall progress bar), using whichever
// breakdown is meaningful for the current phase.
func (ps *progressScreen) renderMetrics(m syncMetrics) {
	switch ps.phase {
	case phaseScanRunning:
		ps.expValue.SetText(fmt.Sprintf("%d / %d", m.totalExpsDone, m.totalExps))
		if m.totalConflicts > 0 {
			ps.filesValue.SetText(fmt.Sprintf("%d (⚠ %d conflicts)", m.totalFiles, m.totalConflicts))
		} else {
			ps.filesValue.SetText(fmt.Sprintf("%d", m.totalFiles))
		}
		ps.bytesValue.SetText(humanBytes(m.totalBytes))
	case phaseScanComplete, phaseScanCancelled:
		// Pre-sync, copyFilesTotal/totalFilesDone/totalConflicts already are
		// exactly the copy/skip/conflict breakdown (no folder/file bytesDone
		// has been touched by a sync yet), so no second pass is needed here.
		ps.expValue.SetText(fmt.Sprintf("%d", m.totalExps))
		copyFiles, skipFiles, conflictFiles := m.copyFilesTotal, m.totalFilesDone, m.totalConflicts
		copyBytes, skipBytes := m.copyBytesTotal, m.totalBytesDone
		if ps.extras.quickScan {
			// Quick scan never verified content — it only knows a path is
			// missing somewhere or present everywhere — so it must not
			// claim anything is "synced", only what's queued to copy.
			ps.filesValue.SetText(fmt.Sprintf("%d to add", copyFiles))
			ps.bytesValue.SetText(fmt.Sprintf("%s to add", humanBytes(copyBytes)))
		} else if conflictFiles > 0 {
			ps.filesValue.SetText(fmt.Sprintf("%d unsynced / %d synced / %d conflicts", copyFiles, skipFiles, conflictFiles))
			ps.bytesValue.SetText(fmt.Sprintf("%s unsynced / %s synced", humanBytes(copyBytes), humanBytes(skipBytes)))
		} else {
			ps.filesValue.SetText(fmt.Sprintf("%d unsynced / %d synced", copyFiles, skipFiles))
			ps.bytesValue.SetText(fmt.Sprintf("%s unsynced / %s synced", humanBytes(copyBytes), humanBytes(skipBytes)))
		}
	default:
		ps.expValue.SetText(fmt.Sprintf("%d / %d", m.totalExpsDone, m.totalExps))
		if ps.extras.quickScan {
			ps.filesValue.SetText(fmt.Sprintf("%d / %d", m.copyFilesDone, m.copyFilesTotal))
			ps.bytesValue.SetText(fmt.Sprintf("%s / %s", humanBytes(m.copyBytesDone), humanBytes(m.copyBytesTotal)))
		} else {
			skipFilesTotal := m.totalFiles - m.copyFilesTotal
			skipBytesTotal := m.totalBytes - m.copyBytesTotal
			ps.filesValue.SetText(fmt.Sprintf("%d / %d\n(%d already synced)", m.copyFilesDone, m.copyFilesTotal, skipFilesTotal))
			ps.bytesValue.SetText(fmt.Sprintf("%s / %s\n(%s already synced)", humanBytes(m.copyBytesDone), humanBytes(m.copyBytesTotal), humanBytes(skipBytesTotal)))
		}
		// While actively syncing, never show 100% from the byte ratio alone —
		// bytes can finish transferring before the sync is actually confirmed
		// done (post-transfer verification/cleanup still has to run). Hold
		// the bar at 99% until phaseSyncComplete flips it to 1.0 for real.
		switch {
		case ps.phase != phaseSyncing:
			if m.copyBytesTotal > 0 {
				ps.overallBar.SetValue(float64(m.copyBytesDone) / float64(m.copyBytesTotal))
			} else {
				ps.overallBar.SetValue(1.0)
			}
		case m.copyBytesTotal > 0:
			v := float64(m.copyBytesDone) / float64(m.copyBytesTotal)
			if v > 0.99 {
				v = 0.99
			}
			ps.overallBar.SetValue(v)
		default:
			ps.overallBar.SetValue(0.99)
		}
	}
}

// refreshLists syncs the error banner and re-renders the experiment list
// plus, when the selected experiment's folders aren't stable yet (still
// scanning) or nothing is selected, the folders/files lists too.
func (ps *progressScreen) refreshLists() {
	ps.syncErrorLabelForSelection()

	ps.expList.Refresh()
	// Only refresh foldList & fileList if the selected exp is static or
	// changed. If we are in scan running and it's active, refresh.
	if ps.selectedExpIdx >= 0 && ps.selectedExpIdx < len(ps.expStates) {
		exp := ps.expStates[ps.selectedExpIdx]
		if len(exp.folders) == 0 {
			ps.refreshFolders()
			ps.refreshFiles()
		}
	} else {
		ps.refreshFolders()
		ps.refreshFiles()
	}
}

// refreshUI re-renders the whole screen from current state: phase chrome
// (title/buttons/bars), the three metric tiles, and the experiment/folder/
// file lists.
func (ps *progressScreen) refreshUI() {
	ps.applyPhaseChrome()
	ps.renderMetrics(computeSyncMetrics(ps.expStates))
	ps.refreshLists()
}
