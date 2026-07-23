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
		func() fyne.CanvasObject { return createBackingBarItem(ps.s.win) },
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
			updateBackingBarItem(obj, row.label, row.summary, row.progress, row.err, row.hasError, row.isFolder, sel, ps.s.win, row.conflictReason)
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

// recomputeUnresolved refreshes the cached set of conflicts still awaiting a
// usable decision. Called from every list-refresh entry point so file, folder
// and experiment rows read one consistent snapshot. Without an N-way resolver
// there's nothing to resolve conflicts with, so every conflict stays
// unresolved.
func (ps *progressScreen) recomputeUnresolved() {
	if ps.extras.nway == nil {
		ps.unresolved, ps.haveResolver = nil, false
		return
	}
	ps.unresolved, ps.haveResolver = ps.extras.nway.unresolvedKeys(), true
}

// isUnresolvedConflict reports whether f is a conflict still needing a
// decision — what drives the orange wash and its roll-up onto the folder and
// experiment rows above it.
func (ps *progressScreen) isUnresolvedConflict(expLabel string, f *fileUIState) bool {
	if f.action != syncengine.ActionConflict {
		return false
	}
	if !ps.haveResolver {
		return true
	}
	return ps.unresolved[nwayConflictKey{expName: expLabel, relPath: f.relPath}]
}

// unresolvedInExp counts conflicts still awaiting a decision anywhere in one
// experiment, for the experiment row's own orange wash.
func (ps *progressScreen) unresolvedInExp(exp *expUIState) int {
	if !ps.haveResolver {
		return exp.totalConflicts
	}
	n := 0
	for k := range ps.unresolved {
		if k.expName == exp.label {
			n++
		}
	}
	return n
}

// unresolvedWarnTip captions the warning icon on a folder or experiment row
// that has unresolved conflicts beneath it. Empty when there are none, which
// is also what clears the row's orange wash.
func unresolvedWarnTip(n int) string {
	if n <= 0 {
		return ""
	}
	return plural(n, "unresolved conflict", "")
}

// conflictRowSummary captions a conflict file row: a short status for the
// right-aligned summary (the chosen resolution, or nothing while undecided —
// the warning icon and orange wash already say "conflict"), plus the full
// reason (shown in the icon's tooltip, never inline, so it can't overrun the
// file name) and the clickable relPath.
func (ps *progressScreen) conflictRowSummary(exp *expUIState, f *fileUIState) (summary, reason, relPath string) {
	if ps.extras.nway == nil {
		return "", f.conflictReason, ""
	}
	return ps.extras.nway.rowSummary(exp.label, f.relPath, f.conflictReason), f.conflictReason, f.relPath
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
				summary, reason, relPath := ps.conflictRowSummary(exp, f)
				// Once resolved the row drops its warning icon and orange wash
				// and just states the chosen outcome.
				if !ps.isUnresolvedConflict(exp.label, f) {
					reason = ""
				}
				unsynced = append(unsynced, barRow{
					label:           f.name,
					summary:         summary,
					conflictRelPath: relPath,
					conflictReason:  reason,
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
				unsynced = append(unsynced, barRow{label: path.Base(e.RelPath), summary: "⚠ conflict", conflictReason: e.ConflictReason})
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
			unresolved := 0
			for _, f := range fold.files {
				if ps.isUnresolvedConflict(exp.label, f) {
					unresolved++
				}
			}
			unsynced = append(unsynced, barRow{
				label:    fold.path,
				summary:  summary,
				progress: prog,
				hasError: fold.hasError,
				isFolder: true,
				refIdx:   i,
				// Orange wash rolls up from the files: the folder stays flagged
				// until every conflict inside it has a decision.
				conflictReason: unresolvedWarnTip(unresolved),
			})
		}
	} else {
		for i, row := range exp.tempFolders {
			total := row.CopyCount + row.SkipCount + row.ConflictCount
			unsynced = append(unsynced, barRow{
				label:    row.Path,
				summary:  fmt.Sprintf("%d / %d files", row.SkipCount, total),
				isFolder: true,
				refIdx:   i,
				// Mid-scan nothing can be resolved yet, so every conflict found
				// so far counts as outstanding.
				conflictReason: unresolvedWarnTip(row.ConflictCount),
			})
		}
	}
	return unsynced, synced
}

func (ps *progressScreen) refreshFiles() {
	ps.recomputeUnresolved()
	ps.fileUnsyncedRows, ps.fileSyncedRows = ps.computeFileRows()
	ps.applyFilesMode(len(ps.fileUnsyncedRows) > 0, len(ps.fileSyncedRows) > 0)
	ps.fileUnsyncedList.Refresh()
	ps.fileSyncedList.Refresh()
}

func (ps *progressScreen) refreshFolders() {
	ps.recomputeUnresolved()
	ps.foldUnsyncedRows, ps.foldSyncedRows = ps.computeFolderRows()
	ps.applyFoldMode(len(ps.foldUnsyncedRows) > 0, len(ps.foldSyncedRows) > 0)
	ps.foldUnsyncedList.Refresh()
	ps.foldSyncedList.Refresh()
}
