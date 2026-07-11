package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeFileBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	writeFile(t, path, string(data))
}

func planFor(t *testing.T, result NWayScanResult, relPath string) FileConvergencePlan {
	t.Helper()
	for _, f := range result.Files {
		if f.RelPath == relPath {
			return f
		}
	}
	t.Fatalf("no plan found for %s (files: %v)", relPath, relPathsOf(result.Files))
	return FileConvergencePlan{}
}

func relPathsOf(files []FileConvergencePlan) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.RelPath
	}
	return out
}

// TestScanNWay_LocationWithMostFilesIsNotTreatedAsEmpty is a regression test
// for exactly the bug observed against a real Teams remote: an N-way scan
// that (incorrectly) treated a location holding most of an experiment's
// files as having none of them, disagreeing with a known-correct pairwise
// scan against the same location. Three locations, one ("main") holding
// every file, the other two missing a few each — every file main has must
// show up as Exists=true for main in every plan, and never be misclassified
// as FileMissingSome-from-main.
func TestScanNWay_LocationWithMostFilesIsNotTreatedAsEmpty(t *testing.T) {
	mainRoot, aRoot, bRoot := t.TempDir(), t.TempDir(), t.TempDir()
	const exp = "Luke - Zucchini"

	files := []string{
		"2026-06-23/RecorderA/260623_0900.mp3",
		"2026-06-23/RecorderA/260623_0905.mp3",
		"2026-06-23/RecorderA/260623_0910.mp3",
		"2026-06-23/RecorderB/260623_0900.mp3",
	}
	for _, f := range files {
		writeFile(t, filepath.Join(mainRoot, exp, f), "identical-audio-bytes-"+f)
	}
	// a is missing the last file; b is missing the last two.
	for _, f := range files[:3] {
		writeFile(t, filepath.Join(aRoot, exp, f), "identical-audio-bytes-"+f)
	}
	for _, f := range files[:2] {
		writeFile(t, filepath.Join(bRoot, exp, f), "identical-audio-bytes-"+f)
	}

	locs := []Location{
		{ID: "main", Name: "main", Kind: LocationLocal, RootPath: mainRoot},
		{ID: "a", Name: "a", Kind: LocationLocal, RootPath: aRoot},
		{ID: "b", Name: "b", Kind: LocationLocal, RootPath: bRoot},
	}

	result, err := ScanNWay(context.Background(), locs, exp, DefaultFilterSettings())
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Files) != len(files) {
		t.Fatalf("got %d files in result, want %d (files: %v)", len(result.Files), len(files), relPathsOf(result.Files))
	}

	for _, f := range files {
		plan := planFor(t, result, f)
		if !plan.States[0].Exists {
			t.Errorf("%s: main.Exists = false, want true — this is the exact bug class this test guards against", f)
		}
	}

	if planFor(t, result, files[3]).Status != FileMissingSome {
		t.Errorf("%s: status = %v, want FileMissingSome (present at main only)", files[3], planFor(t, result, files[3]).Status)
	}
	if got := planFor(t, result, files[3]).States[1].Exists; got {
		t.Errorf("%s: a.Exists = true, want false", files[3])
	}
}

func TestScanNWay_AllInSync(t *testing.T) {
	roots := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	const exp = "exp"
	content := []byte("same bytes everywhere")
	for _, r := range roots {
		writeFileBytes(t, filepath.Join(r, exp, "r/f.mp3"), content)
	}
	locs := make([]Location, len(roots))
	for i, r := range roots {
		locs[i] = Location{ID: r, Name: r, Kind: LocationLocal, RootPath: r}
	}

	result, err := ScanNWay(context.Background(), locs, exp, DefaultFilterSettings())
	if err != nil {
		t.Fatal(err)
	}
	if result.InSyncCount != 1 || result.MissingSomeCount != 0 || result.ConflictCount != 0 {
		t.Fatalf("counts = in=%d missing=%d conflict=%d, want in=1 missing=0 conflict=0",
			result.InSyncCount, result.MissingSomeCount, result.ConflictCount)
	}
	if planFor(t, result, "r/f.mp3").Status != FileInSync {
		t.Errorf("status = %v, want FileInSync", planFor(t, result, "r/f.mp3").Status)
	}
}

