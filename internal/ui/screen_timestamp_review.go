package ui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// timestampReviewRow is one recorder's line on the timestamp review screen.
// apply performs the correction (renaming sourceFiles per
// parser.RenameForTimestamp(f, correct(oldTime))) wherever this recorder's
// files actually live - Sync Recorders always renames local destDirs
// directly (recorder.ApplyTimestampFix), while Manage Files' Retime applies
// across whichever Locations (local or remote) the user selected, via
// rclone (syncengine.ApplyRenames) - so this row doesn't need to know or
// care which.
type timestampReviewRow struct {
	recorderID  string
	parser      recorder.TimestampParser
	sourceFiles []recorder.SourceFile
	check       recorder.TimestampIssue
	apply       func(correct func(time.Time) time.Time) error
	// recheck re-runs this recorder's timestamp check at a new tolerance,
	// leaving the (tolerance-independent) consensus date and this recorder's
	// files untouched. It lets the review screen's tolerance slider re-judge
	// every recorder live without either caller having to rebuild the screen.
	recheck func(tolerance time.Duration) recorder.TimestampIssue
	// recheckAt is recheck's counterpart for a not-yet-confirmed edited start
	// time: it judges recorded (typed into "New start time") against the
	// same other-recorder times and consensus date recheck uses, so the
	// review screen can show live whether an override actually resolves the
	// mismatch before the user commits it.
	recheckAt func(recorded time.Time, tolerance time.Duration) recorder.TimestampIssue

	// locs and relDir let the review screen stream this recorder's files for
	// in-place listening, from the highest-priority location that has them
	// (see audioOpener) - relDir is this recorder's own directory, relative
	// to a Location root and forward-slash separated, with each file's
	// DestRelPath (just a filename, see recorder.SourceFile) joined onto it
	// to get a full location-relative path. Both are nil/empty when a host
	// has nothing streamable to offer (locs is what actually gates a play
	// button - see audioRowControls.update).
	locs   []syncengine.Location
	relDir string
}

// timestampReviewInput is one recorder handed to buildTimestampReviewRows:
// its identity, its files, its earliest recorded start (used for the
// session-wide consensus), and the one thing that genuinely differs between
// the two retime entry points - how a confirmed correction is applied to its
// files (Sync Recorders renames local dirs directly; Manage Files renames
// across rclone Locations). Everything else about the check is shared.
type timestampReviewInput struct {
	recorderID  string
	parser      recorder.TimestampParser
	sourceFiles []recorder.SourceFile
	start       time.Time
	apply       func(correct func(time.Time) time.Time) error

	// locs and relDir carry through to the same-named timestampReviewRow
	// fields - see there.
	locs   []syncengine.Location
	relDir string
}

// buildTimestampReviewRows is the single retime pathway both entry points go
// through: it computes the session's consensus date across every recorder's
// earliest start (recorder.ConsensusDate), then for each recorder checks that
// start against the consensus and every OTHER recorder's start
// (recorder.CheckRecorderTimestamp), wiring a recheck closure so the review
// screen's tolerance slider can re-judge live. Recorders with no parseable
// timestamp are dropped, but every recorder that was checked gets a row
// regardless of verdict - including a clean one - so the review screen always
// has something to show and can render its own all-clear state (see
// refreshSummary) rather than the caller silently skipping the screen.
// Keeping this in one place is what stops the two callers from drifting - the
// reason the Manage Files path could suggest a different fix than Sync
// Recorders for the same clock error.
func buildTimestampReviewRows(inputs []timestampReviewInput, tolerance time.Duration) []timestampReviewRow {
	allStarts := make([]time.Time, len(inputs))
	for i, in := range inputs {
		allStarts[i] = in.start
	}
	cy, cm, cd := recorder.ConsensusDate(allStarts)

	var rows []timestampReviewRow
	for i, in := range inputs {
		others := make([]time.Time, 0, len(inputs)-1)
		for j, o := range inputs {
			if j != i {
				others = append(others, o.start)
			}
		}
		in := in
		recheck := func(tol time.Duration) recorder.TimestampIssue {
			return *recorder.CheckRecorderTimestamp(in.sourceFiles, in.parser, cy, cm, cd, others, tol)
		}
		recheckAt := func(recorded time.Time, tol time.Duration) recorder.TimestampIssue {
			return *recorder.EvaluateTimestamp(recorded, cy, cm, cd, others, tol)
		}
		rows = append(rows, timestampReviewRow{
			recorderID:  in.recorderID,
			parser:      in.parser,
			sourceFiles: in.sourceFiles,
			check:       recheck(tolerance),
			recheck:     recheck,
			recheckAt:   recheckAt,
			apply:       in.apply,
			locs:        in.locs,
			relDir:      in.relDir,
		})
	}
	return rows
}

