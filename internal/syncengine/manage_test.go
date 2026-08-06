package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestListRecursive_FindsNestedFiles(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "exp/metadata.csv"), "a")
	writeFile(t, filepath.Join(root, "exp/site/r1/230802_0751.mp3"), "b")

	entries, err := ListRecursive(context.Background(), loc, "exp")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].RelPath != "exp/metadata.csv" || entries[1].RelPath != "exp/site/r1/230802_0751.mp3" {
		t.Errorf("unexpected paths: %+v", entries)
	}
}

func TestPlanMove_DetectsCollisionAndPreservesStructure(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "Luke - Wooster 2/metadata.csv"), "src metadata")
	writeFile(t, filepath.Join(root, "Luke - Wooster 2/r1/230802_0751.mp3"), "audio")
	writeFile(t, filepath.Join(root, "Luke - Wooster 1/metadata.csv"), "dst metadata")

	plan, err := PlanMove(context.Background(), loc, "Luke - Wooster 2", "Luke - Wooster 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 2 {
		t.Fatalf("got %d planned moves, want 2: %+v", len(plan.Moves), plan.Moves)
	}
	if len(plan.Collisions) != 1 || plan.Collisions[0] != "Luke - Wooster 1/metadata.csv" {
		t.Fatalf("collisions = %+v, want [Luke - Wooster 1/metadata.csv]", plan.Collisions)
	}
	foundAudio := false
	for _, m := range plan.Moves {
		if m.DstRelPath == "Luke - Wooster 1/r1/230802_0751.mp3" {
			foundAudio = true
		}
	}
	if !foundAudio {
		t.Errorf("expected nested audio file to preserve its relative structure under the new prefix, got %+v", plan.Moves)
	}
}

func TestApplyMove_MergesWithCollisionResolutions(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "Luke - Wooster 2/metadata.csv"), "src metadata")
	writeFile(t, filepath.Join(root, "Luke - Wooster 2/r1/230802_0751.mp3"), "audio")
	writeFile(t, filepath.Join(root, "Luke - Wooster 1/metadata.csv"), "dst metadata")

	ctx := context.Background()
	plan, err := PlanMove(ctx, loc, "Luke - Wooster 2", "Luke - Wooster 1")
	if err != nil {
		t.Fatal(err)
	}
	resolutions := map[string]CollisionResolution{
		"Luke - Wooster 1/metadata.csv": CollisionKeepBoth,
	}
	if err := ApplyMove(ctx, loc, plan, resolutions); err != nil {
		t.Fatal(err)
	}

	// Original dest metadata untouched.
	data, err := os.ReadFile(filepath.Join(root, "Luke - Wooster 1/metadata.csv"))
	if err != nil || string(data) != "dst metadata" {
		t.Errorf("destination metadata.csv = %q, err=%v, want untouched \"dst metadata\"", data, err)
	}
	// Incoming metadata kept under a new name.
	kept, err := os.ReadFile(filepath.Join(root, "Luke - Wooster 1/metadata (2).csv"))
	if err != nil || string(kept) != "src metadata" {
		t.Errorf("kept-both file missing or wrong content: %q, err=%v", kept, err)
	}
	// Non-colliding nested audio file moved with structure preserved.
	audio, err := os.ReadFile(filepath.Join(root, "Luke - Wooster 1/r1/230802_0751.mp3"))
	if err != nil || string(audio) != "audio" {
		t.Errorf("audio file not merged correctly: %q, err=%v", audio, err)
	}
	assertFileMissing(t, filepath.Join(root, "Luke - Wooster 2/r1/230802_0751.mp3"))
}

