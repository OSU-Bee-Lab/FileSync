package syncengine

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestCoveringDirs(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{
			name:  "empty input needs the whole root",
			paths: nil,
			want:  nil,
		},
		{
			name:  "a file at the root needs the whole root",
			paths: []string{"2026-06-23/RecorderA/a.mp3", "README.txt"},
			want:  nil,
		},
		{
			name:  "one dir per recorder",
			paths: []string{"2026-06-23/RecorderA/a.mp3", "2026-06-23/RecorderA/b.mp3", "2026-06-23/RecorderB/a.mp3"},
			want:  []string{"2026-06-23/RecorderA", "2026-06-23/RecorderB"},
		},
		{
			name:  "a nested dir is dropped when its ancestor is already covered",
			paths: []string{"2026-06-23/RecorderA/a.mp3", "2026-06-23/RecorderA/sub/b.mp3"},
			want:  []string{"2026-06-23/RecorderA"},
		},
		{
			name:  "a sibling sharing a name prefix is not mistaken for a descendant",
			paths: []string{"d/RecorderA/a.mp3", "d/RecorderA2/b.mp3"},
			want:  []string{"d/RecorderA", "d/RecorderA2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CoveringDirs(tc.paths)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("CoveringDirs(%v) = %v, want %v", tc.paths, got, tc.want)
			}
		})
	}
}

// TestScanNWayScoped_MatchesFullScanWithinScopes is the core guarantee the
// batch-upload narrowing rests on: for every path inside the scopes, a scoped
// scan must produce exactly what an unscoped scan of the whole experiment
// produces — same root-relative RelPath, same per-location Exists, same
// status. Anything outside the scopes is simply absent.
func TestScanNWayScoped_MatchesFullScanWithinScopes(t *testing.T) {
	mainRoot, aRoot := t.TempDir(), t.TempDir()
	const exp = "Luke - Zucchini"

	// An older deployment (outside the scopes, and much larger) plus the one
	// this "session" just offloaded.
	old := []string{
		"2026-05-01/RecorderA/260501_0900.mp3",
		"2026-05-01/RecorderA/260501_0905.mp3",
		"2026-05-01/RecorderB/260501_0900.mp3",
	}
	fresh := []string{
		"2026-06-23/RecorderA/260623_0900.mp3",
		"2026-06-23/RecorderA/260623_0905.mp3",
		"2026-06-23/RecorderB/260623_0900.mp3",
	}
	for _, f := range append(append([]string{}, old...), fresh...) {
		writeFile(t, filepath.Join(mainRoot, exp, f), "audio-"+f)
	}
	// a has the old deployment and only the first fresh file, so the other
	// two must come back FileMissingSome.
	for _, f := range append(append([]string{}, old...), fresh[0]) {
		writeFile(t, filepath.Join(aRoot, exp, f), "audio-"+f)
	}

	locs := []Location{
		{ID: "main", Name: "main", Kind: LocationLocal, RootPath: mainRoot},
		{ID: "a", Name: "a", Kind: LocationLocal, RootPath: aRoot},
	}

	scopes := CoveringDirs(fresh)
	want := []string{"2026-06-23/RecorderA", "2026-06-23/RecorderB"}
	if !reflect.DeepEqual(scopes, want) {
		t.Fatalf("CoveringDirs = %v, want %v", scopes, want)
	}

	ctx := context.Background()
	full, err := ScanNWay(ctx, locs, exp, DefaultFilterSettings(), NWayFullScan)
	if err != nil {
		t.Fatalf("full scan: %v", err)
	}
	scoped, err := ScanNWayScopedWithProgress(ctx, locs, exp, scopes, DefaultFilterSettings(), nil, NWayFullScan)
	if err != nil {
		t.Fatalf("scoped scan: %v", err)
	}

	gotPaths := relPathsOf(scoped.Files)
	sort.Strings(gotPaths)
	wantPaths := append([]string{}, fresh...)
	sort.Strings(wantPaths)
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("scoped scan covered %v, want exactly the scoped files %v", gotPaths, wantPaths)
	}

	for _, f := range fresh {
		got, exp := planFor(t, scoped, f), planFor(t, full, f)
		if got.Status != exp.Status {
			t.Errorf("%s: scoped status %v, full scan says %v", f, got.Status, exp.Status)
		}
		if len(got.States) != len(exp.States) {
			t.Fatalf("%s: scoped has %d states, full scan has %d", f, len(got.States), len(exp.States))
		}
		for i := range got.States {
			if got.States[i].Location.ID != exp.States[i].Location.ID {
				t.Errorf("%s: state %d is %s, full scan says %s", f, i, got.States[i].Location.ID, exp.States[i].Location.ID)
			}
			if got.States[i].Exists != exp.States[i].Exists {
				t.Errorf("%s at %s: scoped Exists=%v, full scan says %v",
					f, got.States[i].Location.ID, got.States[i].Exists, exp.States[i].Exists)
			}
			if got.States[i].Size != exp.States[i].Size {
				t.Errorf("%s at %s: scoped Size=%d, full scan says %d",
					f, got.States[i].Location.ID, got.States[i].Size, exp.States[i].Size)
			}
		}
	}

	if scoped.MissingSomeCount != 2 {
		t.Errorf("MissingSomeCount = %d, want 2 (the two fresh files a lacks)", scoped.MissingSomeCount)
	}
}

// TestScanNWayScoped_ScopeMissingAtOneLocation covers the case that motivated
// the whole change: the scanned subtree doesn't exist at all at one location
// (a remote that has never seen this experiment). That must come back as
// "every file missing there", not as a listing error.
func TestScanNWayScoped_ScopeMissingAtOneLocation(t *testing.T) {
	mainRoot, emptyRoot := t.TempDir(), t.TempDir()
	const exp = "Luke - Zucchini"

	fresh := []string{
		"2026-06-23/RecorderA/260623_0900.mp3",
		"2026-06-23/RecorderA/260623_0905.mp3",
	}
	for _, f := range fresh {
		writeFile(t, filepath.Join(mainRoot, exp, f), "audio-"+f)
	}
	// emptyRoot exists as a location but holds no experiment at all.

	locs := []Location{
		{ID: "main", Name: "main", Kind: LocationLocal, RootPath: mainRoot},
		{ID: "remote", Name: "remote", Kind: LocationLocal, RootPath: emptyRoot},
	}

	scoped, err := ScanNWayScopedWithProgress(context.Background(), locs, exp,
		CoveringDirs(fresh), DefaultFilterSettings(), nil, NWayQuickScan)
	if err != nil {
		t.Fatalf("scoped scan: %v", err)
	}

	if scoped.MissingSomeCount != len(fresh) {
		t.Fatalf("MissingSomeCount = %d, want %d", scoped.MissingSomeCount, len(fresh))
	}
	for _, f := range fresh {
		plan := planFor(t, scoped, f)
		if !plan.States[0].Exists {
			t.Errorf("%s: expected present at main", f)
		}
		if plan.States[1].Exists {
			t.Errorf("%s: expected absent at remote", f)
		}
	}

	// And the transfer plan must still name the file by its full
	// experiment-relative path, since that's what the copy engine filters on.
	pairs := BuildNWayTransferPlan(scoped, PreferLocalSource)
	if len(pairs) != 1 {
		t.Fatalf("got %d transfer pairs, want 1", len(pairs))
	}
	var got []string
	for _, f := range pairs[0].Files {
		got = append(got, f.RelPath)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, fresh) {
		t.Errorf("transfer paths = %v, want %v", got, fresh)
	}
}