// timestampIssueDetail describes a check in plain language, stating the
// concrete finding - which date field is off and what the others agree on, or
// how many minutes the first file's time-of-day sits from the nearest
// recorder against the current tolerance - rather than a vague "doesn't
// match". tolerance is the value the check was run at, echoed so the reader
// can see why a given gap did or didn't trip it (and re-judge as they slide
// it on the review screen).
func timestampIssueDetail(check recorder.TimestampIssue, tolerance time.Duration) string {
	loc := check.Recorded.Location()
	consensus := time.Date(check.ConsensusYear, check.ConsensusMonth, check.ConsensusDay, 0, 0, 0, 0, loc)
	recDate := check.Recorded.Format("Jan 2, 2006")
	conDate := consensus.Format("Jan 2, 2006")
	recTime := check.Recorded.Format("3:04 PM")
	tolMin := int(tolerance / time.Minute)

	switch check.Kind {
	case recorder.IssueWrongYear:
		return fmt.Sprintf("first file dated %s — the other recorders agree on %s (year is off)", recDate, conDate)
	case recorder.IssueWrongMonth:
		return fmt.Sprintf("first file dated %s — the other recorders agree on %s (month is off)", recDate, conDate)
	case recorder.IssueWrongDay:
		return fmt.Sprintf("first file dated %s — the other recorders agree on %s (day is off)", recDate, conDate)
	case recorder.IssueAMPM:
		dir := "earlier than"
		if check.RecordedLater {
			dir = "later than"
		}
		return fmt.Sprintf("first file at %s is about 12 h %s the other recorders' start — looks like an AM/PM mix-up", recTime, dir)
	case recorder.IssueDateAndTime:
		dir := "earlier than"
		if check.RecordedLater {
			dir = "later than"
		}
		return fmt.Sprintf("first file dated %s at %s — the other recorders agree on %s, and the time is also %d min %s their median start (tolerance %d min)", recDate, recTime, conDate, check.MinutesFromMedian, dir, tolMin)
	case recorder.IssueOther:
		sameDate := check.Recorded.Year() == check.ConsensusYear &&
			check.Recorded.Month() == check.ConsensusMonth &&
			check.Recorded.Day() == check.ConsensusDay
		if !sameDate {
			return fmt.Sprintf("first file dated %s — the other recorders agree on %s", recDate, conDate)
		}
		if check.MinutesFromMedian >= 0 {
			dir := "earlier than"
			if check.RecordedLater {
				dir = "later than"
			}
			return fmt.Sprintf("first file at %s is %d min %s the other recorders' median start time (tolerance %d min)", recTime, check.MinutesFromMedian, dir, tolMin)
		}
		return "doesn't line up with the other recorders — check manually"
	default:
		return "looks correct"
	}
}

// timestampCardColor mirrors the active-sync screen's colored-row language
// (see rowBackgroundColor): orange marks a recorder the detector flagged,
// gray is the neutral/"looks correct" state - deliberately not the same
// transparent "untinted" idle look Screen 2 uses for jobIdle, since here
// every recorder should read as reviewed, not merely pending.
func timestampCardColor(suspicious bool) color.Color {
	if suspicious {
		return colorRGBA(0xE0, 0x7B, 0x4A, 0xFF)
	}
	return colorRGBA(0xDD, 0xDD, 0xDD, 0xFF)
}

// timestampCardColorFor picks a card's background from its live effective
// check: blue only once an override (adjust) actually resolves the mismatch
// (Suspicious false) - an override still outside tolerance stays orange,
// same as an untouched flagged recorder, so blue always reads as "resolved"
// rather than merely "touched". Reuses the sync progress screen's light
// "done" blue (see rowBackgroundColor's jobDone) rather than a darker shade,
// which read poorly against the card's dark text.
func timestampCardColorFor(check recorder.TimestampIssue, adjust bool) color.Color {
	if adjust && !check.Suspicious {
		return colorRGBA(0xAE, 0xD3, 0xF2, 0xFF)
	}
	return timestampCardColor(check.Suspicious)
}

// ordinalDay formats a day-of-month with its English ordinal suffix
// (1st, 2nd, 3rd, 4th, ... 11th-13th, 21st, ...), for plain-language
// timestamp previews.
func ordinalDay(d int) string {
	if d >= 11 && d <= 13 {
		return fmt.Sprintf("%dth", d)
	}
	switch d % 10 {
	case 1:
		return fmt.Sprintf("%dst", d)
	case 2:
		return fmt.Sprintf("%dnd", d)
	case 3:
		return fmt.Sprintf("%drd", d)
	default:
		return fmt.Sprintf("%dth", d)
	}
}

