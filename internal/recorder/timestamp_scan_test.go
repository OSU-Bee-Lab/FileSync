package recorder

import (
	"testing"
	"time"
)

// fakeDriver is a minimal Driver+TimestampParser stand-in for
// GroupTimestampFiles tests, which only care about the TimestampParser
// methods it drives (ParseTimestamp, RecorderDirDepth) - the rest of Driver
// is never called by GroupTimestampFiles, so it's stubbed out.
type fakeDriver struct {
	name  string
	depth int
}

func (f fakeDriver) Name() string                        { return f.name }
func (f fakeDriver) QuickReject(v Volume) bool           { return false }
func (f fakeDriver) Detect(v Volume) bool                { return false }
func (f fakeDriver) RecorderID(v Volume) (string, error) { return "", nil }
func (f fakeDriver) SourceFiles(v Volume) ([]SourceFile, error) {
	return nil, nil
}

// ParseTimestamp recognizes flat, driver-tagged names like
// "sony-260101_1200.mp3" so fakeDriver instances don't collide with each
// other's patterns.
func (f fakeDriver) ParseTimestamp(destRelPath string) (time.Time, bool) {
	prefix := f.name + "-"
	if len(destRelPath) <= len(prefix) || destRelPath[:len(prefix)] != prefix {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("060102_1504", destRelPath[len(prefix):len(prefix)+11], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (f fakeDriver) RenameForTimestamp(destRelPath string, t time.Time) string {
	return f.name + "-" + t.Format("060102_1504")
}

func (f fakeDriver) RecorderDirDepth() int { return f.depth }

func withFakeDrivers(t *testing.T, fakes ...Driver) {
	t.Helper()
	orig := Drivers
	Drivers = append([]Driver{}, fakes...)
	t.Cleanup(func() { Drivers = orig })
}

// TestGroupTimestampFilesFlatRecorder covers a Sony-style driver
// (RecorderDirDepth 0): the recorder directory holds its files directly, so
// grouping by immediate containing directory is correct.
func TestGroupTimestampFilesFlatRecorder(t *testing.T) {
	withFakeDrivers(t, fakeDriver{name: "sony", depth: 0})

	groups := GroupTimestampFiles([]string{
		"exp/01_02/sony-260101_1200.mp3",
		"exp/01_02/sony-260101_1210.mp3",
	})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0]
	if g.RecorderID != "01_02" {
		t.Errorf("RecorderID = %q, want %q", g.RecorderID, "01_02")
	}
	if g.RelDir != "exp/01_02" {
		t.Errorf("RelDir = %q, want %q", g.RelDir, "exp/01_02")
	}
	if len(g.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(g.Files))
	}
}

// TestGroupTimestampFilesNestedRecorder is the regression case this test
// guards: an Olympus-style driver (RecorderDirDepth 1) whose files sit one
// level below the recorder directory in a device category subdirectory
// (MUSIC, TALK, ...). The recorder ID must resolve to the actual recorder
// directory, not the category subdirectory, and files from different
// category subdirectories under the same recorder must merge into one
// group rather than becoming separate cards misnamed after their category.
func TestGroupTimestampFilesNestedRecorder(t *testing.T) {
	withFakeDrivers(t, fakeDriver{name: "olympus", depth: 1})

	groups := GroupTimestampFiles([]string{
		"exp/REC42/MUSIC/olympus-260101_1200",
		"exp/REC42/TALK/olympus-260101_1210",
	})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1 (MUSIC and TALK should merge into one recorder)", len(groups))
	}
	g := groups[0]
	if g.RecorderID != "REC42" {
		t.Errorf("RecorderID = %q, want %q (got the category subdirectory instead of the recorder ID)", g.RecorderID, "REC42")
	}
	if g.RelDir != "exp/REC42" {
		t.Errorf("RelDir = %q, want %q", g.RelDir, "exp/REC42")
	}
	wantDestRels := map[string]bool{
		"MUSIC/olympus-260101_1200": true,
		"TALK/olympus-260101_1210":  true,
	}
	if len(g.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(g.Files))
	}
	for _, f := range g.Files {
		if !wantDestRels[f.DestRelPath] {
			t.Errorf("unexpected DestRelPath %q", f.DestRelPath)
		}
	}
}

// TestGroupTimestampFilesNestedRecorderDistinctIDs makes sure two different
// physical recorders that both happen to use the same category name don't
// get merged into one group just because they share that name.
func TestGroupTimestampFilesNestedRecorderDistinctIDs(t *testing.T) {
	withFakeDrivers(t, fakeDriver{name: "olympus", depth: 1})

	groups := GroupTimestampFiles([]string{
		"exp/REC1/MUSIC/olympus-260101_1200",
		"exp/REC2/MUSIC/olympus-260101_1200",
	})
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 (REC1 and REC2 must stay distinct)", len(groups))
	}
}