func TestApplyMove_SkipLeavesSourceInPlace(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "src/f.csv"), "src")
	writeFile(t, filepath.Join(root, "dst/f.csv"), "dst")

	ctx := context.Background()
	plan, err := PlanMove(ctx, loc, "src", "dst")
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyMove(ctx, loc, plan, map[string]CollisionResolution{"dst/f.csv": CollisionSkip}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "src/f.csv"))
	if err != nil || string(data) != "src" {
		t.Errorf("skipped file should remain at source unchanged: %q, err=%v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(root, "dst/f.csv"))
	if err != nil || string(data) != "dst" {
		t.Errorf("destination should remain untouched on skip: %q, err=%v", data, err)
	}
}

// A plain directory rename with nothing colliding must go through rclone's
// one-shot directory move, not a move per file - on a remote like
// SharePoint that is the difference between one API call and one per file.
// The observable difference: a file that appeared after the plan was built
// moves too, because the directory itself is what got renamed.
func TestApplyMove_DirectoryRenameMovesWholeDirAtOnce(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "Reed - Illinois Soybean/2026-07-17/r1/230802_0751.mp3"), "audio")

	ctx := context.Background()
	plan, err := PlanMove(ctx, loc, "Reed - Illinois Soybean/2026-07-17", "Reed - Illinois Soybean/2026-07-14")
	if err != nil {
		t.Fatal(err)
	}
	if plan.DstRoot != "Reed - Illinois Soybean/2026-07-14" {
		t.Fatalf("DstRoot = %q, want the destination directory", plan.DstRoot)
	}
	writeFile(t, filepath.Join(root, "Reed - Illinois Soybean/2026-07-17/r1/unplanned.mp3"), "late arrival")

	if err := ApplyMove(ctx, loc, plan, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "Reed - Illinois Soybean/2026-07-14/r1/230802_0751.mp3"))
	if err != nil || string(data) != "audio" {
		t.Errorf("renamed audio file = %q, err=%v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(root, "Reed - Illinois Soybean/2026-07-14/r1/unplanned.mp3"))
	if err != nil || string(data) != "late arrival" {
		t.Errorf("whole directory should have been renamed, taking the unplanned file with it: %q, err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Reed - Illinois Soybean/2026-07-17")); !os.IsNotExist(err) {
		t.Errorf("source directory should be gone after a rename, stat err=%v", err)
	}
}

// The fast path is only for whole directories: moving a single file must
// still go file-by-file, leaving everything else in its source directory.
func TestPlanMove_SingleFileHasNoDstRoot(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "exp/r1/a.mp3"), "a")
	writeFile(t, filepath.Join(root, "exp/r1/b.mp3"), "b")

	ctx := context.Background()
	plan, err := PlanMove(ctx, loc, "exp/r1/a.mp3", "exp/r1/renamed.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if plan.DstRoot != "" {
		t.Fatalf("DstRoot = %q, want empty for a single-file move", plan.DstRoot)
	}
	if err := ApplyMove(ctx, loc, plan, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "exp/r1/renamed.mp3")); err != nil || string(data) != "a" {
		t.Errorf("renamed file = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "exp/r1/b.mp3")); err != nil || string(data) != "b" {
		t.Errorf("sibling file should be untouched: %q, err=%v", data, err)
	}
}

// Merging into a directory that already exists can't be a directory rename;
// rclone reports the destination exists and falls back to per-file moves,
// which must still merge both trees correctly.
func TestApplyMove_MergeIntoExistingDirWithoutCollisions(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "src/r2/b.mp3"), "b")
	writeFile(t, filepath.Join(root, "dst/r1/a.mp3"), "a")

	ctx := context.Background()
	plan, err := PlanMove(ctx, loc, "src", "dst")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collisions) != 0 {
		t.Fatalf("expected no collisions, got %+v", plan.Collisions)
	}
	if err := ApplyMove(ctx, loc, plan, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "dst/r2/b.mp3")); err != nil || string(data) != "b" {
		t.Errorf("merged file = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "dst/r1/a.mp3")); err != nil || string(data) != "a" {
		t.Errorf("existing destination file should be untouched: %q, err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "src")); !os.IsNotExist(err) {
		t.Errorf("emptied source directory should be cleaned up, stat err=%v", err)
	}
}

func TestPlanDelete_ListsAllNestedFiles(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "exp/metadata.csv"), "a")
	writeFile(t, filepath.Join(root, "exp/r1/230802_0751.mp3"), "b")

	plan, err := PlanDelete(context.Background(), loc, "exp")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(plan.Entries), plan.Entries)
	}
}

func TestApplyDelete_RemovesDirectoryRecursively(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "exp/metadata.csv"), "a")
	writeFile(t, filepath.Join(root, "exp/r1/230802_0751.mp3"), "b")

	if err := ApplyDelete(context.Background(), loc, "exp"); err != nil {
		t.Fatal(err)
	}
	assertFileMissing(t, filepath.Join(root, "exp/metadata.csv"))
	assertFileMissing(t, filepath.Join(root, "exp/r1/230802_0751.mp3"))
	if _, err := os.Stat(filepath.Join(root, "exp")); err == nil {
		t.Error("exp directory should no longer exist after delete")
	}
}