// plainDateTime renders t as e.g. "June 3rd 2026 at 3:45 PM" for the review
// screen's live preview of the edited start time.
func plainDateTime(t time.Time) string {
	return fmt.Sprintf("%s %s %d at %s", t.Month().String(), ordinalDay(t.Day()), t.Year(), t.Format("3:04 PM"))
}

// signedUnit formats n with an explicit sign and a singular/plural unit
// name, e.g. signedUnit(-1, "month") -> "-1 month", signedUnit(12, "hour")
// -> "+12 hours".
func signedUnit(n int, unit string) string {
	sign := "+"
	if n < 0 {
		sign = "-"
		n = -n
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%s%d %s", sign, n, unit)
}

// formatAdjustment describes the edit from -> to as a compound, plain-
// language delta, e.g. "-1 month; +12 hours" - each calendar component
// (year, month, day, hour, minute) is diffed independently rather than
// normalized into a single duration, since a recorder's clock error is
// naturally described that way (e.g. "the month was wrong" rather than "off
// by ~30 days").
func formatAdjustment(from, to time.Time) string {
	var parts []string
	if d := to.Year() - from.Year(); d != 0 {
		parts = append(parts, signedUnit(d, "year"))
	}
	if d := int(to.Month()) - int(from.Month()); d != 0 {
		parts = append(parts, signedUnit(d, "month"))
	}
	if d := to.Day() - from.Day(); d != 0 {
		parts = append(parts, signedUnit(d, "day"))
	}
	if d := to.Hour() - from.Hour(); d != 0 {
		parts = append(parts, signedUnit(d, "hour"))
	}
	if d := to.Minute() - from.Minute(); d != 0 {
		parts = append(parts, signedUnit(d, "minute"))
	}
	if len(parts) == 0 {
		return "no change"
	}
	return strings.Join(parts, "; ")
}

// strikethrough overlays a combining strikethrough mark on every rune of s,
// used to show a file's old (about to be replaced) name in the review
// screen's rename preview. Fyne's canvas/widget text has no native
// strikethrough style, so this is done with Unicode's combining
// long-stroke-overlay character rather than a custom-drawn text renderer.
func strikethrough(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
		b.WriteRune('̶')
	}
	return b.String()
}

// timestampReviewEntry is the live, editable state for one recorder on the
// review screen - separate from timestampReviewRow (the immutable detection
// result) so switching which recorder is selected doesn't lose whatever the
// user already typed or toggled for the others.
type timestampReviewEntry struct {
	row    timestampReviewRow
	adjust bool
	text   string
}

// timestampReviewHost supplies the pieces of a caller's screen that
// showTimestampReview needs but doesn't own: where to draw (s, win), what
// the Continue/Exit actions actually do, and an optional per-recorder hook
// run right after ApplyTimestampFix (Sync Recorders uses this to re-upload a
// corrected file outside batch-upload mode; Manage Files' Retime leaves it
// nil since it has no uploads to redo).
type timestampReviewHost struct {
	s   *state
	win fyne.Window

	// parentPath is shown at the top of the screen, below the header, so the
	// researcher can see at a glance which session/path is under review: the
	// sync destination's relative path for Sync Recorders (single local
	// destination, so its Location name is included - "<Location name>:
	// <path>"), or the relative path the user browsed to for Manage Files'
	// Retime (no Location name - Retime applies across every selected
	// Location, so naming just one of them would misstate where this
	// actually reaches).
	parentPath string

	// continueLabel/exitLabel must name the same destination twice, differing
	// only in whether the corrections are applied ("Apply & End Sync" vs "End
	// Without Applying") - both buttons go to the same next screen, so a label
	// pair that hides that (the old "End Sync"/"Exit Sync") reads as two
	// different outcomes.
	// continueLabel names the continue button when at least one recorder is
	// checked for a correction ("Apply & End Sync", "Apply Corrections") -
	// it's misleading once nothing is checked, since nothing will actually
	// be applied, so continueBaseLabel names the same destination without
	// that promise ("End Sync", "Continue") for the button to fall back to
	// when every entry is unchecked. The button swaps between the two live
	// as adjust checkboxes are toggled.
	continueLabel     string
	continueBaseLabel string
	onContinue        func()

	// applyOnlyLabel/onApplyOnly optionally add a third button between exit
	// and continue: apply the corrections, same as continue, but hand off to
	// onApplyOnly instead of onContinue. Sync Recorders' batch-upload mode
	// uses this so applying a clock-drift fix doesn't force committing to
	// the upload right now; leave both zero to omit the button entirely
	// (Manage Files and non-batch Sync Recorders, where continue and
	// "apply only" would go to the same place anyway).
	applyOnlyLabel string
	onApplyOnly    func()

	exitLabel   string
	exitWarning string
	onExit      func()

	afterFix func(row timestampReviewRow, delta time.Duration)
}

