package ui

import (
	"fmt"
	"image/color"
	"path"
	"sort"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// This file holds the scan/sync screen's row-list construction: turning
// expStates (or, mid-scan, their live tempFolders/tempRecent) into the
// barRow slices the Folders/Files split-panel lists render, plus the list
// widgets themselves. See progress_screen.go for the screen struct and its
// top-level refresh orchestration.

// makeBarList builds a list backed by a *[]barRow. isSelected (may be nil)
// decides which rows get the selection stroke; gray rows get a permanent
// grey wash.
func (ps *progressScreen) makeBarList(rows *[]barRow, isSelected func(barRow) bool) *widget.List {
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
			updateBackingBarItem(obj, row.label, row.summary, row.progress, row.err, row.hasError, row.isFolder, sel, ps.s.win)
			setItemFade(obj, row.fade)
			if row.gray {
				tintItemBg(obj, color.NRGBA{R: 243, G: 244, B: 246, A: 255})
			}
		},
	)
}

// buildSplit wires two section lists into a VSplit and returns a function
// that shows one or both panels depending on which groups have rows.
func buildSplit(unsyncedList, syncedList *widget.List) (fyne.CanvasObject, func(hasUnsynced, hasSynced bool)) {
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

// capAndFade limits a group to maxFileRows rows: a folder can hold
// thousands of files the user won't scroll, so we show the first rows,
// fade the trailing ones out, and append a "N files not shown" banner.
func (ps *progressScreen) capAndFade(rows []barRow) []barRow {
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

// tempEntriesForFolder returns the subset of exp.tempRecent whose directory
// matches the currently selected tempFolder.
func (ps *progressScreen) tempEntriesForFolder(exp *expUIState) []syncengine.ScanEntry {
	if ps.selectedFoldIdx < 0 || ps.selectedFoldIdx >= len(exp.tempFolders) {
		return nil
	}
	folderPath := exp.tempFolders[ps.selectedFoldIdx].Path
	var filtered []syncengine.ScanEntry
	for _, e := range exp.tempRecent {
		if path.Dir(e.RelPath) == folderPath {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// conflictRowSummary captions a conflict file row: the plain scan reason by
// default, or the richer N-way resolution summary (and its clickable
// relPath) when this session is an N-way scan.
func (ps *progressScreen) conflictRowSummary(exp *expUIState, f *fileUIState) (summary, relPath string) {
	summary = fmt.Sprintf("⚠ conflict — %s", f.conflictReason)
	if ps.extras.nway == nil {
		return summary, ""
	}
	return ps.extras.nway.rowSummary(exp.label, f.relPath, f.conflictReason), f.relPath
}

// computeFileRows splits the selected folder's files into to-sync and
// already-synced groups. Works from live scan data (tempRecent) before
// completion and from exp.folders afterwards. Already-synced rows carry
// gray=true and no fill so they never turn blue.
func (ps *progressScreen) computeFileRows() (unsynced, synced []barRow) {
	if ps.selectedExpIdx < 0 || ps.selectedExpIdx >= len(ps.expStates) {
		return nil, nil
	}
	exp := ps.expStates[ps.selectedExpIdx]
	if len(exp.folders) > 0 {
		if ps.selectedFoldIdx < 0 || ps.selectedFoldIdx >= len(exp.folders) {
			return nil, nil
		}
		folderFiles := exp.folders[ps.selectedFoldIdx].files
		if ps.isSyncing() {
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
				summary, relPath := ps.conflictRowSummary(exp, f)
				unsynced = append(unsynced, barRow{
					label:           f.name,
					summary:         summary,
					conflictRelPath: relPath,
				})
				continue
			}
			prog := 0.0
			if ps.isSyncing() && f.size > 0 {
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
		for _, e := range ps.tempEntriesForFolder(exp) {
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
	return ps.capAndFade(unsynced), ps.capAndFade(synced)
}

// computeFolderRows splits the selected experiment's folders into unsynced
// (has files to copy) and already-synced (all files identical) groups. The
// split is only known once exp.folders is populated (scan complete); while
// the scan is still running, tempFolders all show in the unsynced group.
// refIdx maps each row back to its folder index so selection works.
func (ps *progressScreen) computeFolderRows() (unsynced, synced []barRow) {
	if ps.selectedExpIdx < 0 || ps.selectedExpIdx >= len(ps.expStates) {
		return nil, nil
	}
	exp := ps.expStates[ps.selectedExpIdx]
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
			bytesDone, bytesTotal := fold.bytesDone, fold.totalBytes
			if ps.isSyncing() {
				filesDone, filesTotal = fold.copyFilesDone, fold.copyTotalFiles
				bytesDone, bytesTotal = fold.copyBytesDone, fold.copyTotalBytes
			}
			prog := 0.0
			if bytesTotal > 0 {
				prog = float64(bytesDone) / float64(bytesTotal)
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

func (ps *progressScreen) refreshFiles() {
	ps.fileUnsyncedRows, ps.fileSyncedRows = ps.computeFileRows()
	ps.applyFilesMode(len(ps.fileUnsyncedRows) > 0, len(ps.fileSyncedRows) > 0)
	ps.fileUnsyncedList.Refresh()
	ps.fileSyncedList.Refresh()
}

func (ps *progressScreen) refreshFolders() {
	ps.foldUnsyncedRows, ps.foldSyncedRows = ps.computeFolderRows()
	ps.applyFoldMode(len(ps.foldUnsyncedRows) > 0, len(ps.foldSyncedRows) > 0)
	ps.foldUnsyncedList.Refresh()
	ps.foldSyncedList.Refresh()
}
