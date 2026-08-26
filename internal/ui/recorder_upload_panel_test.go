package ui

import "testing"

// Two upload destinations upload the same recorder+file independently, so
// the two entries must stay distinct: keyed on relPath alone, the first
// destination's UploadDone removed the other destination's entry too,
// leaving that upload invisible while it was still running.
func TestUploadEntriesAreKeyedByDestination(t *testing.T) {
	list := []uploadFileEntry{
		{recorderID: "REC1", destID: "sharepoint", relPath: "exp/REC1/a.mp3"},
		{recorderID: "REC1", destID: "gdrive", relPath: "exp/REC1/a.mp3"},
	}

	if got := findUploadEntry(list, "REC1", "gdrive", "exp/REC1/a.mp3"); got != 1 {
		t.Fatalf("findUploadEntry(gdrive) = %d, want 1", got)
	}
	if got := findUploadEntry(list, "REC1", "dropbox", "exp/REC1/a.mp3"); got != -1 {
		t.Fatalf("findUploadEntry(unknown dest) = %d, want -1", got)
	}

	list = removeUploadEntry(list, "REC1", "sharepoint", "exp/REC1/a.mp3")
	if len(list) != 1 {
		t.Fatalf("after removing one destination's entry: len = %d, want 1", len(list))
	}
	if list[0].destID != "gdrive" {
		t.Fatalf("wrong entry removed: %q survived, want gdrive", list[0].destID)
	}
}

func TestUploadEntryLabelNamesDestinationOnlyWhenAmbiguous(t *testing.T) {
	e := uploadFileEntry{relPath: "exp/REC1/a.mp3", destName: "Lab SharePoint"}
	if got := e.label(false); got != "exp/REC1/a.mp3" {
		t.Errorf("label(false) = %q, want the bare path", got)
	}
	if got := e.label(true); got != "exp/REC1/a.mp3 → Lab SharePoint" {
		t.Errorf("label(true) = %q, want the path qualified by destination", got)
	}
	// A destination-less entry (e.g. an older UploadUpdate) never grows a
	// dangling arrow.
	bare := uploadFileEntry{relPath: "exp/REC1/a.mp3"}
	if got := bare.label(true); got != "exp/REC1/a.mp3" {
		t.Errorf("label(true) with no destination name = %q, want the bare path", got)
	}
}