// timestampReviewScreen holds the live state for the master-detail
// timestamp review step: a left-hand list of recorders (colored per
// timestampCardColor) and a right-hand detail pane for whichever one is
// selected, showing its files and a live rename preview.
type timestampReviewScreen struct {
	host     timestampReviewHost
	entries  []*timestampReviewEntry
	selected int

	// continueBtn's label toggles between host.continueLabel and
	// host.continueBaseLabel as adjust checkboxes are checked/unchecked -
	// see refreshContinueLabel.
	continueBtn *widget.Button
	// tolerance is the live match tolerance the tolerance slider drives; it
	// starts at the caller-supplied value and every recheck runs against it.
	tolerance time.Duration

	cards      []*canvas.Rectangle
	cardLabels []*widget.Label
	detailBox  *fyne.Container
	// summaryLbl states, at a glance, how many recorders look off - so an
	// all-clear check still lands on this screen with an explicit "everything
	// lines up" rather than a wall of gray rows the user has to interpret. It
	// refreshes live as the tolerance slider changes what's flagged.
	summaryLbl *widget.Label

	// fileAudio is the currently-selected recorder's per-file playback
	// controls, rebuilt by rebuildDetail every time the detail pane redraws.
	// The one refresh callback registered with registerAudioRefreshFunc (see
	// audio_controls.go) walks this slice on every player state change, so
	// it always repaints whichever recorder is actually on screen rather
	// than needing to be re-registered per selection.
	fileAudio []timestampFileAudio
	// audioStatusLbl surfaces a playback error for the currently open
	// recorder's files, mirroring destFolderBrowser's statusLbl.
	audioStatusLbl *widget.Label
}

// timestampFileAudio is one file row's playback controls plus what they need
// to keep updating themselves as the player's state changes: the transport
// widgets built by audioRow, the Location(s) to stream from, and the file's
// full path relative to a Location root.
type timestampFileAudio struct {
	controls *audioRowControls
	locs     []syncengine.Location
	relPath  string
	filename string
}

