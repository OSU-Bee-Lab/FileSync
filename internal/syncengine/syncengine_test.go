package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "github.com/rclone/rclone/backend/local"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedExperiment builds one schema-shaped experiment directory under
// <root>/<name>/, with an mp3 (should be synced) and a wav (should be
// filtered out) nested under a deployment-date/recorder tree.
func seedExperiment(t *testing.T, root, name string) {
	t.Helper()
	base := filepath.Join(root, name)
	writeFile(t, filepath.Join(base, "metadata.csv"), "recorder,site\nRecorderA,WARS\n")
	writeFile(t, filepath.Join(base, "README.txt"), "test experiment\n")
	writeFile(t, filepath.Join(base, "2026-06-23", "RecorderA", "260623_0900.mp3"), "audio-bytes-1")
	writeFile(t, filepath.Join(base, "2026-06-23", "RecorderA", "260623_0905.mp3"), "audio-bytes-2")
	writeFile(t, filepath.Join(base, "2026-06-23", "RecorderA", "260623_0905.wav"), "not-mp3-should-be-filtered")
}

func localLoc(root string) Location {
	return Location{ID: root, Name: root, Kind: LocationLocal, RootPath: root}
}

func names(entries []ExperimentEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	sort.Strings(out)
	return out
}

func childNames(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	sort.Strings(out)
	return out
}

func TestListExperiments(t *testing.T) {
	root := t.TempDir()
	seedExperiment(t, root, "Luke Hearon - Golden Forage")
	seedExperiment(t, root, "Luke Hearon - Mustard Cover Crop")

	got, err := ListExperiments(context.Background(), localLoc(root))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Luke Hearon - Golden Forage", "Luke Hearon - Mustard Cover Crop"}
	if got := names(got); !equalStrings(got, want) {
		t.Fatalf("ListExperiments = %v, want %v", got, want)
	}
}

func TestListChildren_AtEachDepth(t *testing.T) {
	root := t.TempDir()
	seedExperiment(t, root, "Luke - Zucchini")
	loc := localLoc(root)
	ctx := context.Background()

	top, err := ListChildren(ctx, loc, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := childNames(top), []string{"Luke - Zucchini"}; !equalStrings(got, want) {
		t.Fatalf("depth 0 = %v, want %v", got, want)
	}

	inExperiment, err := ListChildren(ctx, loc, "Luke - Zucchini")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-06-23", "README.txt", "metadata.csv"}
	if got := childNames(inExperiment); !equalStrings(got, want) {
		t.Fatalf("depth 1 = %v, want %v", got, want)
	}

	inDate, err := ListChildren(ctx, loc, "Luke - Zucchini/2026-06-23")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := childNames(inDate), []string{"RecorderA"}; !equalStrings(got, want) {
		t.Fatalf("depth 2 = %v, want %v", got, want)
	}

	inRecorder, err := ListChildren(ctx, loc, "Luke - Zucchini/2026-06-23/RecorderA")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"260623_0900.mp3", "260623_0905.mp3", "260623_0905.wav"}
	if got := childNames(inRecorder); !equalStrings(got, want) {
		t.Fatalf("depth 3 = %v, want %v", got, want)
	}
}

func TestPreviewAndStartBackup_WholeExperiment(t *testing.T) {
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	seedExperiment(t, srcRoot, "Luke - Zucchini")
	src, dst := localLoc(srcRoot), localLoc(dstRoot)
	ctx := context.Background()
	fset := DefaultFilterSettings()

	preview, err := PreviewBackup(ctx, src, dst, "Luke - Zucchini", fset)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CopyCount != 5 {
		t.Fatalf("preview.CopyCount = %d, want 5", preview.CopyCount)
	}

	job, progress := StartBackup(ctx, src, dst, "Luke - Zucchini", fset, true, preview)
	final := drain(t, progress)
	if final.Status != JobDone {
		t.Fatalf("final status = %v, want JobDone (err=%v)", final.Status, final.Err)
	}
	_ = job

	assertFileExists(t, filepath.Join(dstRoot, "Luke - Zucchini", "2026-06-23", "RecorderA", "260623_0900.mp3"))
	assertFileExists(t, filepath.Join(dstRoot, "Luke - Zucchini", "2026-06-23", "RecorderA", "260623_0905.mp3"))
	assertFileExists(t, filepath.Join(dstRoot, "Luke - Zucchini", "2026-06-23", "RecorderA", "260623_0905.wav"))
	assertFileExists(t, filepath.Join(dstRoot, "Luke - Zucchini", "metadata.csv"))
	assertFileExists(t, filepath.Join(dstRoot, "Luke - Zucchini", "README.txt"))
}

