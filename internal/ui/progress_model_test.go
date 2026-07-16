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

// TestApplySyncSnapshotConcurrentTransfers reproduces the double-counting /
// premature-completion bug in the old CurrentFile reconstruction: with
// rclone's Transfers=4, several files transfer concurrently and all show up
// in snap.Files with their own live partial bytes, but snap.BytesDone is the
// job-GLOBAL cumulative total across every in-flight file. The old code
// subtracted completed bytes from that global total and assigned the
// remainder entirely to CurrentFile — double-counting the other concurrent
// files' bytes into CurrentFile and clamping it done too early. Each file's
// bytesDone must instead come from its own entry in snap.Files, and the
// folder/exp totals must equal the sum of the (correct) per-file bytes, with
// no file marked done before it actually reports Done, across successive
// snapshots.
func TestApplySyncSnapshotConcurrentTransfers(t *testing.T) {
	const fileSize int64 = 1000

	entries := []syncengine.ScanEntry{
		{RelPath: "exp1/a.wav", Size: fileSize, Action: syncengine.ActionCopy},
		{RelPath: "exp1/b.wav", Size: fileSize, Action: syncengine.ActionCopy},
		{RelPath: "exp1/c.wav", Size: fileSize, Action: syncengine.ActionCopy},
		{RelPath: "exp1/d.wav", Size: fileSize, Action: syncengine.ActionCopy},
	}
	exp := buildExpUIState("Concurrent Transfers Exp", syncengine.ScanResult{
		Entries:    entries,
		CopyCount:  len(entries),
		TotalBytes: fileSize * int64(len(entries)),
	})

	// Tick 1: all four files transferring concurrently, each 20% done.
	// CurrentFile names only one of them (rclone's transferring list
	// collapses to a single name), but all four appear in snap.Files.
	partial := int64(200)
	snap1 := syncengine.ProgressSnapshot{
		BytesDone:   partial * 4, // job-global cumulative across all 4 in-flight files
		BytesTotal:  fileSize * 4,
		CurrentFile: "exp1/d.wav",
		Status:      syncengine.JobRunning,
		Files: map[string]syncengine.FileProgress{
			"exp1/a.wav": {BytesDone: partial},
			"exp1/b.wav": {BytesDone: partial},
			"exp1/c.wav": {BytesDone: partial},
			"exp1/d.wav": {BytesDone: partial},
		},
	}
	exp.applySyncSnapshot(snap1)

	for _, rel := range []string{"exp1/a.wav", "exp1/b.wav", "exp1/c.wav", "exp1/d.wav"} {
		f := exp.fileMap[rel]
		if f.done {
			t.Errorf("%s marked done prematurely after partial snapshot", rel)
		}
		if f.bytesDone != partial {
			t.Errorf("%s bytesDone = %d, want %d (must come from its own snap.Files entry, not a share of the global total)", rel, f.bytesDone, partial)
		}
	}

	wantFolderBytes := partial * 4
	if len(exp.folders) != 1 {
		t.Fatalf("expected 1 folder, got %d", len(exp.folders))
	}
	if got := exp.folders[0].copyBytesDone; got != wantFolderBytes {
		t.Errorf("folder copyBytesDone = %d, want %d (sum of per-file bytes, no double count)", got, wantFolderBytes)
	}
	if got := exp.copyBytesDone; got != wantFolderBytes {
		t.Errorf("exp copyBytesDone = %d, want %d", got, wantFolderBytes)
	}
	prevExpBytes := exp.copyBytesDone

	// Tick 2: "a" and "b" finish, "c" and "d" keep progressing. CurrentFile
	// now names "d". snap.BytesDone climbs by the sum of every file's
	// increase.
	snap2 := syncengine.ProgressSnapshot{
		BytesDone:   fileSize*2 + 500 + 500, // a+b done, c and d at 500 each
		BytesTotal:  fileSize * 4,
		CurrentFile: "exp1/d.wav",
		Status:      syncengine.JobRunning,
		Files: map[string]syncengine.FileProgress{
			"exp1/a.wav": {BytesDone: fileSize, Done: true},
			"exp1/b.wav": {BytesDone: fileSize, Done: true},
			"exp1/c.wav": {BytesDone: 500},
			"exp1/d.wav": {BytesDone: 500},
		},
	}
	exp.applySyncSnapshot(snap2)

	if !exp.fileMap["exp1/a.wav"].done || !exp.fileMap["exp1/b.wav"].done {
		t.Errorf("a/b should be done after reporting Done")
	}
	if exp.fileMap["exp1/c.wav"].done || exp.fileMap["exp1/d.wav"].done {
		t.Errorf("c/d should not be done yet (only 500/1000 bytes)")
	}
	if got := exp.fileMap["exp1/c.wav"].bytesDone; got != 500 {
		t.Errorf("c bytesDone = %d, want 500", got)
	}
	if got := exp.fileMap["exp1/d.wav"].bytesDone; got != 500 {
		t.Errorf("d (CurrentFile) bytesDone = %d, want 500 (not inflated by the other files' bytes)", got)
	}

	wantExpBytes := fileSize*2 + 500 + 500
	if exp.copyBytesDone != wantExpBytes {
		t.Errorf("exp copyBytesDone = %d, want %d", exp.copyBytesDone, wantExpBytes)
	}
	if exp.copyBytesDone < prevExpBytes {
		t.Errorf("exp copyBytesDone regressed: %d -> %d", prevExpBytes, exp.copyBytesDone)
	}
}

