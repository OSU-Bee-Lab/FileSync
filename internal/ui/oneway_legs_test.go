package ui

import (
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

func TestKeepOneWayLegs(t *testing.T) {
	src := syncengine.Location{ID: "sp", Name: "SharePoint", Kind: syncengine.LocationRemote}
	d1 := syncengine.Location{ID: "d1", Name: "Drive 1", Kind: syncengine.LocationLocal}
	d2 := syncengine.Location{ID: "d2", Name: "Drive 2", Kind: syncengine.LocationLocal}
	other := syncengine.Location{ID: "x", Name: "Elsewhere", Kind: syncengine.LocationLocal}
	dstIDs := map[string]bool{"d1": true, "d2": true}

	pairs := []syncengine.NWayTransferPair{
		{Source: src, Dest: d1},                                    // 0: the download
		{Source: d1, Dest: d2, Via: src, DependsOn: []int{0}},      // 1: chained off it
		{Source: d1, Dest: d2},                                     // 2: plain dest→dest
		{Source: d2, Dest: src},                                    // 3: write-back
		{Source: src, Dest: other},                                 // 4: unselected dest
		{Source: d1, Dest: other, Via: src, DependsOn: []int{0}},   // 5: chained to unselected dest
		{Source: other, Dest: d2, Via: other, DependsOn: []int{4}}, // 6: chained, not source-rooted
		{Source: d1, Dest: d2, Via: src, DependsOn: []int{3}},      // 7: chained off a dropped leg
	}

	want := []bool{true, true, false, false, false, false, false, false}
	got := keepOneWayLegs(pairs, src.ID, dstIDs)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("leg %d (%s → %s via %q): kept = %v, want %v",
				i, pairs[i].Source.ID, pairs[i].Dest.ID, pairs[i].Via.ID, got[i], want[i])
		}
	}
}