func TestScanNWay_ConflictAmongThree(t *testing.T) {
	rootA, rootB, rootC := t.TempDir(), t.TempDir(), t.TempDir()
	const exp = "exp"
	writeFileBytes(t, filepath.Join(rootA, exp, "r/f.mp3"), []byte("version-A-bytes"))
	writeFileBytes(t, filepath.Join(rootB, exp, "r/f.mp3"), []byte("version-A-bytes")) // matches A
	writeFileBytes(t, filepath.Join(rootC, exp, "r/f.mp3"), []byte("version-C-bytes")) // differs

	locs := []Location{
		{ID: "a", Name: "a", Kind: LocationLocal, RootPath: rootA},
		{ID: "b", Name: "b", Kind: LocationLocal, RootPath: rootB},
		{ID: "c", Name: "c", Kind: LocationLocal, RootPath: rootC},
	}

	result, err := ScanNWay(context.Background(), locs, exp, DefaultFilterSettings())
	if err != nil {
		t.Fatal(err)
	}
	plan := planFor(t, result, "r/f.mp3")
	if plan.Status != FileConflict {
		t.Fatalf("status = %v, want FileConflict", plan.Status)
	}
	if plan.ConflictReason == "" {
		t.Error("expected a non-empty ConflictReason")
	}
}

// TestScanNWay_SizeCapCollision reproduces the recorder-rollover scenario
// from NOTES.md at N-way scale: two locations share an identical file, the
// third has a genuinely different recording that happens to land on the
// same byte size (as real rollover-capped recordings do 90% of the time).
// Size-only N-way comparison (the earlier design) would call this
// FileInSync; the byte-level check must catch it as FileConflict.
func TestScanNWay_SizeCapCollision(t *testing.T) {
	rootA, rootB, rootC := t.TempDir(), t.TempDir(), t.TempDir()
	const exp = "exp"
	same := make([]byte, 1000)
	for i := range same {
		same[i] = byte(i % 251)
	}
	different := make([]byte, 1000) // same size, different content
	for i := range different {
		different[i] = byte((i + 77) % 251)
	}
	writeFileBytes(t, filepath.Join(rootA, exp, "r/f.mp3"), same)
	writeFileBytes(t, filepath.Join(rootB, exp, "r/f.mp3"), same)
	writeFileBytes(t, filepath.Join(rootC, exp, "r/f.mp3"), different)

	locs := []Location{
		{ID: "a", Name: "a", Kind: LocationLocal, RootPath: rootA},
		{ID: "b", Name: "b", Kind: LocationLocal, RootPath: rootB},
		{ID: "c", Name: "c", Kind: LocationLocal, RootPath: rootC},
	}

	result, err := ScanNWay(context.Background(), locs, exp, DefaultFilterSettings())
	if err != nil {
		t.Fatal(err)
	}
	plan := planFor(t, result, "r/f.mp3")
	if plan.Status != FileConflict {
		t.Fatalf("status = %v, want FileConflict (same size, different content must not be treated as in-sync)", plan.Status)
	}
}

func TestBuildNWayTransferPlan_FanOutNotCrossProduct(t *testing.T) {
	locA := Location{ID: "a", Name: "a", Kind: LocationLocal}
	locB := Location{ID: "b", Name: "b", Kind: LocationLocal}
	locC := Location{ID: "c", Name: "c", Kind: LocationLocal}
	locD := Location{ID: "d", Name: "d", Kind: LocationLocal}

	result := NWayScanResult{
		Locations: []Location{locA, locB, locC, locD},
		Files: []FileConvergencePlan{
			{
				RelPath: "r/f.mp3",
				Status:  FileMissingSome,
				States: []FileLocationState{
					{Location: locA, Exists: true, Size: 100},
					{Location: locB, Exists: false},
					{Location: locC, Exists: false},
					{Location: locD, Exists: false},
				},
			},
		},
	}

	pairs := BuildNWayTransferPlan(result, nil)
	total := 0
	for _, p := range pairs {
		total += len(p.Files)
		if p.Source.ID != "a" {
			t.Errorf("pair source = %s, want a (the only present location)", p.Source.ID)
		}
	}
	if total != 3 {
		t.Fatalf("got %d total transfers, want 3 (fan-out to b,c,d), not a cross product", total)
	}
	if len(pairs) != 3 {
		t.Fatalf("got %d (source,dest) pairs, want 3", len(pairs))
	}
}

