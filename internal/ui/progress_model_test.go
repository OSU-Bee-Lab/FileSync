package ui

import (
	"fmt"
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// TestApplySyncSnapshotStickyCompletion reproduces the "de-syncing folder"
// bug: rclone's accounting keeps only its ~100 most recent completed
// transfers (MaxCompletedTransfers), so on a folder with many files a
// genuinely-copied file drops out of ProgressSnapshot.Files partway through
// the sync. applySyncSnapshot must not regress those files to zero — a copy
// job never un-finishes a file — so both the experiment's byte total and its
// completed-file count must climb monotonically.
func TestApplySyncSnapshotStickyCompletion(t *testing.T) {
	const nFiles = 250
	const fileSize int64 = 1000
	const window = 100 // rclone MaxCompletedTransfers-style retention cap

	entries := make([]syncengine.ScanEntry, nFiles)
	for i := range entries {
		entries[i] = syncengine.ScanEntry{
			RelPath: fmt.Sprintf("moth_1/f%03d.mp3", i),
			Size:    fileSize,
			Action:  syncengine.ActionCopy,
		}
	}
	exp := buildExpUIState("Luke - Various Opportunistic Recordings", syncengine.ScanResult{
		Entries:    entries,
		CopyCount:  nFiles,
		TotalBytes: fileSize * nFiles,
	})

	// Drive one snapshot per file completing, emulating rclone: Files only
	// ever carries the current in-progress file plus the trailing `window`
	// completed transfers; everything older has been pruned out.
	var prevBytesDone int64
	var prevFilesDone int
	var cumBytes int64
	for done := 1; done <= nFiles; done++ {
		cumBytes = int64(done) * fileSize
		current := fmt.Sprintf("moth_1/f%03d.mp3", done-1)

		files := make(map[string]syncengine.FileProgress)
		start := done - window
		if start < 0 {
			start = 0
		}
		for i := start; i < done; i++ {
			files[fmt.Sprintf("moth_1/f%03d.mp3", i)] = syncengine.FileProgress{
				BytesDone: fileSize,
				Done:      true,
			}
		}

		exp.applySyncSnapshot(syncengine.ProgressSnapshot{
			BytesDone:   cumBytes,
			BytesTotal:  fileSize * nFiles,
			CurrentFile: current,
			Status:      syncengine.JobRunning,
			Files:       files,
		})

		if exp.bytesDone < prevBytesDone {
			t.Fatalf("bytesDone regressed at file %d/%d: %d -> %d (pruned-but-copied file was zeroed)",
				done, nFiles, prevBytesDone, exp.bytesDone)
		}
		filesDone := 0
		for _, fold := range exp.folders {
			filesDone += fold.filesDone
		}
		if filesDone < prevFilesDone {
			t.Fatalf("filesDone regressed at file %d/%d: %d -> %d (folder de-synced)",
				done, nFiles, prevFilesDone, filesDone)
		}
		prevBytesDone = exp.bytesDone
		prevFilesDone = filesDone
	}

	// By the last snapshot every file has been reported done at least once,
	// so the experiment should read as fully copied even though Files only
	// held the last `window` of them.
	if exp.bytesDone != fileSize*nFiles {
		t.Errorf("final bytesDone = %d, want %d", exp.bytesDone, fileSize*nFiles)
	}
	if exp.copyBytesDone != fileSize*nFiles {
		t.Errorf("final copyBytesDone = %d, want %d", exp.copyBytesDone, fileSize*nFiles)
	}
}
