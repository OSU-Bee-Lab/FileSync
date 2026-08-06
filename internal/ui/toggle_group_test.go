package ui

import (
	"strings"
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// badgeNumbers renders the group's badge state as "name=number" pairs in
// display order, skipping unselected (0) chips.
func badgeNumbers(g *toggleGroup) string {
	var parts []string
	for _, name := range g.options {
		if n := g.chips[name].number; n != 0 {
			parts = append(parts, name+"="+string(rune('0'+n)))
		}
	}
	return strings.Join(parts, " ")
}

func TestToggleGroup_NumbersFollowDisplayOrderNotTapOrder(t *testing.T) {
	g := newToggleGroup([]string{"a", "b", "c"}, nil, nil)

	// Tapped worst-case: last, first, middle.
	g.toggle("c")
	g.toggle("a")
	g.toggle("b")

	if got, want := badgeNumbers(g), "a=1 b=2 c=3"; got != want {
		t.Errorf("badges = %q, want %q: numbering must not depend on tap order", got, want)
	}
	if got, want := strings.Join(g.Selected(), ","), "a,b,c"; got != want {
		t.Errorf("Selected() = %q, want %q", got, want)
	}
}

func TestToggleGroup_NumbersStayContiguousAfterDeselect(t *testing.T) {
	g := newToggleGroup([]string{"a", "b", "c"}, []string{"a", "b", "c"}, nil)

	g.toggle("b")
	if got, want := badgeNumbers(g), "a=1 c=2"; got != want {
		t.Errorf("badges = %q, want %q: deselecting the middle chip must not leave a gap", got, want)
	}

	// Re-selecting it puts it back at its display position, not at the end.
	g.toggle("b")
	if got, want := badgeNumbers(g), "a=1 b=2 c=3"; got != want {
		t.Errorf("badges = %q, want %q", got, want)
	}
}

func TestToggleGroup_SetSelectedIgnoresTheOrderGiven(t *testing.T) {
	g := newToggleGroup([]string{"a", "b", "c"}, nil, nil)
	g.SetSelected([]string{"c", "a"})

	if got, want := badgeNumbers(g), "a=1 c=2"; got != want {
		t.Errorf("badges = %q, want %q", got, want)
	}
	if got, want := strings.Join(g.Selected(), ","), "a,c"; got != want {
		t.Errorf("Selected() = %q, want %q", got, want)
	}
}

func TestNewRoleToggleGroup_RanksWithinEachRole(t *testing.T) {
	// Deliberately unordered input: a remote before locals, and each role's
	// locals out of Priority order.
	locs := []syncengine.Location{
		{ID: "a-cloud", Name: "a-cloud", Kind: syncengine.LocationRemote, Role: syncengine.RoleAudio, Priority: 1},
		{ID: "r-slow", Name: "r-slow", Kind: syncengine.LocationLocal, Role: syncengine.RoleResults, Priority: 2},
		{ID: "a-slow", Name: "a-slow", Kind: syncengine.LocationLocal, Role: syncengine.RoleAudio, Priority: 2},
		{ID: "r-fast", Name: "r-fast", Kind: syncengine.LocationLocal, Role: syncengine.RoleResults, Priority: 1},
		{ID: "a-fast", Name: "a-fast", Kind: syncengine.LocationLocal, Role: syncengine.RoleAudio, Priority: 1},
	}

	g := newRoleToggleGroup(locs, nil)
	want := []string{"a-fast", "a-slow", "a-cloud", "r-fast", "r-slow"}
	if got := strings.Join(g.options, ","); got != strings.Join(want, ",") {
		t.Fatalf("options = %q, want %q: Audio row first, each role ranked locals-then-remotes by Priority", got, strings.Join(want, ","))
	}

	g.SetSelected([]string{"r-slow", "a-slow"})
	if got, want := strings.Join(g.Selected(), ","), "a-slow,r-slow"; got != want {
		t.Errorf("Selected() = %q, want %q: ranked order, whatever order the caller asked in", got, want)
	}
}
