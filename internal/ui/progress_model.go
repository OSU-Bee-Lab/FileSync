package ui

import (
	"path"
	"sort"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// This file holds the scan/sync screen's data model: pure types and
// mutation logic, with no Fyne widgets. See progress_widgets.go for render
// primitives, progress_screen.go for the screen struct and its rendering,
// and progress_run.go for the scan/sync goroutine drivers that call into
// this model.

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

// syncMetrics is the single-pass aggregate over expStates (and, while a scan
// is still in flight, the live tempFolders) that progressScreen.renderMetrics
// renders per phase. Computed once per refreshUI call by computeSyncMetrics.
type syncMetrics struct {
	totalExps, totalExpsDone      int
	totalFiles, totalFilesDone    int
	totalBytes, totalBytesDone    int64
	copyFilesDone, copyFilesTotal int
	copyBytesDone, copyBytesTotal int64
	totalConflicts                int
}

// computeSyncMetrics aggregates expStates (folders/files once a scan has
// built them, tempFolders while it's still running) in a single pass over
// exp→folder→file. renderMetrics then derives every phase's display text
// from this one struct instead of re-walking the tree per phase.
func computeSyncMetrics(expStates []*expUIState) syncMetrics {
	var m syncMetrics
	m.totalExps = len(expStates)
	for _, e := range expStates {
		if e.status == statusDone || e.status == statusError || e.status == statusCanceled {
			m.totalExpsDone++
		}
		if len(e.folders) > 0 {
			for _, fold := range e.folders {
				m.totalFiles += fold.totalFiles
				m.totalFilesDone += fold.filesDone
				m.totalBytes += fold.totalBytes
				m.totalBytesDone += fold.bytesDone
				m.copyFilesTotal += fold.copyTotalFiles
				m.copyFilesDone += fold.copyFilesDone
				m.copyBytesTotal += fold.copyTotalBytes
				m.copyBytesDone += fold.copyBytesDone
				// Conflicts surface the moment the scan finds them — a burst
				// of conflicts mid-scan is exactly the "picked the wrong
				// location / stale recorder files" signal the user needs
				// before syncing.
				for _, file := range fold.files {
					if file.action == syncengine.ActionConflict {
						m.totalConflicts++
					}
				}
			}
		} else {
			for _, row := range e.tempFolders {
				m.totalFiles += row.CopyCount + row.SkipCount + row.ConflictCount
				m.totalFilesDone += row.SkipCount
				m.totalBytes += row.CopyBytes
				m.copyFilesTotal += row.CopyCount
				m.copyBytesTotal += row.CopyBytes
				m.totalConflicts += row.ConflictCount
			}
		}
	}
	return m
}

// fileSyncRank orders the Current Sync list while a sync is running:
// actively-transferring files first, not-yet-started files in the
// middle, and files that have already finished at the bottom.
func fileSyncRank(f *fileUIState) int {
	switch {
	case f.done:
		return 2
	case f.bytesDone > 0:
		return 0
	default:
		return 1
	}
}

// applyScanProgress folds a live scan progress update into the experiment's
// temp (not-yet-final) folder/recent-entry snapshot. Pure — the caller wraps
// this in fyne.Do and triggers a re-render.
func (e *expUIState) applyScanProgress(p syncengine.ScanProgress) {
	e.tempFolders = p.Dirs
	e.tempRecent = p.Recent
}

// applySyncSnapshot folds one copy-progress snapshot into the experiment's
// per-file, per-folder, and experiment-level aggregates. Pure — the caller
// wraps this in fyne.Do and triggers a re-render.
func (e *expUIState) applySyncSnapshot(snap syncengine.ProgressSnapshot) {
	completedBytesSum := int64(0)
	for _, file := range e.fileMap {
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
		if file, ok := e.fileMap[snap.CurrentFile]; ok {
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

	e.hasError = false
	e.bytesDone = 0
	e.copyBytesDone = 0
	e.copyFilesDone = 0
	for _, fold := range e.folders {
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
		e.bytesDone += fold.bytesDone
		e.copyBytesDone += fold.copyBytesDone
		e.copyFilesDone += fold.copyFilesDone
		if fold.hasError {
			e.hasError = true
		}
	}
}

// markDone sets every folder/file in the experiment to fully complete. Called
// once a sync job finishes successfully, so the final render shows 100%
// rather than whatever the last progress snapshot happened to report. Pure —
// the caller wraps this in fyne.Do and triggers a re-render.
func (e *expUIState) markDone() {
	for _, fold := range e.folders {
		fold.bytesDone = fold.totalBytes
		fold.filesDone = fold.totalFiles
		fold.copyBytesDone = fold.copyTotalBytes
		fold.copyFilesDone = fold.copyTotalFiles
		for _, file := range fold.files {
			file.bytesDone = file.size
			file.done = true
		}
	}
	e.bytesDone = e.totalBytes
	e.copyBytesDone = e.copyTotalBytes
	e.copyFilesDone = e.copyTotalFiles
}