// showTimestampReview shows the full-screen review step between a caller
// (Sync Recorders' end-of-session check, or Manage Files' Retime scan) and
// whatever it does next - see timestampReviewHost. Continuing applies every
// checked recorder's correction - parsed from its entry, generalized to a
// uniform offset from that recorder's own recorded time, and applied to
// every file from it (see recorder.ApplyTimestampFix) - then calls
// host.onContinue.
func showTimestampReview(host timestampReviewHost, rows []timestampReviewRow, tolerance time.Duration) {
	sorted := make([]timestampReviewRow, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].check.Suspicious && !sorted[j].check.Suspicious
	})

	tr := &timestampReviewScreen{host: host, tolerance: tolerance}
	for _, r := range sorted {
		tr.entries = append(tr.entries, &timestampReviewEntry{
			row:    r,
			adjust: false,
			text:   r.check.Suggested.Format("2006-01-02 15:04"),
		})
	}

	leftBox := container.NewVBox()
	for i, e := range tr.entries {
		i := i
		check := tr.effectiveCheck(e)
		bg := canvas.NewRectangle(timestampCardColorFor(check, e.adjust))
		idLabel := widget.NewLabelWithStyle(e.row.recorderID, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		statusLabel := widget.NewLabel(timestampIssueDetail(check, tr.tolerance))
		statusLabel.Wrapping = fyne.TextWrapWord
		cell := container.NewStack(bg, container.NewPadded(container.NewVBox(idLabel, statusLabel)))
		card := newTappableCard(cell, func() { tr.selectRow(i) })
		leftBox.Add(card)
		tr.cards = append(tr.cards, bg)
		tr.cardLabels = append(tr.cardLabels, statusLabel)
	}

	// One refresh callback for the whole screen (rather than one per row, the
	// way destFolderBrowser's pooled list rows need): the detail pane shows
	// only one recorder's files at a time, and tr.fileAudio is repointed at
	// whichever one that is every time rebuildDetail runs, so a single
	// registration here stays correct across selection changes for the life
	// of this screen.
	registerAudioRefreshFunc(tr.refreshAudio)

	tr.detailBox = container.NewStack()
	continueBtn := widget.NewButton(host.continueBaseLabel, nil)
	continueBtn.Importance = widget.HighImportance
	continueBtn.OnTapped = tr.applyAndContinue
	tr.continueBtn = continueBtn

	// applyOnlyBtn, when the host supplies it, sits between exit and
	// continue: same correction-applying behavior as continue, but hands
	// off to onApplyOnly instead. Medium importance - it commits to
	// applying, same as continue, but isn't "the thing this screen is for"
	// (see actionRow), which stays the rightmost button.
	var applyOnlyBtn *widget.Button
	if host.applyOnlyLabel != "" {
		applyOnlyBtn = widget.NewButton(host.applyOnlyLabel, tr.applyAndFinish)
		applyOnlyBtn.Importance = widget.MediumImportance
	}

	// exitBtn leaves without applying any of the corrections being reviewed
	// here, same as bypassing the check entirely - it deliberately calls
	// host.onExit directly, not applyAndContinue or host.onContinue, neither
	// of which it wants to run. Warns first, since it's easy to tap without
	// registering that anything typed into the review is about to be
	// discarded.
	//
	// Amber, not red: both buttons on this screen land in the same place
	// (Sync Recorders ends the session either way, Manage Files returns to
	// its setup screen either way), and the only difference between them is
	// whether the corrections are applied. Styling that difference as
	// blue-vs-red would read as "safe vs destructive" when nothing here
	// deletes anything - so the labels carry the distinction (the host
	// supplies both, and must phrase them as the same destination with and
	// without the corrections) and the color just marks discarded work.
	exitBtn := widget.NewButton(host.exitLabel, func() {
		showCautionConfirm("Corrections not applied", host.exitWarning,
			host.exitLabel, "Return to Review", func(ok bool) {
				if ok {
					host.onExit()
				}
			}, host.win)
	})
	exitBtn.Importance = widget.WarningImportance

	header := widget.NewLabelWithStyle("Review Recorder Timestamps", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	pathLbl := widget.NewLabel(host.parentPath)
	pathLbl.Truncation = fyne.TextTruncateEllipsis
	sub := widget.NewLabel("Each recorder's clock is assumed wrong (or right) for its entire session - adjusting one applies the same correction to every file from that recorder.")
	sub.Wrapping = fyne.TextWrapWord

	tr.summaryLbl = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	tr.summaryLbl.Wrapping = fyne.TextWrapWord
	tr.refreshSummary()

	toleranceRow := tr.buildToleranceRow()

	left := container.NewBorder(sectionHeader("Recorders"), nil, nil, nil, container.NewVScroll(leftBox))
	right := container.NewBorder(nil, nil, nil, nil, tr.detailBox)
	split := container.NewHSplit(left, right)
	split.SetOffset(0.3)

	rightBtns := []fyne.CanvasObject{}
	if applyOnlyBtn != nil {
		rightBtns = append(rightBtns, applyOnlyBtn)
	}
	rightBtns = append(rightBtns, continueBtn)

	content := container.NewBorder(
		container.NewVBox(header, pathLbl, sub, tr.summaryLbl, toleranceRow, widget.NewSeparator()),
		actionRow(exitBtn, rightBtns...),
		nil, nil,
		split,
	)
	host.s.setContent(container.NewPadded(content))

	tr.selectRow(0)
}

// timestampToleranceSliderMax bounds the review screen's tolerance slider. A
// recorder's time-of-day only has to land within tolerance of the nearest
// other recorder, and the AM/PM heuristic already covers the ~12 h case, so
// anything past a few hours would just wave every recorder through - 180 min
// is a generous ceiling that still leaves the slider's useful range legible.
const timestampToleranceSliderMax = 180

// buildToleranceRow builds the live "match tolerance" control: a slider (with
// a numeric read-out) that re-judges every recorder as it moves, so the
// researcher can see directly how tight or loose a tolerance flags. The
// chosen value is persisted so it carries into the next run and the offload
// path (both read RecorderSettings.TimestampToleranceMinutes).
func (tr *timestampReviewScreen) buildToleranceRow() fyne.CanvasObject {
	valueLbl := widget.NewLabel("")
	setValueLabel := func(min int) {
		valueLbl.SetText(fmt.Sprintf("%d min", min))
	}

	slider := widget.NewSlider(0, timestampToleranceSliderMax)
	slider.Step = 1
	slider.SetValue(float64(tr.tolerance / time.Minute))
	setValueLabel(int(tr.tolerance / time.Minute))
	slider.OnChanged = func(v float64) {
		min := int(v)
		setValueLabel(min)
		tr.applyTolerance(min)
	}

	label := widget.NewLabel("Match tolerance")
	return container.NewBorder(nil, nil, label, valueLbl, slider)
}

// applyTolerance re-runs every recorder's check at the given tolerance
// (minutes), refreshes the cards and the open detail pane to match, and
// persists the value. Cards keep their original position rather than
// re-sorting, so a recorder the reader is watching doesn't jump as its
// verdict flips. A recorder the user has already given a manual start time
// (adjust) keeps that text - only untouched rows adopt the fresh suggestion.
func (tr *timestampReviewScreen) applyTolerance(minutes int) {
	tr.tolerance = time.Duration(minutes) * time.Minute
	tr.host.s.cfg.RecorderSettings.TimestampToleranceMinutes = minutes
	tr.host.s.saveConfig()

	for i, e := range tr.entries {
		if e.row.recheck != nil {
			e.row.check = e.row.recheck(tr.tolerance)
		}
		if !e.adjust {
			e.text = e.row.check.Suggested.Format("2006-01-02 15:04")
		}
		tr.refreshCard(i)
	}
	tr.refreshSummary()
	tr.rebuildDetail()
}

// effectiveCheck is what actually drives an entry's card color and status
// text: when e has a valid "New start time" override, the edited candidate
// time re-judged against the other recorders at the live tolerance (see
// recheckAt) - otherwise just the original detection. This is what lets a
// card go blue only once the typed-in time actually resolves the mismatch,
// rather than the instant the checkbox is ticked.
func (tr *timestampReviewScreen) effectiveCheck(e *timestampReviewEntry) recorder.TimestampIssue {
	if e.adjust && e.row.recheckAt != nil {
		if edited, err := time.ParseInLocation("2006-01-02 15:04", e.text, e.row.check.Recorded.Location()); err == nil {
			return e.row.recheckAt(edited, tr.tolerance)
		}
	}
	return e.row.check
}

// refreshCard repaints entries[i]'s left-hand card - fill color and status
// line - from its current effective check. Called whenever anything that
// feeds effectiveCheck changes: the tolerance slider, the New start time
// text, or the adjust checkbox.
func (tr *timestampReviewScreen) refreshCard(i int) {
	e := tr.entries[i]
	check := tr.effectiveCheck(e)
	tr.cards[i].FillColor = timestampCardColorFor(check, e.adjust)
	tr.cards[i].Refresh()
	tr.cardLabels[i].SetText(timestampIssueDetail(check, tr.tolerance))
}

// refreshSummary restates how many recorders currently look off. An all-clear
// check gets an explicit confirmation (so the screen still shows, and reads as
// "nothing to do" rather than an unexplained list); otherwise it names the
// count that need a look.
func (tr *timestampReviewScreen) refreshSummary() {
	if tr.summaryLbl == nil {
		return
	}
	flagged := 0
	for _, e := range tr.entries {
		if tr.effectiveCheck(e).Suspicious {
			flagged++
		}
	}
	total := len(tr.entries)
	if flagged == 0 {
		tr.summaryLbl.SetText(fmt.Sprintf("All %s line up — nothing needs correcting. Continue, or set a new start time on any recorder to override.", plural(total, "recorder start time", "recorder start times")))
		return
	}
	verb := "look"
	if flagged == 1 {
		verb = "looks"
	}
	tr.summaryLbl.SetText(fmt.Sprintf("%d of %d %s %s off (highlighted) — review each and set a new start time where needed.", flagged, total, pluralWord(total, "recorder", ""), verb))
}

// refreshContinueLabel switches the continue button between
// host.continueBaseLabel (nothing checked, so tapping it applies nothing)
// and host.continueLabel (at least one recorder checked for a correction) -
// called every time an adjust checkbox is toggled.
func (tr *timestampReviewScreen) refreshContinueLabel() {
	if tr.continueBtn == nil {
		return
	}
	label := tr.host.continueBaseLabel
	for _, e := range tr.entries {
		if e.adjust {
			label = tr.host.continueLabel
			break
		}
	}
	tr.continueBtn.SetText(label)
}

// selectRow switches the detail pane to entries[i] and refreshes every left
// card's highlight so the selected one reads clearly against the rest.
func (tr *timestampReviewScreen) selectRow(i int) {
	tr.selected = i
	for j, bg := range tr.cards {
		if j == i {
			bg.StrokeColor = theme.Color(theme.ColorNamePrimary)
			bg.StrokeWidth = 3
		} else {
			bg.StrokeWidth = 0
		}
		bg.Refresh()
	}
	tr.rebuildDetail()
}

// rebuildDetail redraws the right-hand pane for the currently selected
// recorder: its Adjust checkbox and correction entry, plus a live rename
// preview for every one of its files - the old name struck through and the
// new one beside it, recomputed from whatever's currently in the entry.
func (tr *timestampReviewScreen) rebuildDetail() {
	e := tr.entries[tr.selected]

	header := widget.NewLabelWithStyle(
		fmt.Sprintf("%s — %s", e.row.recorderID, timestampIssueDetail(tr.effectiveCheck(e), tr.tolerance)),
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	header.Wrapping = fyne.TextWrapWord
	refreshHeader := func() {
		header.SetText(fmt.Sprintf("%s — %s", e.row.recorderID, timestampIssueDetail(tr.effectiveCheck(e), tr.tolerance)))
	}

	entry := widget.NewEntry()
	entry.SetText(e.text)

	errLbl := widget.NewLabel("")
	errLbl.Wrapping = fyne.TextWrapWord

	tr.audioStatusLbl = widget.NewLabel("")
	tr.audioStatusLbl.Wrapping = fyne.TextWrapWord

	previewLbl := widget.NewLabel("")
	adjustLbl := widget.NewLabel("")
	refreshPreview := func() {
		if !e.adjust {
			previewLbl.SetText("")
			adjustLbl.SetText("")
			return
		}
		edited, err := time.ParseInLocation("2006-01-02 15:04", e.text, e.row.check.Recorded.Location())
		if err != nil {
			previewLbl.SetText("")
			adjustLbl.SetText("")
			return
		}
		previewLbl.SetText("New start time: " + plainDateTime(edited))
		adjustLbl.SetText("Adjustment: " + formatAdjustment(e.row.check.Recorded, edited))
	}

	filesBox := container.NewVBox()
	// addFileRow wraps content in a playable row - a transport (play/
	// restart/spinner) streaming this file from the highest-priority
	// Location that has it (see audioOpener), the same controls
	// destFolderBrowser's rows use. registration with the player's change
	// notifications happens once for the whole screen (see
	// showTimestampReview), so update is called with register=nil here.
	addFileRow := func(sf recorder.SourceFile, content fyne.CanvasObject) {
		row := audioRow(content, newPresenceIndicator())
		controls := audioControlsFrom(row)
		relPath := joinRel(e.row.relDir, sf.DestRelPath)
		controls.update(nil, e.row.locs, relPath, sf.DestRelPath)
		tr.fileAudio = append(tr.fileAudio, timestampFileAudio{controls, e.row.locs, relPath, sf.DestRelPath})
		filesBox.Add(row)
	}
	refreshFiles := func() {
		filesBox.Objects = nil
		tr.fileAudio = tr.fileAudio[:0]
		for _, sf := range e.row.sourceFiles {
			oldT, ok := e.row.parser.ParseTimestamp(sf.DestRelPath)
			if !ok || !e.adjust {
				addFileRow(sf, widget.NewLabel(sf.DestRelPath))
				continue
			}
			edited, err := time.ParseInLocation("2006-01-02 15:04", e.text, e.row.check.Recorded.Location())
			if err != nil {
				addFileRow(sf, widget.NewLabel(sf.DestRelPath))
				continue
			}
			delta := edited.Sub(e.row.check.Recorded)
			newT := oldT.Add(delta)
			newRel := e.row.parser.RenameForTimestamp(sf.DestRelPath, newT)
			oldLbl := widget.NewLabel(strikethrough(sf.DestRelPath))
			newLbl := widget.NewLabelWithStyle(newRel, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			oldCol := container.NewVBox(oldLbl, widget.NewLabel(plainDateTime(oldT)))
			newCol := container.NewVBox(newLbl, widget.NewLabel(plainDateTime(newT)))
			addFileRow(sf, container.NewHBox(oldCol, widget.NewLabel("→"), newCol))
		}
		filesBox.Refresh()
	}

	entry.OnChanged = func(text string) {
		e.text = text
		errLbl.SetText("")
		refreshPreview()
		refreshFiles()
		refreshHeader()
		tr.refreshCard(tr.selected)
	}

	adjust := widget.NewCheck("New start time", nil)
	adjust.SetChecked(e.adjust)
	setEnabled := func(on bool) {
		if on {
			entry.Enable()
		} else {
			entry.Disable()
		}
	}
	setEnabled(e.adjust)
	adjust.OnChanged = func(checked bool) {
		e.adjust = checked
		setEnabled(checked)
		refreshPreview()
		refreshFiles()
		refreshHeader()
		tr.refreshCard(tr.selected)
		tr.refreshContinueLabel()
	}

	refreshPreview()
	refreshFiles()

	detail := container.NewBorder(
		container.NewVBox(header, container.NewBorder(nil, nil, adjust, nil, entry), previewLbl, adjustLbl, errLbl, widget.NewSeparator(),
			widget.NewLabelWithStyle(fmt.Sprintf("Files (%d)", len(e.row.sourceFiles)), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			tr.audioStatusLbl),
		nil, nil, nil,
		container.NewVScroll(filesBox),
	)

	tr.detailBox.Objects = []fyne.CanvasObject{container.NewPadded(detail)}
	tr.detailBox.Refresh()
}

// refreshAudio is this screen's one player-state-change callback (see
// registerAudioRefreshFunc in showTimestampReview): it repaints every file
// row currently on screen for whichever recorder is selected, and surfaces a
// playback error the same way destFolderBrowser's refresh does.
func (tr *timestampReviewScreen) refreshAudio() {
	if tr.audioStatusLbl != nil {
		if st := audioPlayer().State(); st.Err != nil {
			tr.audioStatusLbl.SetText("Couldn't play that file. " + classifyError(st.Err).String())
		}
	}
	for _, fa := range tr.fileAudio {
		fa.controls.update(nil, fa.locs, fa.relPath, fa.filename)
	}
}

// applyFixes validates every checked entry's correction text and applies
// them all (renaming every file for that recorder - see
// recorder.ApplyTimestampFix - and re-uploading outside batch mode). It
// reports false, having selected the offending recorder instead of applying
// anything, if any checked entry's text fails to parse.
func (tr *timestampReviewScreen) applyFixes() bool {
	type parsedFix struct {
		entry *timestampReviewEntry
		delta time.Duration
	}
	var fixes []parsedFix
	for _, e := range tr.entries {
		if !e.adjust {
			continue
		}
		edited, err := time.ParseInLocation("2006-01-02 15:04", e.text, e.row.check.Recorded.Location())
		if err != nil {
			tr.selectForEntry(e)
			return false
		}
		delta := edited.Sub(e.row.check.Recorded)
		if delta == 0 {
			continue
		}
		fixes = append(fixes, parsedFix{e, delta})
	}

	for _, f := range fixes {
		_ = f.entry.row.apply(func(t time.Time) time.Time { return t.Add(f.delta) })
		if tr.host.afterFix != nil {
			tr.host.afterFix(f.entry.row, f.delta)
		}
	}
	return true
}

// applyAndContinue applies every checked correction (see applyFixes), then
// hands off to onContinue (Batch Upload or End Sync).
func (tr *timestampReviewScreen) applyAndContinue() {
	if tr.applyFixes() {
		tr.host.onContinue()
	}
}

// applyAndFinish is applyAndContinue's counterpart for the optional
// applyOnlyBtn: applies every checked correction, then hands off to
// onApplyOnly instead of onContinue.
func (tr *timestampReviewScreen) applyAndFinish() {
	if tr.applyFixes() {
		tr.host.onApplyOnly()
	}
}

// selectForEntry switches the detail pane to e (used to surface a parse
// error on Apply without silently skipping that recorder's correction).
func (tr *timestampReviewScreen) selectForEntry(e *timestampReviewEntry) {
	for i, other := range tr.entries {
		if other == e {
			tr.selectRow(i)
			return
		}
	}
}

// tappableCard wraps arbitrary content in a fyne.Tappable, used for the
// review screen's selectable recorder cards - the same "colored background
// behind padded content" shape the active-sync screen's rows use, plus a
// tap handler.
type tappableCard struct {
	widget.BaseWidget
	content  fyne.CanvasObject
	onTapped func()
}

func newTappableCard(content fyne.CanvasObject, onTapped func()) *tappableCard {
	c := &tappableCard{content: content, onTapped: onTapped}
	c.ExtendBaseWidget(c)
	return c
}

func (c *tappableCard) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(c.content)
}

func (c *tappableCard) Tapped(*fyne.PointEvent) {
	if c.onTapped != nil {
		c.onTapped()
	}
}

// reuploadCorrectedFiles pushes every one of row's files - renamed by delta,
// see showTimestampReview - to every one of sc.params.uploads. Needed only
// outside batch-upload mode: there, each file already uploaded under its
// (bad) name the instant it landed locally during the sync itself (see
// StartOffload), so the corrected local copy never gets uploaded on its own
// unless this pushes it. The stale, wrongly-named remote copy is left in
// place rather than deleted, per this app's never-delete rule (see
// CLAUDE.md) - the user can clean it up manually once the correctly-named
// copy is confirmed uploaded. destDirs is row's recorder's local
// destinations (Sync Recorders-only - passed in rather than read off row
// since timestampReviewRow itself no longer carries it, see
// checkTimestampsThen's destDirsByID).
func reuploadCorrectedFiles(sc *recorderSyncScreen, row timestampReviewRow, destDirs []string, delta time.Duration) {
	if len(destDirs) == 0 {
		return
	}
	subpathParts := splitSubpathUI(sc.params.subpath)
	for _, sf := range row.sourceFiles {
		t, ok := row.parser.ParseTimestamp(sf.DestRelPath)
		if !ok {
			continue
		}
		newRel := row.parser.RenameForTimestamp(sf.DestRelPath, t.Add(delta))
		localPath := filepath.Join(destDirs[0], newRel)
		relParts := append([]string{sc.params.experimentName}, subpathParts...)
		relParts = append(relParts, row.recorderID, newRel)
		relPath := filepath.Join(relParts...)
		for _, dest := range sc.params.uploads {
			dest := dest
			go recorder.UploadCorrectedFile(sc.watchCtx, row.recorderID, relPath, localPath, dest, sc.uploads.onUploadEvent)
		}
	}
}
