package ui

import (
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

func audioLoc(id string) syncengine.Location {
	return syncengine.Location{ID: id, Name: id, Role: syncengine.RoleAudio}
}

func resultsLoc(id string) syncengine.Location {
	return syncengine.Location{ID: id, Name: id, Role: syncengine.RoleResults}
}

// unitLabels flattens units for comparison, as "<label>:<loc ids joined>".
func unitLabels(units []nwayUnit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		ids := ""
		for j, l := range u.locs {
			if j > 0 {
				ids += "+"
			}
			ids += l.ID
		}
		out[i] = u.label + ":" + ids
	}
	return out
}

func TestBuildNWayUnits_BothRolesRunSideBySide(t *testing.T) {
	locs := []syncengine.Location{audioLoc("a1"), resultsLoc("r1"), audioLoc("a2"), resultsLoc("r2")}
	units := buildNWayUnits(locs, []string{"exp-1"})

	got := unitLabels(units)
	want := []string{"exp-1 (Audio):a1+a2", "exp-1 (Results):r1+r2"}
	if len(got) != len(want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("unit %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, u := range units {
		if u.expName != "exp-1" {
			t.Errorf("unit %q has expName %q, want the bare experiment name: a role suffix is display only and must never reach a path", u.label, u.expName)
		}
	}
}

func TestBuildNWayUnits_SingleRoleKeepsBareLabel(t *testing.T) {
	locs := []syncengine.Location{audioLoc("a1"), audioLoc("a2")}
	units := buildNWayUnits(locs, []string{"exp-1", "exp-2"})

	got := unitLabels(units)
	want := []string{"exp-1:a1+a2", "exp-2:a1+a2"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("units = %v, want %v", got, want)
		}
	}
}

func TestBuildNWayUnits_LoneRoleIsDropped(t *testing.T) {
	// One results location has nothing to converge with, so only the audio
	// pair runs - and, being the only role running, keeps its bare label.
	locs := []syncengine.Location{audioLoc("a1"), audioLoc("a2"), resultsLoc("r1")}
	units := buildNWayUnits(locs, []string{"exp-1"})

	if got, want := unitLabels(units), []string{"exp-1:a1+a2"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("units = %v, want %v", got, want)
	}
}

func TestBuildNWayUnits_NothingToConverge(t *testing.T) {
	locs := []syncengine.Location{audioLoc("a1"), resultsLoc("r1")}
	if units := buildNWayUnits(locs, []string{"exp-1"}); len(units) != 0 {
		t.Fatalf("units = %v, want none: neither role has a second location", unitLabels(units))
	}
}

func TestBuildNWayUnits_RanksEachRoleGroupRegardlessOfClickOrder(t *testing.T) {
	// Slice order is what BuildNWayTransferPlan walks to choose a copy
	// source, so each role group must come out ranked - locals by Priority,
	// then remotes - however the caller assembled the list it passed in.
	remoteResults := syncengine.Location{ID: "r-cloud", Name: "r-cloud", Kind: syncengine.LocationRemote, Role: syncengine.RoleResults, Priority: 1}
	slowResults := syncengine.Location{ID: "r-slow", Name: "r-slow", Kind: syncengine.LocationLocal, Role: syncengine.RoleResults, Priority: 2}
	fastResults := syncengine.Location{ID: "r-fast", Name: "r-fast", Kind: syncengine.LocationLocal, Role: syncengine.RoleResults, Priority: 1}

	// Clicked worst-first: cloud, then the slow drive, then the fast one.
	units := buildNWayUnits([]syncengine.Location{remoteResults, slowResults, fastResults}, []string{"exp-1"})
	if len(units) != 1 {
		t.Fatalf("units = %v, want one Results unit", unitLabels(units))
	}
	if got, want := unitLabels(units)[0], "exp-1:r-fast+r-slow+r-cloud"; got != want {
		t.Errorf("unit = %q, want %q: the highest-ranked local Results location must lead", got, want)
	}
}

func TestNWayResolver_ExpNameForStripsRoleSuffix(t *testing.T) {
	units := buildNWayUnits([]syncengine.Location{audioLoc("a1"), audioLoc("a2"), resultsLoc("r1"), resultsLoc("r2")}, []string{"exp-1"})
	r := newNWayResolver(units)
	if got := r.expNameFor("exp-1 (Results)"); got != "exp-1" {
		t.Errorf("expNameFor(%q) = %q, want %q", "exp-1 (Results)", got, "exp-1")
	}
}
