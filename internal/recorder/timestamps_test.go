package recorder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeParser struct {
	times map[string]time.Time
}

func (f fakeParser) ParseTimestamp(destRelPath string) (time.Time, bool) {
	t, ok := f.times[destRelPath]
	return t, ok
}

func (f fakeParser) RenameForTimestamp(destRelPath string, t time.Time) string {
	return t.Format("060102_1504") + ".mp3"
}

func mustTime(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCheckRecorderTimestamp(t *testing.T) {
	// otherTimes: the other 4 recorders' start times this session (one of
	// them is itself bad - wrong year - to confirm the consensus still
	// reflects the good majority).
	const consensusYear, consensusMonth, consensusDay = 2026, time.July, 10
	otherStarts := []time.Time{
		mustTime("2026-07-10 13:49"),
		mustTime("2026-07-10 13:54"),
		mustTime("2026-07-10 13:58"),
		mustTime("2025-07-10 17:58"),
	}

	t.Run("not suspicious when this recorder agrees with the consensus", func(t *testing.T) {
		files := []SourceFile{{DestRelPath: "260710_1352.mp3"}, {DestRelPath: "260712_1536.mp3"}}
		times := map[string]time.Time{
			"260710_1352.mp3": mustTime("2026-07-10 13:52"),
			"260712_1536.mp3": mustTime("2026-07-12 15:36"),
		}
		check := CheckRecorderTimestamp(files, fakeParser{times}, consensusYear, consensusMonth, consensusDay, otherStarts, time.Hour)
		if check == nil {
			t.Fatal("expected a non-nil result even when nothing looks wrong")
		}
		if check.Suspicious {
			t.Fatalf("expected Suspicious=false, got %+v", check)
		}
		if !check.Suggested.Equal(check.Recorded) {
			t.Fatalf("expected Suggested == Recorded when not suspicious, got %+v", check)
		}
	})

	t.Run("wrong year: whole recorder shifted, not just first file", func(t *testing.T) {
		// Every one of this recorder's files carries the same wrong year -
		// there is no correct majority within its own files, so this must be
		// caught against the session consensus, not against itself.
		files := []SourceFile{
			{DestRelPath: "a"}, {DestRelPath: "b"}, {DestRelPath: "c"}, {DestRelPath: "d"},
		}
		times := map[string]time.Time{
			"a": mustTime("2025-07-10 17:58"),
			"b": mustTime("2025-07-12 19:40"),
			"c": mustTime("2025-07-13 13:47"),
			"d": mustTime("2025-07-15 15:29"),
		}
		check := CheckRecorderTimestamp(files, fakeParser{times}, consensusYear, consensusMonth, consensusDay, otherStarts, time.Hour)
		if check == nil || !check.Suspicious || check.Kind != IssueWrongYear {
			t.Fatalf("expected suspicious IssueWrongYear, got %+v", check)
		}
		if check.Suggested.Year() != 2026 {
			t.Fatalf("expected suggested year 2026, got %d", check.Suggested.Year())
		}
		// The year-only fix doesn't touch the hour - the residual +4h error
		// on this fixture is a known limitation, not a test bug.
		if check.Suggested.Hour() != 17 {
			t.Fatalf("expected the (still wrong) hour to pass through unchanged, got %d", check.Suggested.Hour())
		}
	})

	t.Run("AM/PM mismatch: whole recorder shifted 12h", func(t *testing.T) {
		files := []SourceFile{
			{DestRelPath: "a"}, {DestRelPath: "b"}, {DestRelPath: "c"}, {DestRelPath: "d"},
		}
		times := map[string]time.Time{
			"a": mustTime("2026-07-10 01:49"),
			"b": mustTime("2026-07-12 03:31"),
			"c": mustTime("2026-07-13 21:38"),
			"d": mustTime("2026-07-15 23:20"),
		}
		check := CheckRecorderTimestamp(files, fakeParser{times}, consensusYear, consensusMonth, consensusDay, otherStarts, time.Hour)
		if check == nil || !check.Suspicious || check.Kind != IssueAMPM {
			t.Fatalf("expected suspicious IssueAMPM, got %+v", check)
		}
		if check.Suggested.Hour() != 13 {
			t.Fatalf("expected suggested hour 13, got %d", check.Suggested.Hour())
		}
	})

	t.Run("other when nothing fits, but Suggested still defaults to Recorded", func(t *testing.T) {
		files := []SourceFile{{DestRelPath: "a"}}
		times := map[string]time.Time{"a": mustTime("2026-07-10 04:15")}
		check := CheckRecorderTimestamp(files, fakeParser{times}, consensusYear, consensusMonth, consensusDay, otherStarts, time.Hour)
		if check == nil || !check.Suspicious || check.Kind != IssueOther {
			t.Fatalf("expected suspicious IssueOther, got %+v", check)
		}
		if !check.Suggested.Equal(check.Recorded) {
			t.Fatalf("expected Suggested == Recorded for IssueOther (no confident guess), got %+v", check)
		}
	})

	t.Run("AM/PM caught against the mode even when another recorder shares the error", func(t *testing.T) {
		// Two recorders both set to the wrong half of the day sit ~12 h from
		// the midday majority but right next to each other (23:46 and 23:59).
		// A nearest-neighbor check would let them validate each other;
		// measured against the mode start (~11:5x, the three midday
		// recorders), each is plainly ~12 h off. The one under test (23:59)
		// therefore flips to 11:59. This is the diel-deployment case a
		// nearest-neighbor check silently passed.
		mode := []time.Time{
			mustTime("2026-07-10 11:49"),
			mustTime("2026-07-10 11:54"),
			mustTime("2026-07-10 11:58"),
		}
		files := []SourceFile{{DestRelPath: "a"}}
		times := map[string]time.Time{"a": mustTime("2026-07-10 23:59")}
		others := append([]time.Time{mustTime("2026-07-10 23:46")}, mode...)
		check := CheckRecorderTimestamp(files, fakeParser{times}, consensusYear, consensusMonth, consensusDay, others, time.Hour)
		if check == nil || !check.Suspicious || check.Kind != IssueAMPM {
			t.Fatalf("expected suspicious IssueAMPM despite the co-errored neighbor, got %+v", check)
		}
		if check.Suggested.Hour() != 11 || check.Suggested.Minute() != 59 {
			t.Fatalf("expected suggested 11:59 (flipped), got %02d:%02d", check.Suggested.Hour(), check.Suggested.Minute())
		}
	})

	t.Run("AM/PM that rolled the date across midnight is corrected on both axes", func(t *testing.T) {
		// A recorder whose real start is just before noon but is set to the
		// wrong half of the day records its first file just after midnight the
		// NEXT day (00:01 on the 11th vs the mode's ~11:5x on the 10th). The
		// date "looks wrong" (day, and here month too if it straddled a month
		// end), but it's one fault: a 12 h flip. The fix must snap the time to
		// its flip AND the date back to the consensus, not just shuffle a date
		// field. This is the grouped date+AM/PM case.
		mode := []time.Time{
			mustTime("2026-07-10 11:49"),
			mustTime("2026-07-10 11:54"),
			mustTime("2026-07-10 11:58"),
		}
		files := []SourceFile{{DestRelPath: "a"}}
		times := map[string]time.Time{"a": mustTime("2026-07-11 00:01")}
		check := CheckRecorderTimestamp(files, fakeParser{times}, consensusYear, consensusMonth, consensusDay, mode, time.Hour)
		if check == nil || !check.Suspicious || check.Kind != IssueAMPM {
			t.Fatalf("expected suspicious IssueAMPM for the midnight-rollover case, got %+v", check)
		}
		want := mustTime("2026-07-10 12:01")
		if !check.Suggested.Equal(want) {
			t.Fatalf("expected suggestion snapped to %s (consensus date + flipped time), got %s", want, check.Suggested)
		}
	})

	t.Run("not suspicious with no other recorders to compare time-of-day against", func(t *testing.T) {
		files := []SourceFile{{DestRelPath: "a"}}
		times := map[string]time.Time{"a": mustTime("2026-07-10 04:15")}
		check := CheckRecorderTimestamp(files, fakeParser{times}, consensusYear, consensusMonth, consensusDay, nil, time.Hour)
		if check == nil || check.Suspicious {
			t.Fatalf("expected non-nil, non-suspicious result, got %+v", check)
		}
	})

	t.Run("nil only when there's no parseable timestamp at all", func(t *testing.T) {
		files := []SourceFile{{DestRelPath: "not_a_recording.txt"}}
		if check := CheckRecorderTimestamp(files, fakeParser{nil}, consensusYear, consensusMonth, consensusDay, otherStarts, time.Hour); check != nil {
			t.Fatalf("expected nil, got %+v", check)
		}
	})

	t.Run("ignores list order - earliest is found by parsed time, not position", func(t *testing.T) {
		// A lexical directory listing places every plain "YYMMDD_..." name
		// before any "tmp_"-prefixed one, so the chronologically-first (and
		// here, bad) file arrives last in the list.
		files := []SourceFile{
			{DestRelPath: "260710_1402.mp3"},
			{DestRelPath: "260712_0938.mp3"},
			{DestRelPath: "260713_1120.mp3"},
			{DestRelPath: "tmp_260710_0149.mp3"},
		}
		times := map[string]time.Time{
			"260710_1402.mp3":     mustTime("2026-07-10 14:02"),
			"260712_0938.mp3":     mustTime("2026-07-12 09:38"),
			"260713_1120.mp3":     mustTime("2026-07-13 11:20"),
			"tmp_260710_0149.mp3": mustTime("2026-07-10 01:49"),
		}
		check := CheckRecorderTimestamp(files, fakeParser{times}, consensusYear, consensusMonth, consensusDay, otherStarts, time.Hour)
		if check == nil || !check.Suspicious || check.Kind != IssueAMPM {
			t.Fatalf("expected suspicious IssueAMPM on the tmp_ file despite its list position, got %+v", check)
		}
		if check.DestRelPath != "tmp_260710_0149.mp3" {
			t.Fatalf("expected issue on tmp_260710_0149.mp3, got %s", check.DestRelPath)
		}
	})
}

func TestConsensusDate(t *testing.T) {
	starts := []time.Time{
		mustTime("2026-07-10 13:49"),
		mustTime("2026-07-10 13:54"),
		mustTime("2026-07-10 13:58"),
		mustTime("2025-07-10 17:58"), // one bad recorder shouldn't outvote the majority
	}
	y, m, d := ConsensusDate(starts)
	if y != 2026 || m != time.July || d != 10 {
		t.Fatalf("expected 2026-07-10, got %d-%s-%d", y, m, d)
	}
}

// TestApplyTimestampFixShiftsAllFiles guards the fix's whole reason for
// existing: a recorder's clock is wrong (or right) for its entire session,
// not just its first recording, so correcting one file's timestamp must
// rename every file from that recorder by the same correction, not leave
// the rest with their original (equally wrong) names.
func TestApplyTimestampFixShiftsAllFiles(t *testing.T) {
	dir := t.TempDir()
	files := []SourceFile{{DestRelPath: "a"}, {DestRelPath: "b"}, {DestRelPath: "c"}}
	times := map[string]time.Time{
		"a": mustTime("2025-02-21 13:41"),
		"b": mustTime("2025-02-21 15:12"),
		"c": mustTime("2025-02-21 16:20"),
	}
	for name := range times {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	delta := mustTime("2026-02-21 13:41").Sub(times["a"])
	correct := func(t time.Time) time.Time { return t.Add(delta) }

	if err := ApplyTimestampFix([]string{dir}, fakeParser{times}, files, correct); err != nil {
		t.Fatal(err)
	}

	for name, orig := range times {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("expected %s to be renamed away, but it still exists", name)
		}
		want := correct(orig).Format("060102_1504") + ".mp3"
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected corrected file %s to exist for original %s: %v", want, name, err)
		}
	}
}