// TestDownloadPreservesSubPath reproduces the exact scenario from the
// feature request: downloading a sub-path deeper than one experiment (a
// single deployment date) into an arbitrary local folder must preserve
// that sub-path's structure under the destination, not flatten the files
// into the destination root.
func TestDownloadPreservesSubPath(t *testing.T) {
	srcRoot := t.TempDir()
	destFolder := filepath.Join(t.TempDir(), "foo") // e.g. "/Downloads/foo"
	seedExperiment(t, srcRoot, "Luke - Zucchini")
	src := localLoc(srcRoot)
	ctx := context.Background()
	fset := DefaultFilterSettings()
	relPath := "Luke - Zucchini/2026-06-23"

	preview, err := PreviewDownload(ctx, src, relPath, destFolder, fset)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CopyCount != 3 {
		t.Fatalf("preview.CopyCount = %d, want 3", preview.CopyCount)
	}

	_, progress := StartDownload(ctx, src, relPath, destFolder, fset, true, preview)
	final := drain(t, progress)
	if final.Status != JobDone {
		t.Fatalf("final status = %v, want JobDone (err=%v)", final.Status, final.Err)
	}

	// Must land at <destFolder>/Luke - Zucchini/2026-06-23/..., not
	// <destFolder>/260623_0900.mp3.
	assertFileExists(t, filepath.Join(destFolder, "Luke - Zucchini", "2026-06-23", "RecorderA", "260623_0900.mp3"))
	assertFileExists(t, filepath.Join(destFolder, "Luke - Zucchini", "2026-06-23", "RecorderA", "260623_0905.mp3"))
}

// TestCopyPreserving_NeverDeletesDestinationOnlyFiles is the single most
// important regression test in this codebase: a future refactor that
// accidentally swaps sync.CopyDir for sync.Sync would silently start
// deleting destination-only files, which is exactly the catastrophe the
// lab's existing "never use rclone sync" rule exists to prevent.
func TestCopyPreserving_NeverDeletesDestinationOnlyFiles(t *testing.T) {
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	seedExperiment(t, srcRoot, "Luke - Zucchini")
	src, dst := localLoc(srcRoot), localLoc(dstRoot)
	ctx := context.Background()
	fset := DefaultFilterSettings()

	extraFile := filepath.Join(dstRoot, "Luke - Zucchini", "2026-06-23", "RecorderA", "extra_not_in_source.mp3")
	writeFile(t, extraFile, "must survive the copy")

	preview, err := PreviewBackup(ctx, src, dst, "Luke - Zucchini", fset)
	if err != nil {
		t.Fatal(err)
	}
	_, progress := StartBackup(ctx, src, dst, "Luke - Zucchini", fset, true, preview)
	final := drain(t, progress)
	if final.Status != JobDone {
		t.Fatalf("final status = %v, want JobDone (err=%v)", final.Status, final.Err)
	}

	assertFileExists(t, extraFile)
}

func TestExperimentNameWithSpecialCharacters(t *testing.T) {
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	name := "O'Brien - Test #1 (draft)"
	seedExperiment(t, srcRoot, name)
	src, dst := localLoc(srcRoot), localLoc(dstRoot)
	ctx := context.Background()
	fset := DefaultFilterSettings()

	exps, err := ListExperiments(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(exps) != 1 || exps[0].Name != name {
		t.Fatalf("ListExperiments = %v, want [%q]", exps, name)
	}

	preview, err := PreviewBackup(ctx, src, dst, name, fset)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CopyCount != 5 {
		t.Fatalf("preview.CopyCount = %d, want 5", preview.CopyCount)
	}
	_, progress := StartBackup(ctx, src, dst, name, fset, true, preview)
	final := drain(t, progress)
	if final.Status != JobDone {
		t.Fatalf("final status = %v, want JobDone (err=%v)", final.Status, final.Err)
	}
	assertFileExists(t, filepath.Join(dstRoot, name, "2026-06-23", "RecorderA", "260623_0900.mp3"))
}

func TestProgressReachesCompletion(t *testing.T) {
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	seedExperiment(t, srcRoot, "Luke - Zucchini")
	src, dst := localLoc(srcRoot), localLoc(dstRoot)
	ctx := context.Background()
	fset := DefaultFilterSettings()

	preview, err := PreviewBackup(ctx, src, dst, "Luke - Zucchini", fset)
	if err != nil {
		t.Fatal(err)
	}
	_, progress := StartBackup(ctx, src, dst, "Luke - Zucchini", fset, true, preview)
	final := drain(t, progress)

	if final.Status != JobDone {
		t.Fatalf("final status = %v, want JobDone (err=%v)", final.Status, final.Err)
	}
	if final.BytesDone != final.BytesTotal {
		t.Fatalf("BytesDone = %d, BytesTotal = %d, want equal", final.BytesDone, final.BytesTotal)
	}
	if final.FilesDone != final.FilesTotal {
		t.Fatalf("FilesDone = %d, FilesTotal = %d, want equal", final.FilesDone, final.FilesTotal)
	}
}

func TestCancelDoesNotHang(t *testing.T) {
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	seedExperiment(t, srcRoot, "Luke - Zucchini")
	src, dst := localLoc(srcRoot), localLoc(dstRoot)
	ctx := context.Background()
	fset := DefaultFilterSettings()

	preview, err := PreviewBackup(ctx, src, dst, "Luke - Zucchini", fset)
	if err != nil {
		t.Fatal(err)
	}
	job, progress := StartBackup(ctx, src, dst, "Luke - Zucchini", fset, true, preview)
	job.Cancel()

	final := drain(t, progress)
	if final.Status != JobDone && final.Status != JobCanceled && final.Status != JobError {
		t.Fatalf("final status = %v, want a terminal status", final.Status)
	}
}

// drain reads a progress channel to closure and returns the last snapshot,
// failing the test if it doesn't complete within a generous timeout
// (guards against the channel never closing, i.e. a hang).
func drain(t *testing.T, progress <-chan ProgressSnapshot) ProgressSnapshot {
	t.Helper()
	var last ProgressSnapshot
	timeout := time.After(10 * time.Second)
	for {
		select {
		case snap, ok := <-progress:
			if !ok {
				return last
			}
			last = snap
		case <-timeout:
			t.Fatal("progress channel did not close within timeout")
		}
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %s (%v)", path, err)
	}
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected file to be filtered out, but it exists: %s", path)
	}
}

