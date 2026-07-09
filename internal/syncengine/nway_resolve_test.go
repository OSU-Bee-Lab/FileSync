package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyOverwriteResolutions_WinnerPropagates(t *testing.T) {
	locA := Location{ID: "a", Name: "a"}
	locB := Location{ID: "b", Name: "b"}
	locC := Location{ID: "c", Name: "c"}

	result := NWayScanResult{
		Locations:     []Location{locA, locB, locC},
		ConflictCount: 1,
		Files: []FileConvergencePlan{
			{
				RelPath: "r/f.mp3",
				Status:  FileConflict,
				States: []FileLocationState{
					{Location: locA, Exists: true, Size: 100},
					{Location: locB, Exists: true, Size: 200},
					{Location: locC, Exists: false},
				},
			},
		},
	}

	resolved := ApplyOverwriteResolutions(result, []NWayConflictResolution{
		{RelPath: "r/f.mp3", Kind: NWayOverwrite, WinnerLocationID: "a"},
	})

	if resolved.ConflictCount != 0 || resolved.MissingSomeCount != 1 {
		t.Fatalf("counts = conflict=%d missing=%d, want conflict=0 missing=1", resolved.ConflictCount, resolved.MissingSomeCount)
	}
	plan := resolved.Files[0]
	if plan.Status != FileMissingSome {
		t.Fatalf("status = %v, want FileMissingSome", plan.Status)
	}
	if !plan.States[0].Exists {
		t.Error("winner (a) should still be Exists=true")
	}
	if plan.States[1].Exists {
		t.Error("loser (b) should now be Exists=false so it receives the winner's copy")
	}
	if plan.States[2].Exists {
		t.Error("previously-missing (c) should remain Exists=false")
	}

	pairs := BuildNWayTransferPlan(resolved, nil)
	total := 0
	for _, p := range pairs {
		if p.Source.ID != "a" {
			t.Errorf("transfer source = %s, want a (the winner)", p.Source.ID)
		}
		total += len(p.Files)
	}
	if total != 2 {
		t.Fatalf("got %d transfers, want 2 (a->b overwrite, a->c fill-in)", total)
	}
}

func TestApplyOverwriteResolutions_IgnoresUnresolvedConflicts(t *testing.T) {
	locA := Location{ID: "a", Name: "a"}
	locB := Location{ID: "b", Name: "b"}
	result := NWayScanResult{
		Locations:     []Location{locA, locB},
		ConflictCount: 1,
		Files: []FileConvergencePlan{
			{
				RelPath: "r/untouched.mp3",
				Status:  FileConflict,
				States: []FileLocationState{
					{Location: locA, Exists: true, Size: 100},
					{Location: locB, Exists: true, Size: 200},
				},
			},
		},
	}
	// No resolution passed at all (default Ignore).
	resolved := ApplyOverwriteResolutions(result, nil)
	if resolved.Files[0].Status != FileConflict {
		t.Fatalf("status = %v, want FileConflict (untouched)", resolved.Files[0].Status)
	}
	if resolved.ConflictCount != 1 {
		t.Fatalf("ConflictCount = %d, want 1", resolved.ConflictCount)
	}
}

func TestRenameConflictFile_PreservesContentAndPropagates(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	original := filepath.Join(root, "exp/r/conflict.mp3")
	writeFile(t, original, "the odd one out")

	ctx := context.Background()
	newRelPath := "exp/r/" + SuggestConflictRenameName("exp/r/conflict.mp3")
	if err := RenameConflictFile(ctx, loc, "exp/r/conflict.mp3", newRelPath); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(original); err == nil {
		t.Error("original path should no longer exist after rename")
	}
	newPath := filepath.Join(root, newRelPath)
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("renamed file not found at %s: %v", newPath, err)
	}
	if string(data) != "the odd one out" {
		t.Errorf("renamed file content = %q, want %q (content must survive a rename)", data, "the odd one out")
	}
	if newRelPath == "exp/r/conflict.mp3" {
		t.Error("renamed path should differ from the original")
	}
}

func TestRenameConflictFile_UserSuppliedName(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	writeFile(t, filepath.Join(root, "exp/r/conflict.mp3"), "custom name test")

	ctx := context.Background()
	if err := RenameConflictFile(ctx, loc, "exp/r/conflict.mp3", "exp/r/my-custom-name.mp3"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "exp/r/my-custom-name.mp3"))
	if err != nil {
		t.Fatalf("renamed file not found at user-supplied name: %v", err)
	}
	if string(data) != "custom name test" {
		t.Errorf("content = %q, want %q", data, "custom name test")
	}
}

