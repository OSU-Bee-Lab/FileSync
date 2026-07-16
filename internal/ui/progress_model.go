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

	// folder is the owning folderUIState, set once by buildExpUIState. Lets
	// applySyncSnapshot fold per-file updates straight into their folder's
	// aggregates in a single pass over e.fileMap, instead of a second pass
	// over e.folders/fold.files.
	folder *folderUIState
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

	// totalFiles and totalConflicts are invariant once the scan that built
	// this expUIState has finished: a sync never changes how many files an
	// experiment has or how many are conflicts. Populated once by
	// buildExpUIState so computeSyncMetrics — which runs every UI tick while
	// a sync is running — can sum these instead of re-walking every file of
	// every folder each time just to recompute a number that never changes.
	totalFiles     int
	totalConflicts int
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
			f.folder = folder
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
			if f.action == syncengine.ActionConflict {
				exp.totalConflicts++
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
		exp.totalFiles += f.totalFiles
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
			// totalFiles and totalConflicts don't change once the scan that
			// built e.folders has finished, so they're pre-summed on
			// expUIState by buildExpUIState rather than re-derived here from
			// every file, every tick.
			m.totalFiles += e.totalFiles
			m.totalConflicts += e.totalConflicts
			m.totalBytes += e.totalBytes
			for _, fold := range e.folders {
				m.totalFilesDone += fold.filesDone
				m.totalBytesDone += fold.bytesDone
				m.copyFilesTotal += fold.copyTotalFiles
				m.copyFilesDone += fold.copyFilesDone
				m.copyBytesTotal += fold.copyTotalBytes
				m.copyBytesDone += fold.copyBytesDone
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
	// Reset folder/exp aggregates; the loop below over e.fileMap re-derives
	// them in the same pass as the per-file updates (folders are cheap —
	// there are far fewer of them than files — so this two-line reset plus
	// one file pass replaces what used to be two full passes over every
	// file).
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
	}

	for _, file := range e.fileMap {
		// newBytes/haveBytes: the candidate bytesDone value this snapshot
		// implies for this file, if any. Applied through a monotonic clamp
		// below rather than assigned directly, so a file's bytesDone can
		// never regress across snapshots — defense in depth against a file
		// transiently falling out of rclone's windowed Files map (completed
		// transfers) or, in principle, a single tick where it briefly
		// disappears from the transferring list too.
		var newBytes int64
		haveBytes := false

		if p, ok := snap.Files[file.relPath]; ok {
			// A file currently being copied is present here too (via
			// rclone's "transferring" list, keyed the same as CurrentFile),
			// with its own live partial BytesDone — so the current file's
			// bytes come from here directly, with no need to reconstruct
			// them from the job-global snap.BytesDone (which sums ALL
			// in-flight files under Transfers>1, not just this one, and
			// never includes already-skipped files' bytes at all).
			//
			// Completion is sticky (|| p.Done): rclone can report a file as
			// live-transferring one tick and pruned the next, but this app
			// only ever copies — a file never un-finishes within a job.
			file.done = file.done || p.Done
			file.err = p.Err
			file.hasError = p.Err != nil
			if file.done {
				// rclone reports a completed transfer with Bytes == Size,
				// but once sticky-done, always show the full size rather
				// than trusting whatever this tick's snapshot says.
				newBytes = file.size
			} else {
				newBytes = p.BytesDone
			}
			haveBytes = true
		} else if file.action == syncengine.ActionSkipIdentical {
			// Already-synced files are never transferred, so they never
			// appear in snap.Files at all.
			file.done = true
			newBytes = file.size
			haveBytes = true
		} else if file.done {
			// Not in this snapshot, but previously completed. rclone's
			// accounting keeps only its most recent ~100 completed
			// transfers (MaxCompletedTransfers) in the list snap.Files is
			// built from, so on a big folder a genuinely-copied file
			// silently drops out mid-sync. Keep it at full size — that's
			// what made fully-synced folders de-sync and the Bytes counter
			// fall.
			newBytes = file.size
			haveBytes = true
		}
		// Else: not in this snapshot, not skip-identical, not yet done —
		// i.e. genuinely hasn't started (or, in principle, momentarily fell
		// out of the transferring list). Leave file.bytesDone untouched;
		// the monotonic clamp below is a no-op in that case since it can
		// only ever hold its prior value (0, for a not-yet-started file).

		if haveBytes && newBytes > file.bytesDone {
			file.bytesDone = newBytes
		}

		fold := file.folder
		if fold == nil {
			continue
		}
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

	for _, fold := range e.folders {
		e.bytesDone += fold.bytesDone
		e.copyBytesDone += fold.copyBytesDone
		e.copyFilesDone += fold.copyFilesDone
		if fold.hasError {
			e.hasError = true
		}
	}
}

// markDone finalizes the experiment once its sync job has finished
// successfully, so the render shows the true completed state rather than
// whatever the last progress snapshot happened to report. It only marks
// ActionCopy files done — and only those with no recorded error: a
// deliberately-skipped ActionConflict file was never copied, and an errored
// transfer didn't complete, so neither should read as done.
// (ActionSkipIdentical files are left alone: applySyncSnapshot/
// buildExpUIState already mark those done, since they were already synced
// before this job ran.) Pure — the caller wraps this in fyne.Do and
// triggers a re-render.
func (e *expUIState) markDone() {
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
			if file.action == syncengine.ActionCopy && !file.hasError {
				file.bytesDone = file.size
				file.done = true
			}
			if file.done {
				fold.bytesDone += file.bytesDone
				fold.filesDone++
				if file.action == syncengine.ActionCopy {
					fold.copyBytesDone += file.bytesDone
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
