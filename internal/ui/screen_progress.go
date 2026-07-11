package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"path"
	"sort"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// maxConcurrentTasks bounds how many (experiment, destination) scan/sync
// tasks run at once. Tasks are independent (each opens its own src/dst Fs),
// so running them concurrently means a slow cloud destination no longer
// blocks a fast local one. Capped rather than unbounded to avoid hammering
// a single remote's API with too many simultaneous listings/transfers.
const maxConcurrentTasks = 4

type syncPhase int

const (
	phaseScanRunning syncPhase = iota
	phaseScanComplete
	phaseScanCancelled
	phaseSyncing
	phaseSyncComplete
	phaseSyncCancelled
)

type uiJobStatus int

const (
	statusWaiting uiJobStatus = iota
	statusRunning
	statusDone
	statusError
	statusCanceled
)

type fileUIState struct {
	relPath        string
	name           string
	size           int64
	bytesDone      int64
	done           bool
	err            error
	hasError       bool
	action         syncengine.ScanAction
	dstSize        int64
	conflictReason string
}

type folderUIState struct {
	path       string
	totalBytes int64
	bytesDone  int64
	totalFiles int
	filesDone  int
	hasError   bool
	files      []*fileUIState

	// copy* track only files with action == ActionCopy (the remaining work),
	// excluding already-synced files entirely. Used to show remaining
	// progress during an active sync rather than including pre-done files.
	copyTotalBytes int64
	copyBytesDone  int64
	copyTotalFiles int
	copyFilesDone  int
}

type expUIState struct {
	label       string
	status      uiJobStatus
	err         error
	totalBytes  int64
	bytesDone   int64
	hasError    bool
	folders     []*folderUIState
	fileMap     map[string]*fileUIState
	tempFolders []syncengine.ScanDirProgress
	tempRecent  []syncengine.ScanEntry

	// copy* mirror folderUIState's remaining-work aggregates, summed across
	// folders.
	copyTotalBytes int64
	copyBytesDone  int64
	copyTotalFiles int
	copyFilesDone  int
}

// barRow is one rendered row in a split sub-panel (Files or Folders): a real
// item or a divider banner (used for the "N files not shown" notice when a
// list is capped).
type barRow struct {
	divider  bool
	label    string
	summary  string
	progress float64
	err      error
	hasError bool
	isFolder bool
	gray     bool    // render with a permanent grey wash (already-synced items)
	refIdx   int     // index this row maps back to (folder rows only)
	fade     float64 // 0 fully visible … 1 invisible (trailing rows of a capped list)
	// conflictRelPath, when non-empty, marks this row as a scanned conflict
	// the user can click to open the N-way resolver at (N-way sessions only,
	// and only once the owning experiment's scan has completed).
	conflictRelPath string
}

// isFullySkipped reports whether every file in the folder was already present
// and identical at the destination (nothing to copy). Such folders are shown
// in the "Already synced" section and stay grey.
func (f *folderUIState) isFullySkipped() bool {
	for _, file := range f.files {
		if file.action != syncengine.ActionSkipIdentical {
			return false
		}
	}
	return len(f.files) > 0
}

type progressItemLayout struct {
	percent float64
}

func (l *progressItemLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) >= 3 {
		return objects[2].MinSize()
	}
	return fyne.NewSize(100, 32)
}

func (l *progressItemLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) < 3 {
		return
	}
	bg := objects[0]
	fill := objects[1]
	content := objects[2]

	bg.Resize(size)
	bg.Move(fyne.NewPos(0, 0))

	fillWidth := float32(float64(size.Width) * l.percent)
	if fillWidth < 0 {
		fillWidth = 0
	}
	if fillWidth > size.Width {
		fillWidth = size.Width
	}
	fill.Resize(fyne.NewSize(fillWidth, size.Height))
	fill.Move(fyne.NewPos(0, 0))

	content.Resize(size)
	content.Move(fyne.NewPos(0, 0))

	// Optional fade overlay (objects[3]) covers the whole row.
	if len(objects) >= 4 {
		objects[3].Resize(size)
		objects[3].Move(fyne.NewPos(0, 0))
	}

	// Selection outline (objects[4]) sits on top of everything so the fill
	// and fade never occlude it.
	if len(objects) >= 5 {
		objects[4].Resize(size)
		objects[4].Move(fyne.NewPos(0, 0))
	}
}

func createBackingBarItem() fyne.CanvasObject {
	bg := canvas.NewRectangle(color.White)
	fill := canvas.NewRectangle(color.Transparent)

	nameLabel := widget.NewLabel("")
	nameLabel.Truncation = fyne.TextTruncateEllipsis

	summaryLabel := widget.NewLabel("")
	summaryLabel.Alignment = fyne.TextAlignTrailing

	// errBtn is hidden by default; updateBackingBarItem shows it when there
	// is an error and wires OnTapped to open the error detail modal.
	errBtn := widget.NewButtonWithIcon("", theme.ErrorIcon(), nil)
	errBtn.Importance = widget.DangerImportance
	errBtn.Hide()

	trailing := container.NewHBox(errBtn, summaryLabel)
	content := container.NewBorder(nil, nil, nil, trailing, nameLabel)
	paddedContent := container.NewPadded(content)

	// fade sits on top of everything; setItemFade tints it to fade a row out.
	fade := canvas.NewRectangle(color.Transparent)

	// selectionBorder is drawn last so the selection outline is never hidden
	// behind the progress fill or fade overlay.
	selectionBorder := canvas.NewRectangle(color.Transparent)
	selectionBorder.StrokeWidth = 0

	itemLayout := &progressItemLayout{percent: 0.0}
	item := container.New(itemLayout, bg, fill, paddedContent, fade, selectionBorder)
	return item
}