// TestApplySyncSnapshotSkipIdenticalPlusActiveCopy reproduces the "0 bytes
// until it finishes, then jumps" bug: ActionSkipIdentical files are folded
// into completedBytesSum by the old code even though their bytes are never
// part of snap.BytesDone (they're never transferred at all), so
// snap.BytesDone - completedBytesSum went negative and clamped the active
// copy file to 0 for its entire transfer. The active file's live bytes must
// come straight from its own snap.Files entry regardless of how many
// already-synced files sit alongside it.
func TestApplySyncSnapshotSkipIdenticalPlusActiveCopy(t *testing.T) {
	const fileSize int64 = 1000

	entries := []syncengine.ScanEntry{
		{RelPath: "exp2/already1.wav", Size: fileSize, Action: syncengine.ActionSkipIdentical},
		{RelPath: "exp2/already2.wav", Size: fileSize, Action: syncengine.ActionSkipIdentical},
		{RelPath: "exp2/already3.wav", Size: fileSize, Action: syncengine.ActionSkipIdentical},
		{RelPath: "exp2/active.wav", Size: fileSize, Action: syncengine.ActionCopy},
	}
	exp := buildExpUIState("Skip Plus Active Exp", syncengine.ScanResult{
		Entries:    entries,
		CopyCount:  1,
		TotalBytes: fileSize,
	})

	// Skip-identical files are marked done at build time and are never part
	// of a copy job, so rclone's snapshot never mentions them. Only the
	// actively-copying file appears in snap.Files. snap.BytesDone reflects
	// only the copy job's own transferred bytes (rclone doesn't count
	// skipped files as transferred at all), so it's far smaller than the sum
	// of all four files' sizes.
	active := int64(300)
	snap := syncengine.ProgressSnapshot{
		BytesDone:   active,
		BytesTotal:  fileSize,
		CurrentFile: "exp2/active.wav",
		Status:      syncengine.JobRunning,
		Files: map[string]syncengine.FileProgress{
			"exp2/active.wav": {BytesDone: active},
		},
	}
	exp.applySyncSnapshot(snap)

	af := exp.fileMap["exp2/active.wav"]
	if af.bytesDone != active {
		t.Fatalf("active file bytesDone = %d, want %d (must not be reconstructed as snap.BytesDone minus skip-identical sizes, which goes negative and clamps to 0)", af.bytesDone, active)
	}
	if af.bytesDone < 0 {
		t.Fatalf("active file bytesDone went negative: %d", af.bytesDone)
	}
	if af.done {
		t.Errorf("active file marked done prematurely at %d/%d bytes", active, fileSize)
	}

	for _, rel := range []string{"exp2/already1.wav", "exp2/already2.wav", "exp2/already3.wav"} {
		f := exp.fileMap[rel]
		if !f.done || f.bytesDone != fileSize {
			t.Errorf("%s should stay done/full-size, got done=%v bytesDone=%d", rel, f.done, f.bytesDone)
		}
	}

	wantExpBytesDone := fileSize*3 + active // 3 skip-identical + active's partial
	if exp.bytesDone != wantExpBytesDone {
		t.Errorf("exp bytesDone = %d, want %d", exp.bytesDone, wantExpBytesDone)
	}
	if exp.copyBytesDone != active {
		t.Errorf("exp copyBytesDone = %d, want %d (copy* excludes skip-identical files)", exp.copyBytesDone, active)
	}

	// Finish the copy.
	snapDone := syncengine.ProgressSnapshot{
		BytesDone:   fileSize,
		BytesTotal:  fileSize,
		CurrentFile: "exp2/active.wav",
		Status:      syncengine.JobRunning,
		Files: map[string]syncengine.FileProgress{
			"exp2/active.wav": {BytesDone: fileSize, Done: true},
		},
	}
	exp.applySyncSnapshot(snapDone)
	if !af.done || af.bytesDone != fileSize {
		t.Errorf("active file should be done at full size, got done=%v bytesDone=%d", af.done, af.bytesDone)
	}
	if exp.bytesDone != fileSize*4 {
		t.Errorf("final exp bytesDone = %d, want %d", exp.bytesDone, fileSize*4)
	}
}