func TestBuildNWayTransferPlan_SkipsConflicts(t *testing.T) {
	locA := Location{ID: "a", Name: "a"}
	locB := Location{ID: "b", Name: "b"}
	result := NWayScanResult{
		Locations: []Location{locA, locB},
		Files: []FileConvergencePlan{
			{
				RelPath: "r/conflict.mp3",
				Status:  FileConflict,
				States: []FileLocationState{
					{Location: locA, Exists: true, Size: 100},
					{Location: locB, Exists: true, Size: 200},
				},
			},
		},
	}
	if pairs := BuildNWayTransferPlan(result, nil); len(pairs) != 0 {
		t.Fatalf("expected conflicts to be skipped entirely, got %d pairs", len(pairs))
	}
}

func TestBuildNWayTransferPlan_PreferLocalSource(t *testing.T) {
	remote := Location{ID: "remote", Name: "remote", Kind: LocationRemote}
	local := Location{ID: "local", Name: "local", Kind: LocationLocal}
	dest := Location{ID: "dest", Name: "dest", Kind: LocationLocal}

	result := NWayScanResult{
		Locations: []Location{remote, local, dest},
		Files: []FileConvergencePlan{
			{
				RelPath: "r/f.mp3",
				Status:  FileMissingSome,
				States: []FileLocationState{
					{Location: remote, Exists: true, Size: 100},
					{Location: local, Exists: true, Size: 100},
					{Location: dest, Exists: false},
				},
			},
		},
	}

	pairs := BuildNWayTransferPlan(result, PreferLocalSource)
	if len(pairs) != 1 || pairs[0].Source.ID != "local" {
		t.Fatalf("expected the single transfer's source to prefer local over remote, got %+v", pairs)
	}
}

func TestScanResultFromNWayTransfers(t *testing.T) {
	pair := NWayTransferPair{
		Files: []NWayTransfer{
			{RelPath: "r/a.mp3", Size: 10},
			{RelPath: "r/b.mp3", Size: 20},
		},
	}
	scan := ScanResultFromNWayTransfers(pair)
	if scan.CopyCount != 2 || scan.TotalBytes != 30 {
		t.Fatalf("scan = %+v, want CopyCount=2 TotalBytes=30", scan)
	}
	for _, e := range scan.Entries {
		if e.Action != ActionCopy {
			t.Errorf("entry %s has Action=%v, want ActionCopy", e.RelPath, e.Action)
		}
	}
}

func TestScanNWay_RejectsFewerThanTwoLocations(t *testing.T) {
	if _, err := ScanNWay(context.Background(), []Location{{ID: "only"}}, "exp", DefaultFilterSettings()); err == nil {
		t.Fatal("expected an error for fewer than 2 locations")
	}
}

// TestScanNWay_ListingErrorPropagates guards against the other failure mode
// behind the observed bug: a location that fails to list must surface an
// error, never be silently treated as present-but-empty.
func TestScanNWay_ListingErrorPropagates(t *testing.T) {
	goodRoot := t.TempDir()
	writeFile(t, filepath.Join(goodRoot, "exp", "r/f.mp3"), "bytes")

	// A local root that doesn't exist causes rclone's local backend listing
	// to fail (ErrorDirNotFound), simulating an unreachable location.
	badLoc := Location{ID: "bad", Name: "bad", Kind: LocationLocal, RootPath: filepath.Join(t.TempDir(), "does-not-exist")}
	goodLoc := Location{ID: "good", Name: "good", Kind: LocationLocal, RootPath: goodRoot}

	_, err := ScanNWay(context.Background(), []Location{goodLoc, badLoc}, "exp", DefaultFilterSettings())
	if err == nil {
		t.Fatal("expected an error when one location's listing fails, got nil")
	}
}