func updateBackingBarItem(obj fyne.CanvasObject, labelText, summaryText string, progress float64, itemErr error, hasError bool, isFolder bool, isSelected bool, win fyne.Window) {
	containerObj := obj.(*fyne.Container)
	bg := containerObj.Objects[0].(*canvas.Rectangle)
	fill := containerObj.Objects[1].(*canvas.Rectangle)

	paddedContent := containerObj.Objects[2].(*fyne.Container)
	borderContainer := paddedContent.Objects[0].(*fyne.Container)
	nameLabel := borderContainer.Objects[0].(*widget.Label)
	trailing := borderContainer.Objects[1].(*fyne.Container)
	errBtn := trailing.Objects[0].(*widget.Button)
	summaryLabel := trailing.Objects[1].(*widget.Label)

	nameLabel.SetText(labelText)
	summaryLabel.SetText(summaryText)
	if itemErr != nil {
		errText := itemErr.Error()
		errBtn.OnTapped = func() { showErrorModal(win, errText) }
		errBtn.Show()
	} else {
		errBtn.OnTapped = nil
		errBtn.Hide()
	}

	layout := containerObj.Layout.(*progressItemLayout)
	layout.percent = progress

	var bgColor, fillColor color.Color

	if itemErr != nil || (hasError && !isFolder) {
		bgColor = color.NRGBA{R: 254, G: 226, B: 226, A: 255}
		fillColor = color.NRGBA{R: 252, G: 165, B: 165, A: 255}
	} else if hasError {
		bgColor = color.NRGBA{R: 254, G: 243, B: 199, A: 255}
		fillColor = color.NRGBA{R: 253, G: 186, B: 116, A: 255}
	} else {
		bgColor = color.White
		if progress >= 1.0 {
			fillColor = color.NRGBA{R: 147, G: 197, B: 253, A: 255}
			bgColor = fillColor
		} else {
			fillColor = color.NRGBA{R: 219, G: 234, B: 254, A: 255}
		}
	}

	bg.FillColor = bgColor
	fill.FillColor = fillColor

	if len(containerObj.Objects) >= 5 {
		selectionBorder := containerObj.Objects[4].(*canvas.Rectangle)
		if isSelected {
			selectionBorder.StrokeColor = color.NRGBA{R: 59, G: 130, B: 246, A: 255}
			selectionBorder.StrokeWidth = 2
		} else {
			selectionBorder.StrokeWidth = 0
		}
		selectionBorder.Refresh()
	}

	setItemFade(obj, 0)

	bg.Refresh()
	fill.Refresh()
	containerObj.Refresh()
}

// updateDividerItem restyles a backing-bar item as a section divider (a muted
// grey banner with centered text and no progress fill or summary).
func updateDividerItem(obj fyne.CanvasObject, text string) {
	containerObj := obj.(*fyne.Container)
	bg := containerObj.Objects[0].(*canvas.Rectangle)
	fill := containerObj.Objects[1].(*canvas.Rectangle)

	paddedContent := containerObj.Objects[2].(*fyne.Container)
	borderContainer := paddedContent.Objects[0].(*fyne.Container)
	nameLabel := borderContainer.Objects[0].(*widget.Label)
	trailing := borderContainer.Objects[1].(*fyne.Container)
	errBtn := trailing.Objects[0].(*widget.Button)
	summaryLabel := trailing.Objects[1].(*widget.Label)

	nameLabel.SetText(text)
	summaryLabel.SetText("")
	errBtn.OnTapped = nil
	errBtn.Hide()

	layout := containerObj.Layout.(*progressItemLayout)
	layout.percent = 0

	bg.FillColor = color.NRGBA{R: 229, G: 231, B: 235, A: 255}
	bg.StrokeWidth = 0
	fill.FillColor = color.Transparent

	setItemFade(obj, 0)

	bg.Refresh()
	fill.Refresh()
	containerObj.Refresh()
}

// setItemFade tints a backing-bar item's top overlay (objects[3]) toward the
// pane background. fade is 0 (fully visible) to 1 (invisible).
func setItemFade(obj fyne.CanvasObject, fade float64) {
	containerObj := obj.(*fyne.Container)
	if len(containerObj.Objects) < 4 {
		return
	}
	overlay := containerObj.Objects[3].(*canvas.Rectangle)
	if fade <= 0 {
		overlay.FillColor = color.Transparent
	} else {
		if fade > 1 {
			fade = 1
		}
		overlay.FillColor = color.NRGBA{R: 240, G: 242, B: 245, A: uint8(fade * 255)}
	}
	overlay.Refresh()
}

// tintItemBg overrides a backing-bar item's background colour. Used to give
// already-synced file rows a light grey wash so they read as distinct from
// to-sync rows. Call after updateBackingBarItem (which sets the base colour).
func tintItemBg(obj fyne.CanvasObject, c color.Color) {
	containerObj := obj.(*fyne.Container)
	bg := containerObj.Objects[0].(*canvas.Rectangle)
	bg.FillColor = c
	bg.Refresh()
}

func buildExpUIState(label string, result syncengine.ScanResult) *expUIState {
	exp := &expUIState{
		label:   label,
		status:  statusWaiting,
		fileMap: make(map[string]*fileUIState),
	}

	dirsByPath := make(map[string][]*fileUIState)
	for _, entry := range result.Entries {
		dir := path.Dir(entry.RelPath)
		if dir == "." {
			dir = "."
		}

		done := entry.Action == syncengine.ActionSkipIdentical
		var bytesDone int64
		if done {
			bytesDone = entry.Size
		}

		fileState := &fileUIState{
			relPath:        entry.RelPath,
			name:           path.Base(entry.RelPath),
			size:           entry.Size,
			bytesDone:      bytesDone,
			done:           done,
			action:         entry.Action,
			dstSize:        entry.DstSize,
			conflictReason: entry.ConflictReason,
		}

		exp.fileMap[entry.RelPath] = fileState
		dirsByPath[dir] = append(dirsByPath[dir], fileState)
	}

	for dirPath, files := range dirsByPath {
		folder := &folderUIState{
			path: dirPath,
		}
		for _, f := range files {
			folder.totalBytes += f.size
			folder.totalFiles++
			if f.done {
				folder.bytesDone += f.size
				folder.filesDone++
			}
			if f.action == syncengine.ActionCopy {
				folder.copyTotalBytes += f.size
				folder.copyTotalFiles++
				if f.done {
					folder.copyBytesDone += f.size
					folder.copyFilesDone++
				}
			}
			folder.files = append(folder.files, f)
		}
		sort.SliceStable(folder.files, func(i, j int) bool {
			if folder.files[i].action != folder.files[j].action {
				return folder.files[i].action == syncengine.ActionCopy
			}
			return folder.files[i].relPath < folder.files[j].relPath
		})
		exp.folders = append(exp.folders, folder)
	}

	sort.Slice(exp.folders, func(i, j int) bool {
		return exp.folders[i].path < exp.folders[j].path
	})

	for _, f := range exp.folders {
		exp.totalBytes += f.totalBytes
		exp.bytesDone += f.bytesDone
		exp.copyTotalBytes += f.copyTotalBytes
		exp.copyBytesDone += f.copyBytesDone
		exp.copyTotalFiles += f.copyTotalFiles
		exp.copyFilesDone += f.copyFilesDone
	}

	return exp
}