// TestMarkDoneSkipsConflictAndErroredFiles reproduces the bug where markDone
// unconditionally marked every file done, including ActionConflict files
// (deliberately never copied — the user hasn't resolved them) and files that
// errored mid-transfer. Only successfully-copied (ActionCopy, no error)
// files, plus already-synced (ActionSkipIdentical) files, should read as
// done after markDone.
func TestMarkDoneSkipsConflictAndErroredFiles(t *testing.T) {
	const fileSize int64 = 1000

	entries := []syncengine.ScanEntry{
		{RelPath: "exp3/copied.wav", Size: fileSize, Action: syncengine.ActionCopy},
		{RelPath: "exp3/errored.wav", Size: fileSize, Action: syncengine.ActionCopy},
		{RelPath: "exp3/conflict.wav", Size: fileSize, Action: syncengine.ActionConflict},
		{RelPath: "exp3/already.wav", Size: fileSize, Action: syncengine.ActionSkipIdentical},
	}
	exp := buildExpUIState("MarkDone Exp", syncengine.ScanResult{
		Entries:    entries,
		CopyCount:  2,
		TotalBytes: fileSize * 2,
	})

	// Simulate the copy job's final snapshot: "copied" succeeded, "errored"
	// hit an error and per job.go's contract is never reported Done.
	snap := syncengine.ProgressSnapshot{
		BytesDone:  fileSize + 400,
		BytesTotal: fileSize * 2,
		Status:     syncengine.JobDone,
		Files: map[string]syncengine.FileProgress{
			"exp3/copied.wav":  {BytesDone: fileSize, Done: true},
			"exp3/errored.wav": {BytesDone: 400, Err: fmt.Errorf("boom")},
		},
	}
	exp.applySyncSnapshot(snap)
	exp.markDone()

	if !exp.fileMap["exp3/copied.wav"].done {
		t.Errorf("copied.wav should be done")
	}
	if exp.fileMap["exp3/errored.wav"].done {
		t.Errorf("errored.wav must not be marked done by markDone")
	}
	if exp.fileMap["exp3/conflict.wav"].done {
		t.Errorf("conflict.wav must not be marked done by markDone (never copied, unresolved)")
	}
	if !exp.fileMap["exp3/already.wav"].done {
		t.Errorf("already.wav (skip-identical) should remain done")
	}

	// exp.bytesDone should reflect only the legitimately-completed work:
	// copied.wav (full size) + already.wav (full size). Not conflict.wav,
	// not errored.wav's partial bytes.
	want := fileSize * 2
	if exp.bytesDone != want {
		t.Errorf("exp bytesDone = %d, want %d", exp.bytesDone, want)
	}
	if exp.copyBytesDone != fileSize {
		t.Errorf("exp copyBytesDone = %d, want %d (only copied.wav)", exp.copyBytesDone, fileSize)
	}
	if exp.copyFilesDone != 1 {
		t.Errorf("exp copyFilesDone = %d, want 1", exp.copyFilesDone)
	}
}
