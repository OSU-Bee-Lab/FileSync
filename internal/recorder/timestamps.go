package recorder

import (
	"os"
	"path/filepath"
	"time"
)

// TimestampParser is implemented by drivers whose destination filenames
// encode a recording timestamp that can be checked for operator error (wrong
// AM/PM, wrong year/month/day at recorder setup time). Currently only Sony
// ICD-PX370's YYMMDD_HHMM(.mp3) naming qualifies — Olympus VN-541PC's
// destination names are already derived from filesystem metadata (see
// BestCreationTime), not the recorder's own clock, so a bad Olympus
// filename would just reflect a bad host-machine clock, not a bad manual
// entry on the device.
type TimestampParser interface {
	// ParseTimestamp extracts the recording time encoded in destRelPath, or
	// reports ok=false if destRelPath doesn't match this driver's pattern.
	ParseTimestamp(destRelPath string) (t time.Time, ok bool)

	// RenameForTimestamp returns the destRelPath that corresponds to t,
	// preserving destRelPath's directory and any other driver-specific
	// naming quirks (e.g. Sony's occasional "tmp_" prefix) - only the
	// timestamp portion of the name changes.
	RenameForTimestamp(destRelPath string, t time.Time) string
}

// TimestampIssueKind labels why a recorder's timestamp was (or wasn't)
// flagged, for display only - the actual correction applied is always a
// uniform offset derived from whatever timestamp the user confirms (see
// ApplyTimestampFix), not computed per-Kind.
type TimestampIssueKind int

const (
	// IssueNone: nothing suspicious - this recorder's earliest file agrees
	// with the session's consensus date and time-of-day.
	IssueNone TimestampIssueKind = iota
	// IssueWrongYear/Month/Day: exactly one date field differs from the
	// consensus date established by every recorder synced this session.
	IssueWrongYear
	IssueWrongMonth
	IssueWrongDay
	// IssueAMPM: the date agrees with the consensus, but this recorder's
	// start time-of-day is ~12 hours off from the closest other recorder's -
	// consistent with the recorder's clock having been set in the wrong
	// half of the day.
	IssueAMPM
	// IssueOther: a mismatch was detected but doesn't fit any of the above
	// common-fault patterns cleanly enough to auto-suggest a fix; the user
	// must supply the correct timestamp themselves.
	IssueOther
)

// TimestampIssue is the result of checking one recorder's clock against the
// session's consensus, represented by its earliest file. A recorder's clock
// is wrong (or right) for its whole deployment, not just its first
// recording, so the same correction is meant to be applied to every file
// from this recorder, not just DestRelPath - see ApplyTimestampFix.
type TimestampIssue struct {
	// DestRelPath and Recorded identify and describe this recorder's
	// earliest file - the one used to represent the whole recorder.
	DestRelPath string
	Recorded    time.Time
	// Suspicious is false when this recorder's earliest file agrees with
	// the session consensus within tolerance - Kind is IssueNone and
	// Suggested equals Recorded in that case.
	Suspicious bool
	Kind       TimestampIssueKind
	// Suggested is the best-guess correct timestamp for DestRelPath: a
	// confident single-field replacement for IssueWrongYear/Month/Day or
	// IssueAMPM, or just Recorded unchanged for IssueNone/IssueOther (no
	// confident guess - left for the user to type their own).
	Suggested time.Time
	// ConsensusYear/Month/Day is the session-wide agreed recording date this
	// recorder was checked against (see ConsensusDate), carried through so a
	// caller can spell out the actual mismatch ("first file is Jul 1, others
	// agree on Jun 30") instead of a vague "doesn't match".
	ConsensusYear  int
	ConsensusMonth time.Month
	ConsensusDay   int
	// MinutesFromMedian is |this recorder's earliest-file time-of-day − the
	// median start time-of-day of the other recorders|, in minutes, for the
	// verdicts that hinge on it (a passing time-of-day check, IssueAMPM, and
	// the time-of-day flavor of IssueOther). It is -1 when no such comparison
	// was made: a lone recorder (no otherTimes) has nothing to compare
	// against.
	MinutesFromMedian int
}

