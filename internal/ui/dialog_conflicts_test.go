package ui

import (
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

func TestCollectConflicts(t *testing.T) {
	tasks := []scanTask{{Label: "exp-a"}, {Label: "exp-b"}}
	results := []syncengine.ScanResult{
		{
			Entries: []syncengine.ScanEntry{
				{RelPath: "r/copy.mp3", Size: 10, Action: syncengine.ActionCopy},
				{RelPath: "r/skip.mp3", Size: 20, Action: syncengine.ActionSkipIdentical},
				{RelPath: "r/conflict.mp3", Size: 30, DstSize: 25, Action: syncengine.ActionConflict, ConflictReason: "different size and content"},
			},
		},
		{
			Entries: []syncengine.ScanEntry{
				{RelPath: "r/other-conflict.mp3", Size: 40, DstSize: 40, Action: syncengine.ActionConflict, ConflictReason: "same size, different content"},
			},
		},
	}

	conflicts := collectConflicts(tasks, results)
	if len(conflicts) != 2 {
		t.Fatalf("collectConflicts returned %d entries, want 2: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].TaskLabel != "exp-a" || conflicts[0].RelPath != "r/conflict.mp3" || conflicts[0].SrcSize != 30 || conflicts[0].DstSize != 25 {
		t.Errorf("unexpected first conflict: %+v", conflicts[0])
	}
	if conflicts[1].TaskLabel != "exp-b" || conflicts[1].RelPath != "r/other-conflict.mp3" {
		t.Errorf("unexpected second conflict: %+v", conflicts[1])
	}
}

func TestBuildExpUIStateCarriesConflictInfo(t *testing.T) {
	result := syncengine.ScanResult{
		Entries: []syncengine.ScanEntry{
			{RelPath: "r/conflict.mp3", Size: 30, DstSize: 25, Action: syncengine.ActionConflict, ConflictReason: "different size and content"},
			{RelPath: "r/skip.mp3", Size: 20, Action: syncengine.ActionSkipIdentical},
		},
	}
	exp := buildExpUIState("exp-a", result)

	conflict, ok := exp.fileMap["r/conflict.mp3"]
	if !ok {
		t.Fatal("conflict entry missing from fileMap")
	}
	if conflict.action != syncengine.ActionConflict {
		t.Errorf("conflict.action = %v, want ActionConflict", conflict.action)
	}
	if conflict.dstSize != 25 {
		t.Errorf("conflict.dstSize = %d, want 25", conflict.dstSize)
	}
	if conflict.conflictReason != "different size and content" {
		t.Errorf("conflict.conflictReason = %q, want %q", conflict.conflictReason, "different size and content")
	}
	if conflict.done {
		t.Error("conflict.done = true, want false: a conflict is never treated as already-synced")
	}

	skip, ok := exp.fileMap["r/skip.mp3"]
	if !ok {
		t.Fatal("skip entry missing from fileMap")
	}
	if !skip.done {
		t.Error("skip.done = false, want true")
	}
}

// resolverFixture builds a resolver over two experiments: exp-a has two
// conflicts (one present at all three locations, one at only two), exp-b has
// one conflict.
func resolverFixture() (*nwayResolver, []syncengine.Location) {
	locA := syncengine.Location{ID: "a", Name: "Lab NAS"}
	locB := syncengine.Location{ID: "b", Name: "SharePoint"}
	locC := syncengine.Location{ID: "c", Name: "Laptop"}

	r := newNWayResolver([]string{"exp-a", "exp-b"})
	r.results[0] = syncengine.NWayScanResult{
		Locations: []syncengine.Location{locA, locB, locC},
		Files: []syncengine.FileConvergencePlan{
			{RelPath: "r/ok.mp3", Status: syncengine.FileInSync},
			{RelPath: "r/three-way.mp3", Status: syncengine.FileConflict, ConflictReason: "different size and content",
				States: []syncengine.FileLocationState{
					{Location: locA, Exists: true, Size: 100},
					{Location: locB, Exists: true, Size: 90},
					{Location: locC, Exists: true, Size: 80},
				}},
			{RelPath: "r/two-way.mp3", Status: syncengine.FileConflict, ConflictReason: "same size, different content",
				States: []syncengine.FileLocationState{
					{Location: locA, Exists: false},
					{Location: locB, Exists: true, Size: 50},
					{Location: locC, Exists: true, Size: 50},
				}},
		},
	}
	r.results[1] = syncengine.NWayScanResult{
		Locations: []syncengine.Location{locA, locB, locC},
		Files: []syncengine.FileConvergencePlan{
			{RelPath: "r/other.mp3", Status: syncengine.FileConflict, ConflictReason: "different size, same start (possible partial upload)",
				States: []syncengine.FileLocationState{
					{Location: locA, Exists: true, Size: 10},
					{Location: locB, Exists: true, Size: 5},
					{Location: locC, Exists: false},
				}},
		},
	}
	return r, []syncengine.Location{locA, locB, locC}
}

func TestNWayResolver_ConflictsAndGate(t *testing.T) {
	r, locs := resolverFixture()

	conflicts := r.conflicts()
	if len(conflicts) != 3 {
		t.Fatalf("conflicts() returned %d, want 3: %+v", len(conflicts), conflicts)
	}
	if conflicts[2].key != (nwayConflictKey{expName: "exp-b", relPath: "r/other.mp3"}) {
		t.Errorf("unexpected third conflict key: %+v", conflicts[2].key)
	}
	if len(conflicts[1].versions) != 2 {
		t.Errorf("two-way conflict has %d versions, want 2 (only present copies)", len(conflicts[1].versions))
	}
	if r.unresolvedCount() != 3 {
		t.Fatalf("unresolvedCount = %d, want 3 (nothing decided yet — no default)", r.unresolvedCount())
	}

	// A keep-one choice with no winner and a delete with no locations are
	// both still undecided — the gate must not open for half-made choices.
	r.choices[conflicts[0].key] = nwayChoice{kind: nwayChoiceKeepOne}
	r.choices[conflicts[1].key] = nwayChoice{kind: nwayChoiceDelete}
	if r.unresolvedCount() != 3 {
		t.Errorf("unresolvedCount = %d, want 3 (incomplete choices don't count)", r.unresolvedCount())
	}

	r.choices[conflicts[0].key] = nwayChoice{kind: nwayChoiceKeepOne, winner: locs[0]}
	r.choices[conflicts[1].key] = nwayChoice{kind: nwayChoiceSkip}
	if r.unresolvedCount() != 1 {
		t.Errorf("unresolvedCount = %d, want 1", r.unresolvedCount())
	}
	if r.hasDeletes() {
		t.Error("hasDeletes = true, want false")
	}
	if !r.hasActionable() {
		t.Error("hasActionable = false, want true (an overwrite is real work)")
	}
}

func TestNWayResolver_BuildResolutions(t *testing.T) {
	r, locs := resolverFixture()
	conflicts := r.conflicts()

	r.choices[conflicts[0].key] = nwayChoice{kind: nwayChoiceKeepOne, winner: locs[1]}
	r.choices[conflicts[1].key] = nwayChoice{kind: nwayChoiceKeepAll}
	r.choices[conflicts[2].key] = nwayChoice{kind: nwayChoiceDelete, deleteLoc: []syncengine.Location{locs[1]}}

	res := r.buildResolutions()
	// keep-one → 1, keep-all over 2 present copies → 2 renames, delete → 1.
	if len(res) != 4 {
		t.Fatalf("got %d resolutions, want 4: %+v", len(res), res)
	}

	if res[0].Kind != syncengine.NWayOverwrite || res[0].WinnerLocationID != "b" || res[0].ExpName != "exp-a" {
		t.Errorf("unexpected overwrite resolution: %+v", res[0])
	}

	renames := res[1:3]
	names := map[string]bool{}
	for _, rn := range renames {
		if rn.Kind != syncengine.NWayRename || len(rn.TargetLocationIDs) != 1 {
			t.Fatalf("expected one single-target rename per present copy, got %+v", rn)
		}
		if names[rn.NewName] {
			t.Errorf("duplicate rename target name %q — differing copies renamed to one name would recreate the conflict", rn.NewName)
		}
		names[rn.NewName] = true
	}

	if res[3].Kind != syncengine.NWayDelete || res[3].ExpName != "exp-b" || len(res[3].TargetLocationIDs) != 1 || res[3].TargetLocationIDs[0] != "b" {
		t.Errorf("unexpected delete resolution: %+v", res[3])
	}
	if !r.hasDeletes() {
		t.Error("hasDeletes = false, want true")
	}
}

func TestNWayResolver_ApplyChoiceToUnresolved(t *testing.T) {
	r, locs := resolverFixture()
	conflicts := r.conflicts()

	// Keep Lab NAS's version everywhere it's applicable: the two-way
	// conflict has no Lab NAS copy, so it must stay unresolved rather than
	// be given an impossible winner.
	r.applyChoiceToUnresolved(nwayChoice{kind: nwayChoiceKeepOne, winner: locs[0]})
	if r.unresolvedCount() != 1 {
		t.Fatalf("unresolvedCount = %d, want 1 (two-way conflict has no Lab NAS copy)", r.unresolvedCount())
	}
	if got := r.choices[conflicts[1].key]; got.decided() {
		t.Errorf("two-way conflict should be untouched, got %+v", got)
	}

	// Already-decided conflicts are never overwritten by a later bulk apply.
	r.applyChoiceToUnresolved(nwayChoice{kind: nwayChoiceSkip})
	if got := r.choices[conflicts[0].key]; got.kind != nwayChoiceKeepOne {
		t.Errorf("bulk skip overwrote an existing decision: %+v", got)
	}
	if r.unresolvedCount() != 0 {
		t.Errorf("unresolvedCount = %d, want 0", r.unresolvedCount())
	}

	// Deletes never bulk-apply.
	r2, locs2 := resolverFixture()
	r2.applyChoiceToUnresolved(nwayChoice{kind: nwayChoiceDelete, deleteLoc: []syncengine.Location{locs2[0]}})
	if r2.unresolvedCount() != 3 {
		t.Errorf("unresolvedCount = %d, want 3 (delete must stay a per-file decision)", r2.unresolvedCount())
	}
}

func TestNWayResolver_RowSummary(t *testing.T) {
	r, locs := resolverFixture()
	key := nwayConflictKey{expName: "exp-a", relPath: "r/three-way.mp3"}

	if got := r.rowSummary("exp-a", "r/three-way.mp3", "different size and content"); got != "⚠ conflict — different size and content" {
		t.Errorf("undecided summary = %q", got)
	}
	r.choices[key] = nwayChoice{kind: nwayChoiceKeepOne, winner: locs[0]}
	if got := r.rowSummary("exp-a", "r/three-way.mp3", "x"); got != "✓ keeping Lab NAS's version" {
		t.Errorf("keep-one summary = %q", got)
	}
	r.choices[key] = nwayChoice{kind: nwayChoiceSkip}
	if got := r.rowSummary("exp-a", "r/three-way.mp3", "x"); got != "— not syncing" {
		t.Errorf("skip summary = %q", got)
	}
	r.choices[key] = nwayChoice{kind: nwayChoiceDelete, deleteLoc: []syncengine.Location{locs[1], locs[2]}}
	if got := r.rowSummary("exp-a", "r/three-way.mp3", "x"); got != "✗ deleting from SharePoint, Laptop" {
		t.Errorf("delete summary = %q", got)
	}
}
