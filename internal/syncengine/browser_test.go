package syncengine

import "testing"

func audioLoc(id string) Location {
	return Location{ID: id, Name: id, Kind: LocationLocal, Role: RoleAudio}
}

func resultsLoc(id string) Location {
	return Location{ID: id, Name: id, Kind: LocationLocal, Role: RoleResults}
}

// presenceOf renders one entry's per-Location membership as a compact
// string ("10" = present at the first Location only), for readable
// assertions.
func presenceOf(pres presence, name string) string {
	out := ""
	for _, ok := range pres[name] {
		if ok {
			out += "1"
			continue
		}
		out += "0"
	}
	return out
}

func TestUnionEntries_FoldsResultOntoItsRecording(t *testing.T) {
	locs := []Location{audioLoc("a1"), resultsLoc("r1")}
	perLoc := [][]Entry{
		{{Name: "nun.mp3", Size: 400}},
		{{Name: "nun_buzzdetect.csv", Size: 12}},
	}

	entries, pres := unionEntries(locs, perLoc)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one row: the result belongs to the recording, not beside it", entries)
	}
	if entries[0].Name != "nun.mp3" {
		t.Errorf("row name = %q, want %q", entries[0].Name, "nun.mp3")
	}
	if entries[0].Size != 400 {
		t.Errorf("row size = %d, want the recording's 400", entries[0].Size)
	}
	if got := presenceOf(pres, "nun.mp3"); got != "11" {
		t.Errorf("presence = %q, want %q: both Locations hold their copy of this recording", got, "11")
	}
}

func TestUnionEntries_ResultWithoutItsRecordingKeepsItsOwnRow(t *testing.T) {
	// A Results-only selection has no audio name to fold onto, so the result
	// stays browsable under its real name rather than vanishing.
	locs := []Location{resultsLoc("r1")}
	perLoc := [][]Entry{{{Name: "nun_buzzdetect.csv", Size: 12}}}

	entries, pres := unionEntries(locs, perLoc)
	if len(entries) != 1 || entries[0].Name != "nun_buzzdetect.csv" {
		t.Fatalf("entries = %+v, want the result under its own name", entries)
	}
	if got := presenceOf(pres, "nun_buzzdetect.csv"); got != "1" {
		t.Errorf("presence = %q, want %q", got, "1")
	}
}

func TestUnionEntries_MissingCopiesAndDirectories(t *testing.T) {
	locs := []Location{audioLoc("a1"), audioLoc("a2"), resultsLoc("r1")}
	perLoc := [][]Entry{
		{{Name: "rec", IsDir: true}, {Name: "one.mp3", Size: 1}, {Name: "two.mp3", Size: 2}},
		{{Name: "rec", IsDir: true}, {Name: "one.mp3", Size: 1}},
		{{Name: "rec", IsDir: true}, {Name: "two_buzzdetect.csv", Size: 9}},
	}

	entries, pres := unionEntries(locs, perLoc)
	if len(entries) != 3 {
		t.Fatalf("entries = %+v, want 3 rows (rec/, one.mp3, two.mp3)", entries)
	}
	if !entries[0].IsDir || entries[0].Name != "rec" {
		t.Errorf("first row = %+v, want the directory sorted first", entries[0])
	}
	if got := presenceOf(pres, "rec"); got != "111" {
		t.Errorf("directory presence = %q, want %q: a Results tree mirrors directories one-to-one", got, "111")
	}
	if got := presenceOf(pres, "one.mp3"); got != "110" {
		t.Errorf("one.mp3 presence = %q, want %q: not yet analyzed, so the Results Location has no copy", got, "110")
	}
	if got := presenceOf(pres, "two.mp3"); got != "101" {
		t.Errorf("two.mp3 presence = %q, want %q: only the first Audio Location and the Results Location hold it", got, "101")
	}
}

func TestUnionEntries_ResultsLocationListedFirstStillFolds(t *testing.T) {
	// Location order (and, in the streaming lister, arrival order) must not
	// change the merge - the fold is a second pass over every Results
	// Location, not a first-come merge.
	locs := []Location{resultsLoc("r1"), audioLoc("a1")}
	perLoc := [][]Entry{
		{{Name: "nun_buzzdetect.csv", Size: 12}},
		{{Name: "nun.mp3", Size: 400}},
	}

	entries, pres := unionEntries(locs, perLoc)
	if len(entries) != 1 || entries[0].Name != "nun.mp3" {
		t.Fatalf("entries = %+v, want the single folded recording row", entries)
	}
	if got := presenceOf(pres, "nun.mp3"); got != "11" {
		t.Errorf("presence = %q, want %q", got, "11")
	}
}