func TestPreferLocalSource(t *testing.T) {
	remote := Location{Kind: LocationRemote}
	local := Location{Kind: LocationLocal}
	if !PreferLocalSource(remote, local) {
		t.Error("PreferLocalSource(remote, local) = false, want true")
	}
	if PreferLocalSource(local, remote) {
		t.Error("PreferLocalSource(local, remote) = true, want false")
	}
	if PreferLocalSource(local, local) {
		t.Error("PreferLocalSource(local, local) = true, want false (keep first-found)")
	}
}

func TestScanNWay_DeterministicFileOrder(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	const exp = "exp"
	names := []string{"z.mp3", "a.mp3", "m.mp3"}
	for _, n := range names {
		writeFile(t, filepath.Join(rootA, exp, "r", n), "x")
		writeFile(t, filepath.Join(rootB, exp, "r", n), "x")
	}
	locs := []Location{
		{ID: "a", Name: "a", Kind: LocationLocal, RootPath: rootA},
		{ID: "b", Name: "b", Kind: LocationLocal, RootPath: rootB},
	}
	result, err := ScanNWay(context.Background(), locs, exp, DefaultFilterSettings())
	if err != nil {
		t.Fatal(err)
	}
	got := relPathsOf(result.Files)
	want := append([]string(nil), got...)
	sort.Strings(want)
	if len(got) != 3 {
		t.Fatalf("got %d files, want 3", len(got))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("result.Files not sorted: got %v", got)
		}
	}
}

func TestScanNWay_PopulatesModTime(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	const exp = "exp"
	writeFile(t, filepath.Join(rootA, exp, "r/f.mp3"), "x")
	writeFile(t, filepath.Join(rootB, exp, "r/f.mp3"), "x")
	locs := []Location{
		{ID: "a", Name: "a", Kind: LocationLocal, RootPath: rootA},
		{ID: "b", Name: "b", Kind: LocationLocal, RootPath: rootB},
	}
	result, err := ScanNWay(context.Background(), locs, exp, DefaultFilterSettings())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range planFor(t, result, "r/f.mp3").States {
		if !st.Exists {
			t.Fatal("expected the file at both locations")
		}
		if st.ModTime.IsZero() {
			t.Errorf("%s: ModTime is zero, want the copy's modification time (shown in the conflict resolver)", st.Location.Name)
		}
	}
}

func TestNWayDisplayScanResult_MapsStatuses(t *testing.T) {
	locA := Location{ID: "a", Name: "a"}
	locB := Location{ID: "b", Name: "b"}
	result := NWayScanResult{
		Locations: []Location{locA, locB},
		Files: []FileConvergencePlan{
			{RelPath: "r/insync.mp3", Status: FileInSync, States: []FileLocationState{
				{Location: locA, Exists: true, Size: 10}, {Location: locB, Exists: true, Size: 10},
			}},
			{RelPath: "r/missing.mp3", Status: FileMissingSome, States: []FileLocationState{
				{Location: locA, Exists: true, Size: 20}, {Location: locB},
			}},
			{RelPath: "r/conflict.mp3", Status: FileConflict, ConflictReason: "same size, different content", States: []FileLocationState{
				{Location: locA, Exists: true, Size: 30}, {Location: locB, Exists: true, Size: 30},
			}},
		},
	}

	display := NWayDisplayScanResult(result)
	if display.CopyCount != 1 || display.SkipCount != 1 || display.ConflictCount != 1 {
		t.Fatalf("counts = copy=%d skip=%d conflict=%d, want 1/1/1", display.CopyCount, display.SkipCount, display.ConflictCount)
	}
	if display.TotalBytes != 20 {
		t.Errorf("TotalBytes = %d, want 20 (only the pending copy)", display.TotalBytes)
	}
	byPath := map[string]ScanEntry{}
	for _, e := range display.Entries {
		byPath[e.RelPath] = e
	}
	if byPath["r/insync.mp3"].Action != ActionSkipIdentical {
		t.Errorf("insync mapped to %v, want ActionSkipIdentical", byPath["r/insync.mp3"].Action)
	}
	if byPath["r/missing.mp3"].Action != ActionCopy {
		t.Errorf("missing mapped to %v, want ActionCopy", byPath["r/missing.mp3"].Action)
	}
	conflict := byPath["r/conflict.mp3"]
	if conflict.Action != ActionConflict || conflict.ConflictReason == "" {
		t.Errorf("conflict mapped to %+v, want ActionConflict with a reason", conflict)
	}
}