func TestApplyDelete_RemovesSingleFile(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	target := filepath.Join(root, "exp/r1/230802_0751.mp3")
	writeFile(t, target, "b")

	if err := ApplyDelete(context.Background(), loc, "exp/r1/230802_0751.mp3"); err != nil {
		t.Fatal(err)
	}
	assertFileMissing(t, target)
	if _, err := os.Stat(filepath.Join(root, "exp/r1")); err != nil {
		t.Error("sibling directory should be untouched when deleting a single file")
	}
}

func TestResultsLeaf_SwapsExtensionAndInsertsSuffix(t *testing.T) {
	got := resultsLeaf("exp/r1/230802_0751.wma")
	want := "exp/r1/230802_0751_buzzdetect.csv"
	if got != want {
		t.Errorf("resultsLeaf = %q, want %q", got, want)
	}
}

func TestPlanMove_ResultsRole_SingleFileMapsThroughResultsLeaf(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "Results", Kind: LocationLocal, Role: RoleResults, RootPath: root}
	writeFile(t, filepath.Join(root, "exp/r1/230802_0751_buzzdetect.csv"), "csv")

	plan, err := PlanMove(context.Background(), loc, "exp/r1/230802_0751.wma", "exp/r1/230802_0800.wma")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 1 {
		t.Fatalf("got %d planned moves, want 1: %+v", len(plan.Moves), plan.Moves)
	}
	m := plan.Moves[0]
	if m.SrcRelPath != "exp/r1/230802_0751_buzzdetect.csv" || m.DstRelPath != "exp/r1/230802_0800_buzzdetect.csv" {
		t.Errorf("move = %+v, want src/dst mapped through resultsLeaf", m)
	}
}

func TestPlanMove_ResultsRole_DirectoryMovePassesThroughUnchanged(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "Results", Kind: LocationLocal, Role: RoleResults, RootPath: root}
	writeFile(t, filepath.Join(root, "Luke - Wooster 2/r1/230802_0751_buzzdetect.csv"), "csv")

	plan, err := PlanMove(context.Background(), loc, "Luke - Wooster 2", "Luke - Wooster 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].DstRelPath != "Luke - Wooster 1/r1/230802_0751_buzzdetect.csv" {
		t.Fatalf("directory move should relocate the csv under its own unchanged name, got %+v", plan.Moves)
	}
}

func TestPlanMove_ResultsRole_LiteralCsvPathNeedsNoMapping(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "Results", Kind: LocationLocal, Role: RoleResults, RootPath: root}
	writeFile(t, filepath.Join(root, "exp/r1/230802_0751_buzzdetect.csv"), "csv")

	plan, err := PlanMove(context.Background(), loc, "exp/r1/230802_0751_buzzdetect.csv", "exp/r1/renamed_buzzdetect.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].SrcRelPath != "exp/r1/230802_0751_buzzdetect.csv" || plan.Moves[0].DstRelPath != "exp/r1/renamed_buzzdetect.csv" {
		t.Fatalf("managing the Results location's own file by its real name should need no mapping, got %+v", plan.Moves)
	}
}

func TestPlanDelete_And_ApplyDelete_ResultsRole_SingleFileResolvesThroughResultsLeaf(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "Results", Kind: LocationLocal, Role: RoleResults, RootPath: root}
	target := filepath.Join(root, "exp/r1/230802_0751_buzzdetect.csv")
	writeFile(t, target, "csv")

	ctx := context.Background()
	plan, err := PlanDelete(ctx, loc, "exp/r1/230802_0751.wma")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].RelPath != "exp/r1/230802_0751_buzzdetect.csv" {
		t.Fatalf("PlanDelete should resolve the audio-shaped relPath to the csv, got %+v", plan.Entries)
	}

	if err := ApplyDelete(ctx, loc, "exp/r1/230802_0751.wma"); err != nil {
		t.Fatal(err)
	}
	assertFileMissing(t, target)
}

func TestApplyRenames_ResultsRole_CascadesRetimeStyleRename(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "Results", Kind: LocationLocal, Role: RoleResults, RootPath: root}
	writeFile(t, filepath.Join(root, "exp/r1/230802_0751_buzzdetect.csv"), "csv")

	renames := map[string]string{"230802_0751.wma": "230802_0800.wma"}
	if err := ApplyRenames(context.Background(), loc, "exp/r1", renames); err != nil {
		t.Fatal(err)
	}
	assertFileMissing(t, filepath.Join(root, "exp/r1/230802_0751_buzzdetect.csv"))
	data, err := os.ReadFile(filepath.Join(root, "exp/r1/230802_0800_buzzdetect.csv"))
	if err != nil || string(data) != "csv" {
		t.Errorf("renamed csv missing or wrong content: %q, err=%v", data, err)
	}
}