func TestFilesFromFilter_MatchesCopyEntries(t *testing.T) {
	preview := PreviewResult{
		CopyCount: 2,
		SkipCount: 1,
		Entries: []PreviewEntry{
			{RelPath: "2026-06-23/RecorderA/260623_0900.mp3", Action: ActionCopy},
			{RelPath: "2026-06-23/RecorderA/260623_0905.mp3", Action: ActionCopy},
			{RelPath: "metadata.csv", Action: ActionSkipIdentical},
		},
	}

	f := filesFromFilter(preview)
	if f == nil {
		t.Fatal("expected non-nil filter for CopyCount > 0")
	}
	if !f.HaveFilesFrom() {
		t.Fatal("expected HaveFilesFrom() == true")
	}

	files := f.Files()
	if _, ok := files["2026-06-23/RecorderA/260623_0900.mp3"]; !ok {
		t.Error("expected copy file 260623_0900.mp3 to be in filter")
	}
	if _, ok := files["2026-06-23/RecorderA/260623_0905.mp3"]; !ok {
		t.Error("expected copy file 260623_0905.mp3 to be in filter")
	}
	if _, ok := files["metadata.csv"]; ok {
		t.Error("skipped file metadata.csv should NOT be in filter")
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files in filter, got %d", len(files))
	}
}

func TestFilesFromFilter_NilWhenNoCopies(t *testing.T) {
	preview := PreviewResult{
		CopyCount: 0,
		SkipCount: 3,
		Entries: []PreviewEntry{
			{RelPath: "a.mp3", Action: ActionSkipIdentical},
			{RelPath: "b.mp3", Action: ActionSkipIdentical},
			{RelPath: "c.mp3", Action: ActionSkipIdentical},
		},
	}
	if f := filesFromFilter(preview); f != nil {
		t.Fatal("expected nil filter when CopyCount == 0")
	}
}

// TestBackupAfterFullSync_NoCopyOptimization verifies that the CopyCount=0
// fallback path (no cached filter, full scan) still works: after syncing
// everything, a re-preview shows all skips, and a second backup is a no-op
// that completes successfully.
func TestBackupAfterFullSync_NoCopyOptimization(t *testing.T) {
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	seedExperiment(t, srcRoot, "Luke - Zucchini")
	src, dst := localLoc(srcRoot), localLoc(dstRoot)
	ctx := context.Background()
	fset := DefaultFilterSettings()

	// First sync: everything should copy.
	preview1, err := PreviewBackup(ctx, src, dst, "Luke - Zucchini", fset)
	if err != nil {
		t.Fatal(err)
	}
	if preview1.CopyCount != 5 {
		t.Fatalf("first preview.CopyCount = %d, want 5", preview1.CopyCount)
	}
	_, progress1 := StartBackup(ctx, src, dst, "Luke - Zucchini", fset, true, preview1)
	if final := drain(t, progress1); final.Status != JobDone {
		t.Fatalf("first backup status = %v, want JobDone (err=%v)", final.Status, final.Err)
	}

	// Second preview: everything should be identical.
	preview2, err := PreviewBackup(ctx, src, dst, "Luke - Zucchini", fset)
	if err != nil {
		t.Fatal(err)
	}
	if preview2.CopyCount != 0 {
		t.Fatalf("second preview.CopyCount = %d, want 0", preview2.CopyCount)
	}

	// Second backup with CopyCount=0 preview (no-cache fallback path).
	_, progress2 := StartBackup(ctx, src, dst, "Luke - Zucchini", fset, true, preview2)
	final := drain(t, progress2)
	if final.Status != JobDone {
		t.Fatalf("second backup status = %v, want JobDone (err=%v)", final.Status, final.Err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