func TestDeleteConflictFile_RemovesFile(t *testing.T) {
	root := t.TempDir()
	loc := Location{ID: "loc", Name: "MyLocation", Kind: LocationLocal, RootPath: root}
	target := filepath.Join(root, "exp/r/bad.mp3")
	writeFile(t, target, "delete me")

	if err := DeleteConflictFile(context.Background(), loc, "exp/r/bad.mp3"); err != nil {
		t.Fatal(err)
	}
	assertFileMissing(t, target)
}

// TestRenameThenRescan_PropagatesAsOrdinaryMissingFile is the true
// end-to-end proof for "the renamed file should also get synced
// everywhere": rename a conflicting copy at one of three locations, then
// re-scan — the renamed file must show up as an ordinary FileMissingSome
// entry (present only at the renaming location), with no special-case
// code needed to make it propagate.
func TestRenameThenRescan_PropagatesAsOrdinaryMissingFile(t *testing.T) {
	rootA, rootB, rootC := t.TempDir(), t.TempDir(), t.TempDir()
	const exp = "exp"
	writeFile(t, filepath.Join(rootA, exp, "r/f.mp3"), "version A")
	writeFile(t, filepath.Join(rootB, exp, "r/f.mp3"), "version A") // agrees with a
	writeFile(t, filepath.Join(rootC, exp, "r/f.mp3"), "version C") // conflict

	locA := Location{ID: "a", Name: "a", Kind: LocationLocal, RootPath: rootA}
	locB := Location{ID: "b", Name: "b", Kind: LocationLocal, RootPath: rootB}
	locC := Location{ID: "c", Name: "c", Kind: LocationLocal, RootPath: rootC}
	locs := []Location{locA, locB, locC}
	ctx := context.Background()

	before, err := ScanNWay(ctx, locs, exp, DefaultFilterSettings())
	if err != nil {
		t.Fatal(err)
	}
	if planFor(t, before, "r/f.mp3").Status != FileConflict {
		t.Fatal("expected a conflict before renaming")
	}

	// RenameConflictFile is called with the full path including the
	// experiment prefix (matching how the UI would call it against a
	// Location's root, not a ScanNWay-relative path).
	newRelPathInExp := "r/" + SuggestConflictRenameName("r/f.mp3")
	newRelPath := exp + "/" + newRelPathInExp
	if err := RenameConflictFile(ctx, locC, exp+"/r/f.mp3", newRelPath); err != nil {
		t.Fatal(err)
	}

	after, err := ScanNWay(ctx, locs, exp, DefaultFilterSettings())
	if err != nil {
		t.Fatal(err)
	}

	// The original path is now only present at a and b, in agreement -
	// FileMissingSome (c needs it), not FileConflict.
	orig := planFor(t, after, "r/f.mp3")
	if orig.Status != FileMissingSome {
		t.Fatalf("original path status = %v, want FileMissingSome (a and b agree, c no longer has a conflicting copy)", orig.Status)
	}

	renamed := planFor(t, after, newRelPathInExp)
	if renamed.Status != FileMissingSome {
		t.Fatalf("renamed path status = %v, want FileMissingSome", renamed.Status)
	}
	if !renamed.States[2].Exists {
		t.Error("renamed file should exist at c (where it was renamed)")
	}
	if renamed.States[0].Exists || renamed.States[1].Exists {
		t.Error("renamed file should not exist yet at a or b — that's exactly the propagation the transfer plan must now handle")
	}

	pairs := BuildNWayTransferPlan(after, nil)
	foundRenamedTransfer := false
	for _, p := range pairs {
		for _, f := range p.Files {
			if f.RelPath == renamed.RelPath && p.Source.ID == "c" {
				foundRenamedTransfer = true
			}
		}
	}
	if !foundRenamedTransfer {
		t.Error("expected the transfer plan to propagate the renamed file from c to the other locations, with no special-case code")
	}
}

func TestSuggestConflictRenameNameAt_DistinctPerLocationAndSanitized(t *testing.T) {
	a := SuggestConflictRenameNameAt("r/f.mp3", "Lab NAS")
	b := SuggestConflictRenameNameAt("r/f.mp3", "SharePoint: Bees/2026")
	if a == b {
		t.Fatalf("names for different locations must differ, both were %q", a)
	}
	if !strings.HasPrefix(a, "f (Lab NAS conflict copy ") || !strings.HasSuffix(a, ").mp3") {
		t.Errorf("unexpected shape: %q", a)
	}
	for _, c := range []string{"/", "\\", ":", "*", "?", "<", ">", "|"} {
		if strings.Contains(b, c) {
			t.Errorf("sanitized name %q still contains %q", b, c)
		}
	}
}