func createColumn(title string, content fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 240, G: 242, B: 245, A: 255})
	bg.StrokeWidth = 1
	bg.StrokeColor = color.NRGBA{R: 218, G: 220, B: 224, A: 255}
	bg.CornerRadius = 8

	titleLabel := widget.NewLabelWithStyle(title, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	headerBg := canvas.NewRectangle(color.NRGBA{R: 209, G: 213, B: 219, A: 255})
	header := container.NewStack(headerBg, container.NewPadded(titleLabel))

	colContent := container.NewBorder(
		container.NewVBox(header, widget.NewSeparator()),
		nil, nil, nil,
		content,
	)

	return container.NewStack(bg, colContent)
}

// sectionHeader is a heavy-weight banner that labels a Files sub-panel
// ("Current Sync" / "Already synced"). It sits above its panel's list, so it
// stays fixed while the list scrolls.
func sectionHeader(title string) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 240, G: 242, B: 245, A: 255})
	label := widget.NewLabelWithStyle(title, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	return container.NewStack(bg, container.NewPadded(label))
}

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
}

func showSyncFlow(s *state, tasks []scanTask, onBack func()) {
	showSyncFlowExtras(s, tasks, onBack, syncFlowExtras{})
}

func showSyncFlowExtras(s *state, tasks []scanTask, onBack func(), extras syncFlowExtras) {
	phase := phaseScanRunning
	selectedExpIdx := -1
	selectedFoldIdx := -1

	expStates := make([]*expUIState, len(tasks))
	for i, t := range tasks {
		expStates[i] = &expUIState{
			label:  t.Label,
			status: statusWaiting,
		}
	}

	var activeCancel context.CancelFunc

	titleLabel := widget.NewLabelWithStyle("Scanning...", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	speedLabel := widget.NewLabel("")
	speedLabel.Hide()

	// retryLabel surfaces transient copy errors (dropped connections,
	// timeouts) that rclone is retrying on its own - see
	// syncengine.ProgressSnapshot.Retrying. It's amber, not red: these
	// aren't failures yet, and the progress bar keeps running normally
	// underneath while a retry is pending.
	retryLabel := canvas.NewText("", color.NRGBA{R: 217, G: 119, B: 6, A: 255})
	retryLabel.TextStyle = fyne.TextStyle{Bold: true}
	retryLabel.Hide()

	overallBar := widget.NewProgressBar()
	overallBarInf := widget.NewProgressBarInfinite()

	expValue := widget.NewLabelWithStyle("0 / 0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	filesValue := widget.NewLabelWithStyle("0 / 0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	bytesValue := widget.NewLabelWithStyle("0 B / 0 B", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	expBlurb := metricPanel("Experiments", expValue, color.NRGBA{R: 232, G: 240, B: 254, A: 255})
	filesBlurb := metricPanel("Files", filesValue, color.NRGBA{R: 255, G: 239, B: 219, A: 255})
	bytesBlurb := metricPanel("Bytes", bytesValue, color.NRGBA{R: 243, G: 232, B: 255, A: 255})

	metrics := container.NewGridWithColumns(3, expBlurb, filesBlurb, bytesBlurb)

	errorLabel := widget.NewLabel("")
	errorLabel.Wrapping = fyne.TextWrapWord
	errorLabel.Hide()

	// isSyncing reports whether a real copy has run (or is running). Progress
	// bars only fill during/after an actual sync; a scan leaves them white
	// because nothing has been transferred yet.
	isSyncing := func() bool {
		return phase == phaseSyncing || phase == phaseSyncComplete || phase == phaseSyncCancelled
	}

	var expList *widget.List

	expList = widget.NewList(
		func() int { return len(expStates) },
		func() fyne.CanvasObject { return createBackingBarItem() },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			exp := expStates[id]
			prog := 0.0
			if isSyncing() {
				if exp.copyTotalBytes > 0 {
					prog = float64(exp.copyBytesDone) / float64(exp.copyTotalBytes)
				} else {
					prog = 1.0
				}
			} else if exp.totalBytes > 0 {
				prog = float64(exp.bytesDone) / float64(exp.totalBytes)
			}
			summary := fmt.Sprintf("%d%%", int(prog*100))
			isSelected := selectedExpIdx == int(id)
			updateBackingBarItem(obj, exp.label, summary, prog, exp.err, exp.hasError, false, isSelected, s.win)
		},
	)

	// tempEntriesForFolder returns the subset of exp.tempRecent whose
	// directory matches the currently selected tempFolder.
	tempEntriesForFolder := func(exp *expUIState) []syncengine.ScanEntry {
		if selectedFoldIdx < 0 || selectedFoldIdx >= len(exp.tempFolders) {
			return nil
		}
		folderPath := exp.tempFolders[selectedFoldIdx].Path
		var filtered []syncengine.ScanEntry
		for _, e := range exp.tempRecent {
			if path.Dir(e.RelPath) == folderPath {
				filtered = append(filtered, e)
			}
		}
		return filtered
	}

	// capAndFade limits a group to maxFileRows rows: a folder can hold
	// thousands of files the user won't scroll, so we show the first rows,
	// fade the trailing ones out, and append a "N files not shown" banner.
	capAndFade := func(rows []barRow) []barRow {
		const maxFileRows = 100
		const fadeCount = 15
		if len(rows) <= maxFileRows {
			return rows
		}
		hidden := len(rows) - maxFileRows
		rows = rows[:maxFileRows:maxFileRows]
		for i := range rows {
			remaining := len(rows) - 1 - i
			if remaining < fadeCount {
				rows[i].fade = 0.85 * float64(fadeCount-remaining) / float64(fadeCount)
			}
		}
		return append(rows, barRow{divider: true, label: fmt.Sprintf("%d files not shown", hidden)})
	}

	// computeFileRows splits the selected folder's files into to-sync and
	// already-synced groups. Works from live scan data (tempRecent) before
	// completion and from exp.folders afterwards. Already-synced rows carry
	// gray=true and no fill so they never turn blue.
	// fileSyncRank orders the Current Sync list while a sync is running:
	// actively-transferring files first, not-yet-started files in the
	// middle, and files that have already finished at the bottom.
	fileSyncRank := func(f *fileUIState) int {
		switch {
		case f.done:
			return 2
		case f.bytesDone > 0:
			return 0
		default:
			return 1
		}
	}

	computeFileRows := func() (unsynced, synced []barRow) {
		if selectedExpIdx < 0 || selectedExpIdx >= len(expStates) {
			return nil, nil
		}
		exp := expStates[selectedExpIdx]
		if len(exp.folders) > 0 {
			if selectedFoldIdx < 0 || selectedFoldIdx >= len(exp.folders) {
				return nil, nil
			}
			folderFiles := exp.folders[selectedFoldIdx].files
			if isSyncing() {
				sorted := make([]*fileUIState, len(folderFiles))
				copy(sorted, folderFiles)
				sort.SliceStable(sorted, func(i, j int) bool {
					return fileSyncRank(sorted[i]) < fileSyncRank(sorted[j])
				})
				folderFiles = sorted
			}
			for _, f := range folderFiles {
				if f.action == syncengine.ActionSkipIdentical {
					synced = append(synced, barRow{
						label:   f.name,
						summary: humanBytes(f.size),
						gray:    true,
					})
					continue
				}
				if f.action == syncengine.ActionConflict {
					summary := fmt.Sprintf("⚠ conflict — %s", f.conflictReason)
					relPath := ""
					if extras.nway != nil {
						summary = extras.nway.rowSummary(exp.label, f.relPath, f.conflictReason)
						relPath = f.relPath
					}
					unsynced = append(unsynced, barRow{
						label:           f.name,
						summary:         summary,
						conflictRelPath: relPath,
					})
					continue
				}
				prog := 0.0
				if isSyncing() && f.size > 0 {
					prog = float64(f.bytesDone) / float64(f.size)
				}
				unsynced = append(unsynced, barRow{
					label:    f.name,
					summary:  fmt.Sprintf("%s / %s", humanBytes(f.bytesDone), humanBytes(f.size)),
					progress: prog,
					err:      f.err,
					hasError: f.hasError,
				})
			}
		} else {
			for _, e := range tempEntriesForFolder(exp) {
				switch e.Action {
				case syncengine.ActionSkipIdentical:
					synced = append(synced, barRow{label: path.Base(e.RelPath), summary: humanBytes(e.Size), gray: true})
				case syncengine.ActionConflict:
					unsynced = append(unsynced, barRow{label: path.Base(e.RelPath), summary: fmt.Sprintf("⚠ conflict — %s", e.ConflictReason)})
				default:
					unsynced = append(unsynced, barRow{label: path.Base(e.RelPath), summary: humanBytes(e.Size)})
				}
			}
		}
		return capAndFade(unsynced), capAndFade(synced)
	}

	// computeFolderRows splits the selected experiment's folders into unsynced
	// (has files to copy) and already-synced (all files identical) groups. The
	// split is only known once exp.folders is populated (scan complete); while
	// the scan is still running, tempFolders all show in the unsynced group.
	// refIdx maps each row back to its folder index so selection works.
	computeFolderRows := func() (unsynced, synced []barRow) {
		if selectedExpIdx < 0 || selectedExpIdx >= len(expStates) {
			return nil, nil
		}
		exp := expStates[selectedExpIdx]
		if len(exp.folders) > 0 {
			for i, fold := range exp.folders {
				if fold.isFullySkipped() {
					synced = append(synced, barRow{
						label:    fold.path,
						summary:  fmt.Sprintf("%d / %d files", fold.filesDone, fold.totalFiles),
						isFolder: true,
						gray:     true,
						refIdx:   i,
					})
					continue
				}
				filesDone, filesTotal := fold.filesDone, fold.totalFiles
				if isSyncing() {
					filesDone, filesTotal = fold.copyFilesDone, fold.copyTotalFiles
				}
				prog := 0.0
				if filesTotal > 0 {
					prog = float64(filesDone) / float64(filesTotal)
				}
				summary := fmt.Sprintf("%d / %d files", filesDone, filesTotal)
				conflicts := 0
				for _, f := range fold.files {
					if f.action == syncengine.ActionConflict {
						conflicts++
					}
				}
				if conflicts > 0 {
					summary = fmt.Sprintf("⚠ %d · %s", conflicts, summary)
				}
				unsynced = append(unsynced, barRow{
					label:    fold.path,
					summary:  summary,
					progress: prog,
					hasError: fold.hasError,
					isFolder: true,
					refIdx:   i,
				})
			}
		} else {
			for i, row := range exp.tempFolders {
				total := row.CopyCount + row.SkipCount + row.ConflictCount
				summary := fmt.Sprintf("%d / %d files", row.SkipCount, total)
				if row.ConflictCount > 0 {
					summary = fmt.Sprintf("⚠ %d · %s", row.ConflictCount, summary)
				}
				unsynced = append(unsynced, barRow{
					label:    row.Path,
					summary:  summary,
					isFolder: true,
					refIdx:   i,
				})
			}
		}
		return unsynced, synced
	}

	// makeBarList builds a list backed by a *[]barRow. isSelected (may be nil)
	// decides which rows get the selection stroke; gray rows get a permanent
	// grey wash.
	makeBarList := func(rows *[]barRow, isSelected func(barRow) bool) *widget.List {
		return widget.NewList(
			func() int { return len(*rows) },
			func() fyne.CanvasObject { return createBackingBarItem() },
			func(id widget.ListItemID, obj fyne.CanvasObject) {
				if int(id) < 0 || int(id) >= len(*rows) {
					return
				}
				row := (*rows)[id]
				if row.divider {
					updateDividerItem(obj, row.label)
					return
				}
				sel := false
				if isSelected != nil {
					sel = isSelected(row)
				}
				updateBackingBarItem(obj, row.label, row.summary, row.progress, row.err, row.hasError, row.isFolder, sel, s.win)
				setItemFade(obj, row.fade)
				if row.gray {
					tintItemBg(obj, color.NRGBA{R: 243, G: 244, B: 246, A: 255})
				}
			},
		)
	}

	// buildSplit wires two section lists into a VSplit and returns a function
	// that shows one or both panels depending on which groups have rows.
	buildSplit := func(unsyncedList, syncedList *widget.List) (fyne.CanvasObject, func(hasUnsynced, hasSynced bool)) {
		unsyncedPanel := container.NewBorder(sectionHeader("Current Sync"), nil, nil, nil, unsyncedList)
		syncedPanel := container.NewBorder(sectionHeader("Already synced"), nil, nil, nil, syncedList)
		split := container.NewVSplit(unsyncedPanel, syncedPanel)
		split.SetOffset(0.5)
		applyMode := func(hasUnsynced, hasSynced bool) {
			switch {
			case hasUnsynced && hasSynced:
				unsyncedPanel.Show()
				syncedPanel.Show()
				split.SetOffset(0.5)
			case hasSynced:
				unsyncedPanel.Hide()
				syncedPanel.Show()
				split.SetOffset(0.0)
			default:
				unsyncedPanel.Show()
				syncedPanel.Hide()
				split.SetOffset(1.0)
			}
		}
		return split, applyMode
	}

	// Files column.
	var fileUnsyncedRows, fileSyncedRows []barRow
	fileUnsyncedList := makeBarList(&fileUnsyncedRows, nil)
	fileSyncedList := makeBarList(&fileSyncedRows, nil)
	filesSplit, applyFilesMode := buildSplit(fileUnsyncedList, fileSyncedList)

	if extras.nway != nil {
		// Clicking a conflict row opens the resolver at that file (rows only
		// carry conflictRelPath once their experiment's scan has completed).
		fileUnsyncedList.OnSelected = func(id widget.ListItemID) {
			defer fileUnsyncedList.UnselectAll()
			if int(id) < 0 || int(id) >= len(fileUnsyncedRows) {
				return
			}
			row := fileUnsyncedRows[id]
			if row.conflictRelPath == "" || selectedExpIdx < 0 || selectedExpIdx >= len(expStates) {
				return
			}
			key := nwayConflictKey{expName: expStates[selectedExpIdx].label, relPath: row.conflictRelPath}
			showNWayResolveDialog(s, extras.nway, &key)
		}
	}

	refreshFiles := func() {
		fileUnsyncedRows, fileSyncedRows = computeFileRows()
		applyFilesMode(len(fileUnsyncedRows) > 0, len(fileSyncedRows) > 0)
		fileUnsyncedList.Refresh()
		fileSyncedList.Refresh()
	}

	// Folders column.
	var foldUnsyncedRows, foldSyncedRows []barRow
	foldSelected := func(r barRow) bool { return r.refIdx == selectedFoldIdx }
	foldUnsyncedList := makeBarList(&foldUnsyncedRows, foldSelected)
	foldSyncedList := makeBarList(&foldSyncedRows, foldSelected)
	foldSplit, applyFoldMode := buildSplit(foldUnsyncedList, foldSyncedList)

	refreshFolders := func() {
		foldUnsyncedRows, foldSyncedRows = computeFolderRows()
		applyFoldMode(len(foldUnsyncedRows) > 0, len(foldSyncedRows) > 0)
		foldUnsyncedList.Refresh()
		foldSyncedList.Refresh()
	}

	foldSelect := func(rows *[]barRow, other *widget.List) func(widget.ListItemID) {
		return func(id widget.ListItemID) {
			if int(id) < 0 || int(id) >= len(*rows) {
				return
			}
			other.UnselectAll()
			selectedFoldIdx = (*rows)[id].refIdx
			refreshFolders()
			refreshFiles()
		}
	}
	foldUnsyncedList.OnSelected = foldSelect(&foldUnsyncedRows, foldSyncedList)
	foldSyncedList.OnSelected = foldSelect(&foldSyncedRows, foldUnsyncedList)

	expList.OnSelected = func(id widget.ListItemID) {
		selectedExpIdx = int(id)
		selectedFoldIdx = 0
		foldUnsyncedList.UnselectAll()
		foldSyncedList.UnselectAll()
		expList.Refresh()
		refreshFolders()
		refreshFiles()
		if selectedExpIdx >= 0 && selectedExpIdx < len(expStates) {
			exp := expStates[selectedExpIdx]
			if exp.err != nil {
				errorLabel.SetText(fmt.Sprintf("Error in %s: %s", exp.label, exp.err.Error()))
				errorLabel.Show()
			} else {
				errorLabel.Hide()
			}
		} else {
			errorLabel.Hide()
		}
	}

	columns := container.NewGridWithColumns(3,
		createColumn("Experiments", expList),
		createColumn("Folders", foldSplit),
		createColumn("Files", filesSplit),
	)

	var cancelBtn, backBtn, syncBtn, scanBtn, resolveBtn *widget.Button

	refreshUI := func() {
		switch phase {
		case phaseScanRunning:
			titleLabel.SetText("Scanning...")
			overallBar.Hide()
			overallBarInf.Show()
			overallBarInf.Start()
			syncBtn.Hide()
			scanBtn.Hide()
			resolveBtn.Hide()
			cancelBtn.Show()
			cancelBtn.Enable()
			backBtn.Disable()
		case phaseScanComplete:
			titleLabel.SetText("Ready to Sync")
			overallBarInf.Stop()
			overallBarInf.Hide()
			overallBar.Hide()
			scanBtn.Hide()
			syncBtn.Show()
			var totalCopyToSync int
			for _, e := range expStates {
				for _, f := range e.folders {
					for _, file := range f.files {
						if file.action == syncengine.ActionCopy {
							totalCopyToSync++
						}
					}
				}
			}
			if totalCopyToSync > 0 {
				syncBtn.Enable()
			} else {
				syncBtn.Disable()
			}
			resolveBtn.Hide()
			if extras.nway != nil {
				// Sync stays unreachable until every conflict carries an
				// explicit resolution — there is deliberately no default.
				unresolved := extras.nway.unresolvedCount()
				switch {
				case unresolved > 0:
					titleLabel.SetText(fmt.Sprintf("Scan complete — %d conflict(s) to resolve", unresolved))
					syncBtn.Hide()
					resolveBtn.SetText(fmt.Sprintf("Resolve %d conflict(s)…", unresolved))
					resolveBtn.Show()
				case extras.nway.conflictCount() > 0:
					resolveBtn.SetText("Review conflict resolutions")
					resolveBtn.Show()
					fallthrough
				default:
					// Overwrite/rename/delete resolutions are real work even
					// when the scan itself found nothing to copy.
					if extras.nway.hasActionable() {
						syncBtn.Enable()
					}
				}
			}
			cancelBtn.Hide()
			backBtn.Enable()
		case phaseScanCancelled:
			titleLabel.SetText("Scan Cancelled")
			overallBarInf.Stop()
			overallBarInf.Hide()
			overallBar.Hide()
			syncBtn.Hide()
			resolveBtn.Hide()
			scanBtn.Show()
			scanBtn.Enable()
			cancelBtn.Hide()
			backBtn.Enable()
		case phaseSyncing:
			titleLabel.SetText("Syncing")
			overallBarInf.Hide()
			overallBar.Show()
			scanBtn.Hide()
			syncBtn.Hide()
			resolveBtn.Hide()
			cancelBtn.Show()
			cancelBtn.Enable()
			backBtn.Disable()
		case phaseSyncComplete:
			var hasAnyErrors bool
			for _, e := range expStates {
				if e.hasError {
					hasAnyErrors = true
					break
				}
			}
			if hasAnyErrors {
				titleLabel.SetText("Sync Completed with Errors")
			} else {
				titleLabel.SetText("Sync Complete")
			}
			overallBarInf.Hide()
			overallBar.Show()
			overallBar.SetValue(1.0)
			scanBtn.Hide()
			syncBtn.Hide()
			resolveBtn.Hide()
			cancelBtn.Hide()
			backBtn.SetText("Done")
			backBtn.Enable()
		case phaseSyncCancelled:
			titleLabel.SetText("Sync Cancelled")
			overallBarInf.Hide()
			overallBar.Show()
			scanBtn.Hide()
			syncBtn.Show()
			syncBtn.Enable()
			resolveBtn.Hide()
			cancelBtn.Hide()
			backBtn.Enable()
		}

		var totalExpsDone, totalExps int
		var totalFilesDone, totalFiles int
		var totalBytesDone, totalBytes int64
		var copyFilesDone, copyFilesTotal int
		var copyBytesDone, copyBytesTotal int64
		var totalConflicts int

		totalExps = len(expStates)
		for _, e := range expStates {
			if e.status == statusDone || e.status == statusError || e.status == statusCanceled {
				totalExpsDone++
			}
			if len(e.folders) > 0 {
				for _, fold := range e.folders {
					totalFiles += fold.totalFiles
					totalFilesDone += fold.filesDone
					totalBytes += fold.totalBytes
					totalBytesDone += fold.bytesDone
					copyFilesTotal += fold.copyTotalFiles
					copyFilesDone += fold.copyFilesDone
					copyBytesTotal += fold.copyTotalBytes
					copyBytesDone += fold.copyBytesDone
					for _, file := range fold.files {
						if file.action == syncengine.ActionConflict {
							totalConflicts++
						}
					}
				}
			} else {
				for _, row := range e.tempFolders {
					totalFiles += row.CopyCount + row.SkipCount + row.ConflictCount
					totalFilesDone += row.SkipCount
					totalBytes += row.CopyBytes
					copyFilesTotal += row.CopyCount
					copyBytesTotal += row.CopyBytes
					totalConflicts += row.ConflictCount
				}
			}
		}

		if phase == phaseScanRunning {
			expValue.SetText(fmt.Sprintf("%d / %d", totalExpsDone, totalExps))
			// Conflicts surface the moment the scan finds them — a burst of
			// conflicts mid-scan is exactly the "picked the wrong location /
			// stale recorder files" signal the user needs before syncing.
			if totalConflicts > 0 {
				filesValue.SetText(fmt.Sprintf("%d (⚠ %d conflicts)", totalFiles, totalConflicts))
			} else {
				filesValue.SetText(fmt.Sprintf("%d", totalFiles))
			}
			bytesValue.SetText(humanBytes(totalBytes))
		} else if phase == phaseScanComplete || phase == phaseScanCancelled {
			expValue.SetText(fmt.Sprintf("%d", totalExps))
			var copyFiles, skipFiles, conflictFiles int
			var copyBytes, skipBytes int64
			for _, e := range expStates {
				for _, f := range e.folders {
					for _, file := range f.files {
						switch file.action {
						case syncengine.ActionCopy:
							copyFiles++
							copyBytes += file.size
						case syncengine.ActionConflict:
							conflictFiles++
						default:
							skipFiles++
							skipBytes += file.size
						}
					}
				}
			}
			if conflictFiles > 0 {
				filesValue.SetText(fmt.Sprintf("%d unsynced / %d synced / %d conflicts", copyFiles, skipFiles, conflictFiles))
			} else {
				filesValue.SetText(fmt.Sprintf("%d unsynced / %d synced", copyFiles, skipFiles))
			}
			bytesValue.SetText(fmt.Sprintf("%s unsynced / %s synced", humanBytes(copyBytes), humanBytes(skipBytes)))
		} else {
			expValue.SetText(fmt.Sprintf("%d / %d", totalExpsDone, totalExps))
			skipFilesTotal := totalFiles - copyFilesTotal
			skipBytesTotal := totalBytes - copyBytesTotal
			filesValue.SetText(fmt.Sprintf("%d / %d\n(%d already synced)", copyFilesDone, copyFilesTotal, skipFilesTotal))
			bytesValue.SetText(fmt.Sprintf("%s / %s\n(%s already synced)", humanBytes(copyBytesDone), humanBytes(copyBytesTotal), humanBytes(skipBytesTotal)))
			if copyBytesTotal > 0 {
				overallBar.SetValue(float64(copyBytesDone) / float64(copyBytesTotal))
			} else {
				overallBar.SetValue(1.0)
			}
		}

		if selectedExpIdx >= 0 && selectedExpIdx < len(expStates) {
			exp := expStates[selectedExpIdx]
			if exp.err != nil {
				errorLabel.SetText(fmt.Sprintf("Error in %s: %s", exp.label, exp.err.Error()))
				errorLabel.Show()
			} else {
				errorLabel.Hide()
			}
		} else {
			errorLabel.Hide()
		}

		expList.Refresh()
		// Only refresh foldList & fileList if the selected exp is static or changed.
		// If we are in scan running and it's active, refresh.
		if selectedExpIdx >= 0 && selectedExpIdx < len(expStates) {
			exp := expStates[selectedExpIdx]
			if len(exp.folders) == 0 {
				refreshFolders()
				refreshFiles()
			}
		} else {
			refreshFolders()
			refreshFiles()
		}
	}

	cancelBtn = widget.NewButton("Cancel", func() {
		// All running tasks' contexts (scan or sync) are children of the
		// single context created for this run, so cancelling it cascades
		// to every task/job currently in flight, however many are running
		// concurrently.
		if activeCancel != nil {
			activeCancel()
		}
	})

	backBtn = widget.NewButton("Back", onBack)

	var scanResults []syncengine.ScanResult = make([]syncengine.ScanResult, len(tasks))

	runSync := func() {
		// Rebuild expStates from scanResults so progress is reset when
		// re-running after a cancellation.
		for i, t := range tasks {
			expStates[i] = buildExpUIState(t.Label, scanResults[i])
		}
		selectedFoldIdx = 0
		phase = phaseSyncing
		refreshUI()

		jobs := make([]scanJob, len(tasks))
		for i, t := range tasks {
			taskCopy := t
			resCopy := scanResults[i]
			jobs[i] = scanJob{
				Label:  t.Label,
				Result: resCopy,
				Locs:   t.Locs,
				Start: func(ctx context.Context) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
					return taskCopy.Start(ctx, resCopy)
				},
			}
		}

		// Create this run's context on the UI goroutine, before launching the
		// worker, so the Cancel button (also on the UI goroutine) always
		// observes the current run's cancel — never a stale one or nil in the
		// window before the goroutine is scheduled, and never via a data race.
		ctx, cancel := context.WithCancel(context.Background())
		activeCancel = cancel

		go func() {
			var wg sync.WaitGroup
			sem := make(chan struct{}, maxConcurrentTasks)

			runOne := func(i int, j scanJob) {
				defer wg.Done()
				defer func() { <-sem }()

				if ctx.Err() != nil {
					return
				}

				fyne.Do(func() {
					expStates[i].status = statusRunning
					refreshUI()
				})

				job, progress := j.Start(ctx)
				_ = job

				var final syncengine.ProgressSnapshot
				for snap := range progress {
					final = snap
					snap := snap
					fyne.Do(func() {
						completedBytesSum := int64(0)
						for _, file := range expStates[i].fileMap {
							if p, ok := snap.Files[file.relPath]; ok {
								file.done = p.Done
								file.bytesDone = p.BytesDone
								file.err = p.Err
								file.hasError = p.Err != nil
							} else if file.action == syncengine.ActionSkipIdentical {
								file.done = true
								file.bytesDone = file.size
							} else {
								if file.relPath != snap.CurrentFile {
									file.done = false
									file.bytesDone = 0
								}
							}
							if file.done && file.relPath != snap.CurrentFile {
								completedBytesSum += file.size
							}
						}

						if snap.CurrentFile != "" {
							if file, ok := expStates[i].fileMap[snap.CurrentFile]; ok {
								fileBytes := snap.BytesDone - completedBytesSum
								if fileBytes < 0 {
									fileBytes = 0
								}
								if fileBytes > file.size {
									fileBytes = file.size
								}
								file.bytesDone = fileBytes
								file.done = (fileBytes == file.size)
							}
						}

						expStates[i].hasError = false
						expStates[i].bytesDone = 0
						expStates[i].copyBytesDone = 0
						expStates[i].copyFilesDone = 0
						for _, fold := range expStates[i].folders {
							fold.bytesDone = 0
							fold.filesDone = 0
							fold.copyBytesDone = 0
							fold.copyFilesDone = 0
							fold.hasError = false
							for _, file := range fold.files {
								fold.bytesDone += file.bytesDone
								if file.done {
									fold.filesDone++
								}
								if file.action == syncengine.ActionCopy {
									fold.copyBytesDone += file.bytesDone
									if file.done {
										fold.copyFilesDone++
									}
								}
								if file.hasError {
									fold.hasError = true
								}
							}
							expStates[i].bytesDone += fold.bytesDone
							expStates[i].copyBytesDone += fold.copyBytesDone
							expStates[i].copyFilesDone += fold.copyFilesDone
							if fold.hasError {
								expStates[i].hasError = true
							}
						}

						if snap.Retrying {
							retryLabel.Text = fmt.Sprintf("⚠ Connection hiccup, retrying (%d/%d)…", snap.RetryAttempt, snap.RetryMax)
							retryLabel.Refresh()
							retryLabel.Show()
						} else {
							retryLabel.Hide()
						}

						if snap.Speed > 0 {
							speedLabel.SetText(fmt.Sprintf("Speed: %s/s", humanSpeed(snap.Speed)))
							speedLabel.Show()
						} else {
							speedLabel.Hide()
						}

						// Force refreshing the active folders/files list during sync
						if selectedExpIdx == i {
							refreshFolders()
							refreshFiles()
						}
						refreshUI()
					})
				}

				fyne.Do(func() {
					statusText := statusDone
					var jobErr error
					switch final.Status {
					case syncengine.JobError:
						statusText = statusError
						jobErr = final.Err
						expStates[i].hasError = true
						expStates[i].err = final.Err
						if isAuthError(final.Err) {
							showLocationError(s, final.Err, j.Locs...)
						}
					case syncengine.JobCanceled:
						statusText = statusCanceled
					}
					expStates[i].status = statusText
					expStates[i].err = jobErr

					if statusText == statusDone {
						for _, fold := range expStates[i].folders {
							fold.bytesDone = fold.totalBytes
							fold.filesDone = fold.totalFiles
							fold.copyBytesDone = fold.copyTotalBytes
							fold.copyFilesDone = fold.copyTotalFiles
							for _, file := range fold.files {
								file.bytesDone = file.size
								file.done = true
							}
						}
						expStates[i].bytesDone = expStates[i].totalBytes
						expStates[i].copyBytesDone = expStates[i].copyTotalBytes
						expStates[i].copyFilesDone = expStates[i].copyTotalFiles
					}

					if selectedExpIdx == i {
						refreshFolders()
						refreshFiles()
					}
					refreshUI()
				})
			}

			for i, j := range jobs {
				wg.Add(1)
				sem <- struct{}{}
				go runOne(i, j)
			}
			wg.Wait()

			// Check cancellation before calling cancel() so ctx.Err() reflects
			// whether the user cancelled, not the cleanup cancel below.
			wasCancelled := ctx.Err() != nil
			cancel()

			fyne.Do(func() {
				if wasCancelled {
					phase = phaseSyncCancelled
				} else {
					phase = phaseSyncComplete
				}
				speedLabel.Hide()
				retryLabel.Hide()
				refreshUI()
			})
		}()
	}

	syncBtn = widget.NewButton("Sync", func() {
		if extras.onNWaySync != nil {
			extras.onNWaySync()
			return
		}
		if conflicts := collectConflicts(tasks, scanResults); len(conflicts) > 0 {
			showConflictsPrompt(s, conflicts, runSync)
			return
		}
		runSync()
	})
	syncBtn.Importance = widget.HighImportance
	syncBtn.Hide()

	resolveBtn = widget.NewButton("Resolve conflicts…", func() {
		if extras.nway != nil {
			showNWayResolveDialog(s, extras.nway, nil)
		}
	})
	resolveBtn.Importance = widget.WarningImportance
	resolveBtn.Hide()

	scanBtn = widget.NewButton("Scan", nil) // OnTapped set after runScan is defined
	scanBtn.Importance = widget.MediumImportance
	scanBtn.Hide()

	progressContainer := container.NewStack(overallBar, overallBarInf)

	header := container.NewVBox(
		container.NewHBox(titleLabel, speedLabel, retryLabel),
		progressContainer,
		metrics,
		errorLabel,
		widget.NewSeparator(),
	)

	content := container.NewBorder(
		header,
		container.NewHBox(cancelBtn, scanBtn, resolveBtn, syncBtn, backBtn),
		nil, nil,
		columns,
	)

	if extras.nway != nil {
		// Any resolution change re-captions conflict rows and re-evaluates
		// the Resolve/Sync gate.
		extras.nway.onChange = func() {
			refreshFiles()
			refreshUI()
		}
	}

	s.setContent(container.NewPadded(content))

	if len(tasks) > 0 {
		expList.Select(0)
	}

	refreshUI()

	// runScan resets state and (re-)runs the scan goroutine.
	// It is called once at startup and again if the user clicks Scan after
	// a cancellation.
	var runScan func()
	runScan = func() {
		// Reset experiment states so the lists are clean on re-run.
		for i, t := range tasks {
			expStates[i] = &expUIState{
				label:  t.Label,
				status: statusWaiting,
			}
			scanResults[i] = syncengine.ScanResult{}
		}
		selectedFoldIdx = 0
		phase = phaseScanRunning
		refreshUI()
		if len(tasks) > 0 {
			expList.Select(0)
		}

		// Create this run's context on the UI goroutine, before launching the
		// worker, so the Cancel button (also on the UI goroutine) always
		// observes the current run's cancel — never a stale one or nil in the
		// window before the goroutine is scheduled, and never via a data race.
		ctx, cancel := context.WithCancel(context.Background())
		activeCancel = cancel

		go func() {
			var wg sync.WaitGroup
			sem := make(chan struct{}, maxConcurrentTasks)

			var mu sync.Mutex
			cancelled := false

			runOne := func(i int, task scanTask) {
				defer wg.Done()
				defer func() { <-sem }()

				if ctx.Err() != nil {
					mu.Lock()
					cancelled = true
					mu.Unlock()
					return
				}

				fyne.Do(func() {
					expStates[i].status = statusRunning
					refreshUI()
				})

				result, err := task.Scan(ctx, func(p syncengine.ScanProgress) {
					fyne.Do(func() {
						expStates[i].tempFolders = p.Dirs
						expStates[i].tempRecent = p.Recent
						refreshUI()
					})
				})

				if err != nil {
					isCanceled := errors.Is(err, context.Canceled)
					if isCanceled {
						mu.Lock()
						cancelled = true
						mu.Unlock()
					}
					fyne.Do(func() {
						if isCanceled {
							expStates[i].status = statusCanceled
						} else {
							expStates[i].status = statusError
							expStates[i].err = err
							expStates[i].hasError = true
						}
						refreshUI()
					})
					if !isCanceled && isAuthError(err) {
						fyne.Do(func() {
							showLocationError(s, err, task.Locs...)
						})
					}
					return
				}

				scanResults[i] = result
				fyne.Do(func() {
					expStates[i] = buildExpUIState(task.Label, result)
					expStates[i].status = statusDone
					if selectedExpIdx == i {
						selectedFoldIdx = 0
						refreshFolders()
						refreshFiles()
						expList.Select(widget.ListItemID(i))
					}
					refreshUI()
				})
			}

			for i, task := range tasks {
				wg.Add(1)
				sem <- struct{}{}
				go runOne(i, task)
			}
			wg.Wait()
			cancel()

			fyne.Do(func() {
				mu.Lock()
				wasCancelled := cancelled
				mu.Unlock()
				if wasCancelled {
					phase = phaseScanCancelled
				} else {
					phase = phaseScanComplete
				}
				refreshUI()

				if extras.autoSync && phase == phaseScanComplete {
					// Pre-confirmed plan (see syncFlowExtras.autoSync): start
					// copying without a second Sync press — but only if every
					// task's instant "scan" replay actually succeeded.
					for _, e := range expStates {
						if e.status != statusDone {
							return
						}
					}
					runSync()
				}
			})
		}()
	}

	scanBtn.OnTapped = runScan

	runScan()
}

// showErrorModal opens a scrollable dialog containing the full error text and
// a Copy button. It is triggered by the error-icon button on a list row.
func showErrorModal(win fyne.Window, errText string) {
	errLabel := widget.NewLabel(errText)
	errLabel.Wrapping = fyne.TextWrapWord

	scroll := container.NewScroll(errLabel)
	scroll.SetMinSize(fyne.NewSize(420, 220))

	copyBtn := widget.NewButton("Copy", func() {
		win.Clipboard().SetContent(errText)
	})

	content := container.NewBorder(
		nil,
		container.NewPadded(copyBtn),
		nil, nil,
		scroll,
	)

	d := dialog.NewCustom("Error Details", "Close", content, win)
	d.Show()
}

func metricPanel(label string, value *widget.Label, bg color.Color) fyne.CanvasObject {
	rect := canvas.NewRectangle(bg)
	rect.CornerRadius = 8
	caption := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return container.NewStack(rect, container.NewPadded(container.NewVBox(caption, value)))
}

func humanSpeed(bytesPerSec float64) string {
	const unit = 1024
	if bytesPerSec < unit {
		return fmt.Sprintf("%.1f B", bytesPerSec)
	}
	div, exp := float64(unit), 0
	for m := bytesPerSec / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", bytesPerSec/div, "KMGTPE"[exp])
}