func TestScanNWayWithProgress_EmitsLiveEntriesAndDone(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	const exp = "exp"
	writeFile(t, filepath.Join(rootA, exp, "r/only-a.mp3"), "only at a")
	writeFile(t, filepath.Join(rootA, exp, "r/same.mp3"), "same bytes")
	writeFile(t, filepath.Join(rootB, exp, "r/same.mp3"), "same bytes")
	writeFile(t, filepath.Join(rootA, exp, "r/diff.mp3"), "version A!")
	writeFile(t, filepath.Join(rootB, exp, "r/diff.mp3"), "version B!")
	locs := []Location{
		{ID: "a", Name: "a", Kind: LocationLocal, RootPath: rootA},
		{ID: "b", Name: "b", Kind: LocationLocal, RootPath: rootB},
	}

	var snaps []ScanProgress
	_, err := ScanNWayWithProgress(context.Background(), locs, exp, DefaultFilterSettings(), func(p ScanProgress) {
		snaps = append(snaps, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 {
		t.Fatal("no progress snapshots emitted")
	}

	final := snaps[len(snaps)-1]
	if !final.Done {
		t.Error("last snapshot should have Done=true")
	}
	if final.CopyCount != 1 || final.SkipCount != 1 || final.ConflictCount != 1 {
		t.Errorf("final counts = copy=%d skip=%d conflict=%d, want 1/1/1", final.CopyCount, final.SkipCount, final.ConflictCount)
	}
	if len(final.Recent) != 3 {
		t.Errorf("final Recent has %d entries, want 3 (the full live file list)", len(final.Recent))
	}
	foundDir := false
	for _, d := range final.Dirs {
		if d.Path == "r" {
			foundDir = true
			if d.ConflictCount != 1 {
				t.Errorf("dir r ConflictCount = %d, want 1 (drives the live folder conflict badge)", d.ConflictCount)
			}
		}
	}
	if !foundDir {
		t.Error("final Dirs missing the r directory")
	}
}

// TestScanNWayWithProgress_EmptyDirectoryStillAppears is a regression test
// for diffNWay silently dropping directories that hold no files needing
// classification (an empty directory, or here one whose only file is
// filtered out) from the live scan UI. The pairwise scan
// (scanAgainstDest) pre-registers every source directory upfront via
// tracker.noteDir before classifying any file; diffNWay must do the same
// (across the union of every location's directory listing) so empty
// directories show up immediately instead of never appearing at all.
func TestScanNWayWithProgress_EmptyDirectoryStillAppears(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	const exp = "exp"
	writeFile(t, filepath.Join(rootA, exp, "r/f.mp3"), "bytes")
	writeFile(t, filepath.Join(rootB, exp, "r/f.mp3"), "bytes")
	// An empty subdirectory, present only at rootA, with no files at all -
	// there is no ScanEntry that would ever cause tracker.ensureDir to be
	// called for it via addEntry.
	if err := os.MkdirAll(filepath.Join(rootA, exp, "r", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	locs := []Location{
		{ID: "a", Name: "a", Kind: LocationLocal, RootPath: rootA},
		{ID: "b", Name: "b", Kind: LocationLocal, RootPath: rootB},
	}

	var snaps []ScanProgress
	_, err := ScanNWayWithProgress(context.Background(), locs, exp, DefaultFilterSettings(), func(p ScanProgress) {
		snaps = append(snaps, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 {
		t.Fatal("no progress snapshots emitted")
	}

	final := snaps[len(snaps)-1]
	found := false
	for _, d := range final.Dirs {
		if d.Path == "r/empty" {
			found = true
		}
	}
	if !found {
		t.Errorf("final Dirs missing the empty r/empty directory (dirs: %v)", final.Dirs)
	}
}