// ConsensusDate returns the most common (year, month, day) among starts -
// the recording day every recorder synced this session is assumed to agree
// on, since they were deployed/collected together. Ties break by whichever
// date occurs first in starts.
func ConsensusDate(starts []time.Time) (year int, month time.Month, day int) {
	type ymd struct {
		y int
		m time.Month
		d int
	}
	counts := make(map[ymd]int)
	var order []ymd
	for _, t := range starts {
		k := ymd{t.Year(), t.Month(), t.Day()}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	best := order[0]
	for _, k := range order[1:] {
		if counts[k] > counts[best] {
			best = k
		}
	}
	return best.y, best.m, best.d
}

// minutesSinceMidnight reduces t to its time-of-day, discarding its date -
// used to compare two timestamps' time-of-day across different calendar
// days without the date difference swamping the comparison.
func minutesSinceMidnight(t time.Time) int {
	return t.Hour()*60 + t.Minute()
}

// CheckRecorderTimestamp checks one recorder's earliest file (by parsed
// time, not list position - a lexical directory listing places every plain
// "YYMMDD_..." name before any "tmp_"-prefixed one, so the chronologically-
// first file can otherwise arrive last) against the session's consensus:
// consensusYear/Month/Day (see ConsensusDate, computed across every
// recorder including this one) for its date, and the median of otherTimes -
// every OTHER recorder's own earliest file this session - for its
// time-of-day.
//
// This cross-recorder comparison is required, not just an alternative: a
// recorder's clock is wrong (or right) for its entire deployment, so every
// one of its own files carries the identical error - there is no correct
// majority within a single bad recorder's own files to diverge from.
//
// Unlike a plain "is this suspicious" check, this always returns a non-nil
// result as long as files has at least one parseable timestamp - including
// when nothing looks wrong (Suspicious false, Suggested equal to Recorded) -
// so a review screen can show every recorder uniformly and let the user
// force a manual adjustment even on one that checks out. It returns nil
// only when files has no parseable timestamp at all.
func CheckRecorderTimestamp(files []SourceFile, parser TimestampParser, consensusYear int, consensusMonth time.Month, consensusDay int, otherTimes []time.Time, tolerance time.Duration) *TimestampIssue {
	if parser == nil {
		return nil
	}

	var earliestRel string
	var earliest time.Time
	found := false
	for _, f := range files {
		if t, ok := parser.ParseTimestamp(f.DestRelPath); ok {
			if !found || t.Before(earliest) {
				earliest = t
				earliestRel = f.DestRelPath
				found = true
			}
		}
	}
	if !found {
		return nil
	}

	// issue seeds every return with the identifying fields and the consensus
	// date the caller needs to describe a mismatch; MinutesFromMedian starts
	// at -1 and is only set once the time-of-day comparison actually runs.
	issue := func(kind TimestampIssueKind, suspicious bool, suggested time.Time) *TimestampIssue {
		return &TimestampIssue{
			DestRelPath:       earliestRel,
			Recorded:          earliest,
			Suspicious:        suspicious,
			Kind:              kind,
			Suggested:         suggested,
			ConsensusYear:     consensusYear,
			ConsensusMonth:    consensusMonth,
			ConsensusDay:      consensusDay,
			MinutesFromMedian: -1,
		}
	}

	tolMin := int(tolerance / time.Minute)
	own := minutesSinceMidnight(earliest)
	dateMatches := earliest.Year() == consensusYear && earliest.Month() == consensusMonth && earliest.Day() == consensusDay

	// todDist is the circular distance between two times-of-day (minutes),
	// so 23:59 and 00:01 read as 2 min apart, not 1438, and a ±12 h shift
	// always reads as the maximal 720.
	todDist := func(a, b int) int {
		d := a - b
		if d < 0 {
			d = -d
		}
		if d > 720 {
			d = 1440 - d
		}
		return d
	}

	// median is the circular medoid of the OTHER recorders' start
	// times-of-day - the one whose total circular distance to the rest is
	// smallest - or -1 when this is the only recorder and there is nothing to
	// compare against. It's the robust center of the majority the deployment
	// agrees on: a median (not the nearest neighbor, nor a mean, nor the mode
	// of exact values) stays put when starts are spread over a few minutes,
	// and can't be pulled by the ~12 h AM/PM or date-error outliers, which
	// sit at the far end. Because it goes through todDist it's also correct
	// across the midnight wrap, where a plain minutes-since-midnight median
	// would land on the opposite side of the clock. Smallest total wins ties
	// (deterministic).
	median := -1
	if len(otherTimes) > 0 {
		tods := make([]int, len(otherTimes))
		for i, ot := range otherTimes {
			tods[i] = minutesSinceMidnight(ot)
		}
		bestSum := -1
		for _, cand := range tods {
			sum := 0
			for _, t := range tods {
				sum += todDist(cand, t)
			}
			if bestSum == -1 || sum < bestSum {
				bestSum = sum
				median = cand
			}
		}
	}

	// finish stamps MinutesFromMedian (distance to the median start-of-day,
	// or -1 when there was no median) onto every returned issue.
	finish := func(kind TimestampIssueKind, suspicious bool, suggested time.Time) *TimestampIssue {
		r := issue(kind, suspicious, suggested)
		if median >= 0 {
			r.MinutesFromMedian = todDist(own, median)
		}
		return r
	}

	// AM/PM is checked first, against the median, and before the date-field
	// checks - because the two symptoms are one fault. A recorder set to the
	// wrong half of the day reads ~12 h off; when the real start is late
	// evening, that misread also rolls the calendar date across midnight. So
	// a recorder whose date "looks wrong" is often just 12 h off, and the
	// single correction is to snap both back: the date to the deployment's
	// consensus date, the time to its 12 h flip. Diagnosing this here means
	// such a recorder is called an AM/PM mix-up (with a correct suggestion),
	// not a mystery date slip left for the user to puzzle out.
	if median >= 0 && todDist(own, median) > tolMin {
		flippedTOD := (own + 720) % 1440
		if todDist(flippedTOD, median) <= tolMin {
			flipped := time.Date(consensusYear, consensusMonth, consensusDay, flippedTOD/60, flippedTOD%60, earliest.Second(), 0, earliest.Location())
			return finish(IssueAMPM, true, flipped)
		}
	}

	// Not an AM/PM flip. A wrong date now means a genuine date-field error
	// (wrong year/month/day at setup), corrected by snapping to the consensus
	// date while keeping the (already-fine) time-of-day.
	if !dateMatches {
		diffFields := 0
		if earliest.Year() != consensusYear {
			diffFields++
		}
		if earliest.Month() != consensusMonth {
			diffFields++
		}
		if earliest.Day() != consensusDay {
			diffFields++
		}
		corrected := time.Date(consensusYear, consensusMonth, consensusDay, earliest.Hour(), earliest.Minute(), earliest.Second(), 0, earliest.Location())
		switch {
		case diffFields == 1 && earliest.Year() != consensusYear:
			return finish(IssueWrongYear, true, corrected)
		case diffFields == 1 && earliest.Month() != consensusMonth:
			return finish(IssueWrongMonth, true, corrected)
		case diffFields == 1 && earliest.Day() != consensusDay:
			return finish(IssueWrongDay, true, corrected)
		default:
			return finish(IssueOther, true, earliest)
		}
	}

	// Date agrees. If the time-of-day still sits outside tolerance of the
	// median (and wasn't a clean 12 h flip), it's an offset we can't name -
	// leave it for the user. Otherwise everything lines up.
	if median >= 0 && todDist(own, median) > tolMin {
		return finish(IssueOther, true, earliest)
	}
	return finish(IssueNone, false, earliest)
}

// ApplyTimestampFix applies correct to every file in files, not just the
// file the issue was detected on: a recorder's clock is wrong (or right)
// for its whole deployment, so once one file's timestamp is confirmed bad,
// every other file from the same recorder is assumed off by the exact same
// amount and gets renamed identically - parsed, corrected, reformatted (see
// TimestampParser.RenameForTimestamp) - at every destDir. A file that
// doesn't parse (already non-conforming, or missing at a given destDir) is
// skipped rather than treated as an error, since not every destRoot need
// hold every recorder and not every entry in files need match this parser's
// pattern.
//
// It does not touch the recorder itself: this only ever runs once every
// file for a recorder has already landed locally, at which point the
// device may already be disconnected or wiped (see CLAUDE.md's never-delete
// scoping - this never deletes anything, only renames local files this app
// already copied).
func ApplyTimestampFix(destDirs []string, parser TimestampParser, files []SourceFile, correct func(time.Time) time.Time) error {
	for _, f := range files {
		t, ok := parser.ParseTimestamp(f.DestRelPath)
		if !ok {
			continue
		}
		newRel := parser.RenameForTimestamp(f.DestRelPath, correct(t))
		if newRel == f.DestRelPath {
			continue
		}
		for _, dir := range destDirs {
			oldPath := filepath.Join(dir, f.DestRelPath)
			if _, err := os.Stat(oldPath); err != nil {
				continue
			}
			newPath := filepath.Join(dir, newRel)
			if err := os.Rename(oldPath, newPath); err != nil {
				return err
			}
		}
	}
	return nil
}
