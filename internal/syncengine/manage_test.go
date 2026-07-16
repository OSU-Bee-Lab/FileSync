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
