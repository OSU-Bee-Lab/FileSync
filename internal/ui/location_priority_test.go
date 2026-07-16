package ui

import (
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

func loc(name string, kind syncengine.LocationKind, priority int) syncengine.Location {
	return syncengine.Location{ID: name, Name: name, Kind: kind, Priority: priority}
}

func names(locs []syncengine.Location) []string {
	out := make([]string, len(locs))
	for i, l := range locs {
		out[i] = l.Name
	}
	return out
}

func TestNormalizedLocationOrderGroupsLocalsBeforeRemotes(t *testing.T) {
	locs := []syncengine.Location{
		loc("remote-a", syncengine.LocationRemote, 1),
		loc("local-a", syncengine.LocationLocal, 1),
		loc("remote-b", syncengine.LocationRemote, 2),
		loc("local-b", syncengine.LocationLocal, 2),
	}
	got := normalizedLocationOrder(locs)
	want := []string{"local-a", "local-b", "remote-a", "remote-b"}
	if gotNames := names(got); !equal(gotNames, want) {
		t.Fatalf("order = %v, want %v", gotNames, want)
	}
}

func TestNormalizedLocationOrderRespectsExplicitPriority(t *testing.T) {
	locs := []syncengine.Location{
		loc("slow-drive", syncengine.LocationLocal, 2),
		loc("fast-drive", syncengine.LocationLocal, 1),
	}
	got := normalizedLocationOrder(locs)
	if gotNames := names(got); !equal(gotNames, []string{"fast-drive", "slow-drive"}) {
		t.Fatalf("order = %v, want [fast-drive slow-drive]", gotNames)
	}
	if got[0].Priority != 1 || got[1].Priority != 2 {
		t.Fatalf("priorities not renumbered 1..n: %+v", got)
	}
}

// TestNormalizedLocationOrderFallsBackAlphabetically covers the freeze fix's
// companion request: a legacy config where Priority was never set (all
// zero) should land in alphabetical order rather than arbitrary slice
// order, so upgrading an old config is predictable.
func TestNormalizedLocationOrderFallsBackAlphabetically(t *testing.T) {
	locs := []syncengine.Location{
		loc("zebra", syncengine.LocationLocal, 0),
		loc("apple", syncengine.LocationLocal, 0),
		loc("mango", syncengine.LocationLocal, 0),
	}
	got := normalizedLocationOrder(locs)
	want := []string{"apple", "mango", "zebra"}
	if gotNames := names(got); !equal(gotNames, want) {
		t.Fatalf("order = %v, want %v", gotNames, want)
	}
}

func TestNormalizedLocationOrderIsIdempotent(t *testing.T) {
	locs := []syncengine.Location{
		loc("b", syncengine.LocationLocal, 0),
		loc("a", syncengine.LocationLocal, 0),
	}
	once := normalizedLocationOrder(locs)
	twice := normalizedLocationOrder(once)
	if !equal(names(once), names(twice)) {
		t.Fatalf("not idempotent: once=%v twice=%v", names(once), names(twice))
	}
}

func TestLocationsMovedToPositionReordersOnlyWithinKind(t *testing.T) {
	locs := []syncengine.Location{
		loc("local-1", syncengine.LocationLocal, 1),
		loc("local-2", syncengine.LocationLocal, 2),
		loc("local-3", syncengine.LocationLocal, 3),
		loc("remote-1", syncengine.LocationRemote, 1),
	}
	// Move local-3 (cfg index 2) to the front of the locals.
	got := locationsMovedToPosition(locs, 2, 0)
	want := []string{"local-3", "local-1", "local-2", "remote-1"}
	if gotNames := names(got); !equal(gotNames, want) {
		t.Fatalf("order = %v, want %v", gotNames, want)
	}
	for i, l := range got {
		if l.Kind != syncengine.LocationRemote && l.Priority != indexInLocals(got, i)+1 {
			t.Fatalf("priority not renumbered: %+v", got)
		}
	}
}

func indexInLocals(locs []syncengine.Location, i int) int {
	pos := 0
	for j := 0; j < i; j++ {
		if locs[j].Kind == locs[i].Kind {
			pos++
		}
	}
	return pos
}

func TestLocationsMovedToPositionMovingRemoteLeavesLocalsUntouched(t *testing.T) {
	locs := []syncengine.Location{
		loc("local-1", syncengine.LocationLocal, 1),
		loc("remote-1", syncengine.LocationRemote, 1),
		loc("remote-2", syncengine.LocationRemote, 2),
	}
	// Move remote-2 (cfg index 2) ahead of remote-1.
	got := locationsMovedToPosition(locs, 2, 0)
	want := []string{"local-1", "remote-2", "remote-1"}
	if gotNames := names(got); !equal(gotNames, want) {
		t.Fatalf("order = %v, want %v", gotNames, want)
	}
}

func TestLocationsMovedToPositionClampsOutOfRange(t *testing.T) {
	locs := []syncengine.Location{
		loc("a", syncengine.LocationLocal, 1),
		loc("b", syncengine.LocationLocal, 2),
	}
	got := locationsMovedToPosition(locs, 0, 99)
	if gotNames := names(got); !equal(gotNames, []string{"b", "a"}) {
		t.Fatalf("order = %v, want [b a] (clamped to end)", gotNames)
	}

	got = locationsMovedToPosition(locs, 1, -5)
	if gotNames := names(got); !equal(gotNames, []string{"b", "a"}) {
		t.Fatalf("order = %v, want [b a] (clamped to start)", gotNames)
	}
}

func TestSeamToFinalPos(t *testing.T) {
	cases := []struct {
		from, seam, want int
	}{
		{from: 0, seam: 0, want: 0},  // drop back in place, before itself
		{from: 0, seam: 1, want: 0},  // drop just after itself: no-op position
		{from: 0, seam: 2, want: 1},  // drop two seams down
		{from: 2, seam: 0, want: 0},  // drag up to the very top
		{from: 2, seam: 2, want: 2},  // drop back in place
		{from: 2, seam: 3, want: 2},  // drop just after itself: no-op position
		{from: 2, seam: 4, want: 3},  // drag down past the end
	}
	for _, c := range cases {
		if got := seamToFinalPos(c.from, c.seam); got != c.want {
			t.Errorf("seamToFinalPos(%d, %d) = %d, want %d", c.from, c.seam, got, c.want)
		}
	}
}

// TestMoveBySeamNoOpWhenDroppedInPlace guards the "only reorder when the
// drop target actually changed" check in addRankedSection: for both seams
// adjacent to a row's own position, the resulting final position must equal
// its starting position (from), which is what that check compares against.
func TestMoveBySeamNoOpWhenDroppedInPlace(t *testing.T) {
	for _, from := range []int{0, 1, 2} {
		for _, seam := range []int{from, from + 1} {
			if got := seamToFinalPos(from, seam); got != from {
				t.Errorf("seamToFinalPos(%d, %d) = %d, want %d (no-op)", from, seam, got, from)
			}
		}
	}
}

func equal(a, b []string) bool {
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
