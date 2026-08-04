package ui

import (
	"context"
	"io"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/audio"
	_ "github.com/OSU-Bee-Lab/filesync/internal/audio/drivers" // register the supported formats
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// One player for the whole app: oto allows a single audio device per process,
// and one preview at a time is what the UI wants anyway (pressing play on a
// second file stops the first). Created lazily but not opened lazily - the
// device itself isn't touched until the first play.
var (
	playerOnce sync.Once
	player     *audio.Player
)

func audioPlayer() *audio.Player {
	playerOnce.Do(func() { player = audio.NewPlayer() })
	return player
}

// stopAudio ends any preview in progress. Called from setContent, so leaving
// the screen a preview was started from stops it rather than leaving audio
// playing with no visible control to stop it.
func stopAudio() {
	if player != nil {
		player.Stop()
	}
}

// audioRefreshers are the screen's subscribers to playback state changes -
// one callback per browser/screen that shows transport controls, not one per
// row - and audioRefreshGen is bumped whenever the screen is replaced so
// browsers left behind on the old screen re-register (or don't) rather than
// accumulating for the life of the session.
//
// This is a list rather than the single callback it started as: a screen can
// hold more than one browser - Manage Files has both a From and a To - and
// with one callback only the last one built ever got repainted, so the browser
// actually being played from sat frozen on its play button. That was the whole
// of the "nothing updates" bug.
//
// Only ever touched on the UI goroutine: subscribers register as their rows
// render, and the fan-out runs inside fyne.Do.
var (
	audioRefreshers []func()
	audioRefreshGen int
)

// clearAudioRefreshers drops the current screen's subscribers. Called from
// setContent, which is also where playback stops - subscribers register when
// their rows render, which is after the new content is installed, so this
// can't drop the incoming screen's own registration.
func clearAudioRefreshers() {
	audioRefreshers = nil
	audioRefreshGen++
}

// registerAudioRefreshFunc subscribes fn to playback state changes - the
// generic form behind registerAudioRefresh (destFolderBrowser's per-browser,
// dedup'd registration) and used directly by screens that show playable rows
// outside a destFolderBrowser (e.g. the timestamp review screen), where one
// registration for the whole screen, made once, is enough.
func registerAudioRefreshFunc(fn func()) {
	audioRefreshers = append(audioRefreshers, fn)
	audioPlayer().SetOnChange(func() { fyne.Do(refreshAudioRows) })
}

// registerAudioRefresh subscribes b's rows to playback state changes. Called
// from row rendering rather than from the browser's constructor, so that a
// browser only counts as live once it has actually put rows on screen.
func registerAudioRefresh(b *destFolderBrowser) {
	if b.audioRefreshGen == audioRefreshGen && b.audioRegistered {
		return
	}
	b.audioRegistered = true
	b.audioRefreshGen = audioRefreshGen
	registerAudioRefreshFunc(func() {
		st := audioPlayer().State()
		if st.Err != nil {
			b.statusLbl.SetText("Couldn't play that file. " + classifyError(st.Err).String())
		}
		b.list.Refresh()
	})
}

// refreshAudioRows runs every registered subscriber's refresh callback.
func refreshAudioRows() {
	for _, fn := range audioRefreshers {
		fn()
	}
}

// audioOpener streams relPath for playback from the best available location:
// locs is ranked by normalizedLocationOrder, the same locals-before-remotes,
// Priority-ordered rule the sync engine picks a source with, so a file that
// exists on a local drive is never played off a remote.
//
// The stream is opened fresh on every play and every restart - nothing is
// downloaded ahead of time, and only the bytes actually listened to are
// transferred.
func audioOpener(locs []syncengine.Location, relPath string) audio.Opener {
	ordered := normalizedLocationOrder(locs)
	return func(ctx context.Context) (io.ReadCloser, error) {
		stream, _, err := syncengine.StreamFile(ctx, ordered, relPath)
		if err != nil {
			return nil, err
		}
		return stream, nil
	}
}

// audioRowControls is the per-row transport: a play button that becomes a
// pause button while that row is playing and a spinner while it's still
// opening, plus a back-to-start button for whichever row is loaded. All of it
// is hidden for rows that aren't a playable audio file.
type audioRowControls struct {
	restart *widget.Button
	play    *widget.Button
	spinner *audioSpinner
	box     *fyne.Container

	// row is the whole row this transport sits in. Showing or hiding a
	// control changes the space the transport needs, and Fyne won't re-run
	// the row's layout on its own, so the row is refreshed whenever
	// visibility changes - otherwise a newly shown button is laid out at zero
	// width and simply never appears.
	row *fyne.Container
}

// audioRow builds a list row: content, with a leading indicator (e.g. a
// presenceIndicator - never nil, since Fyne containers can't hold a nil
// child; pass an empty one to show nothing) and transport controls both
// pinned to the trailing edge, in that order, so the indicator sits
// immediately before the transport controls rather than past them. The
// objects are passed to container.New explicitly (rather than via
// container.NewBorder) so their order in Objects is fixed and a pooled row
// can be picked apart again with audioControlsFrom/presenceFrom.
func audioRow(content, leading fyne.CanvasObject) *fyne.Container {
	c := &audioRowControls{
		restart: widget.NewButtonWithIcon("", theme.MediaSkipPreviousIcon(), nil),
		play:    widget.NewButtonWithIcon("", theme.MediaPlayIcon(), nil),
		spinner: newAudioSpinner(),
	}
	c.restart.Importance = widget.LowImportance
	c.play.Importance = widget.LowImportance
	c.restart.Hide()
	c.play.Hide()
	c.spinner.Hide()
	c.box = container.NewHBox(leading, c.restart, c.spinner, c.play)

	c.row = container.New(layout.NewBorderLayout(nil, nil, nil, c.box), content, c.box)
	return c.row
}

// audioControlsFrom recovers the transport from a row built by audioRow. List
// rows are pooled and reused as the list scrolls, so the widgets have to be
// found again by position rather than held onto.
func audioControlsFrom(row *fyne.Container) *audioRowControls {
	box := row.Objects[1].(*fyne.Container)
	return &audioRowControls{
		restart: box.Objects[1].(*widget.Button),
		spinner: box.Objects[2].(*audioSpinner),
		play:    box.Objects[3].(*widget.Button),
		box:     box,
		row:     row,
	}
}

// presenceFrom recovers the leading indicator from a row built by audioRow.
func presenceFrom(row *fyne.Container) *presenceIndicator {
	box := row.Objects[1].(*fyne.Container)
	return box.Objects[0].(*presenceIndicator)
}

// setVisible applies the visibility of all three controls at once, relaying
// out the row only when something actually changed (this runs on every list
// refresh, several times a second during playback).
func (c *audioRowControls) setVisible(restart, spinner, play bool) {
	changed := false
	for _, v := range []struct {
		obj  fyne.CanvasObject
		want bool
	}{{c.restart, restart}, {c.spinner, spinner}, {c.play, play}} {
		if v.obj.Visible() == v.want {
			continue
		}
		changed = true
		if v.want {
			v.obj.Show()
		} else {
			v.obj.Hide()
		}
	}
	if !changed {
		return
	}
	// The spinner only animates while it's on screen, and only costs anything
	// while it's animating.
	if spinner {
		c.spinner.Start()
	} else {
		c.spinner.Stop()
	}
	c.row.Refresh()
}

// hide blanks the controls, for a row that isn't a file at all (a folder, or
// the trailing add-folder row).
func (c *audioRowControls) hide() {
	c.setVisible(false, false, false)
	c.play.OnTapped = nil
	c.restart.OnTapped = nil
}

// update points the controls at the file at relPath (streamed from locs) and
// renders them against the current playback state. register, when non-nil,
// is called to (re)subscribe to playback changes the first time this row
// renders as playable - destFolderBrowser passes a closure that registers
// itself (deduped per browser, see registerAudioRefresh); callers that
// already registered once for the whole screen (e.g. the timestamp review
// screen, see registerAudioRefreshFunc) pass nil.
func (c *audioRowControls) update(register func(), locs []syncengine.Location, relPath, filename string) {
	if !audio.CanPlay(filename) || len(locs) == 0 {
		c.hide()
		return
	}
	if register != nil {
		register()
	}

	p := audioPlayer()
	st := p.State()
	// A row is the active one from the moment its play button is tapped until
	// playback stops or the file ends - which is exactly when back-to-start
	// applies, since anything loaded is either past its first sample or on its
	// way there. A failed preview isn't active: there's nothing to restart.
	active := st.Key == relPath && st.Err == nil

	// Nothing to go back to when the file is parked at its own start, which is
	// where back-to-start leaves a paused preview.
	showRestart := active && !st.AtStart

	switch {
	case active && st.Loading:
		// Reaching the location and parsing the header: a spinner stands in
		// for the play button, since there's nothing to pause yet.
		c.setVisible(showRestart, true, false)
	case active && st.Playing:
		c.play.SetIcon(theme.MediaPauseIcon())
		c.setVisible(showRestart, false, true)
	default:
		// Idle, or loaded and paused.
		c.play.SetIcon(theme.MediaPlayIcon())
		c.setVisible(showRestart, false, true)
	}

	c.play.OnTapped = func() {
		p.Toggle(relPath, filename, audioOpener(locs, relPath))
	}
	c.restart.OnTapped = func() { p.Restart() }
}
