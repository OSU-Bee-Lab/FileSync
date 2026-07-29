package ui

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	_ "github.com/rclone/rclone/backend/local" // the probe files live on disk

	"github.com/OSU-Bee-Lab/filesync/internal/audio"
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// These tests drive the real transport - a real browser row, the real shared
// player, and (where the machine has an audio device) real playback - because
// the bugs this feature has actually had were all in the wiring between those
// pieces rather than in any one of them. Each piece passed in isolation while
// the row on screen never changed.
//
// Two things about testing this. Playback changes reach the UI through
// fyne.Do, which the real driver marshals onto the main thread but the test
// driver runs inline on whichever goroutine called it - so left alone, the
// player's notifications race the test goroutine over the same List and Fyne
// panics inside its own layout code. newProbeBrowser therefore silences
// notifications and each test drives refreshAudioRows itself, at the point the
// app would have run it; TestPlaybackNotifiesTheUI covers the delivery that
// leads up to that. Separately, a list row only re-runs its update function
// when the list is refreshed, so a browser has to be rendered before any state
// change or the assertions measure the first render rather than the update.

// newProbeBrowser renders a file browser showing one file, without going
// through a listing.
func newProbeBrowser(t *testing.T, dir, filename string) (*destFolderBrowser, fyne.Window) {
	t.Helper()
	w := test.NewWindow(nil)
	t.Cleanup(func() {
		audioPlayer().Stop()
		clearAudioRefreshers()
		w.Close()
	})

	// These tests drive the real audio device; keep them out of the speakers.
	audioPlayer().SetVolume(0)

	b := newDestFolderBrowser(w, false)
	b.showFiles = true
	b.selectFiles = true
	b.multiSelect = true
	b.locs = []syncengine.Location{{ID: "l", Name: "l", Kind: syncengine.LocationLocal, RootPath: dir}}
	b.entries = []syncengine.Entry{{Name: filename, Size: 100}}

	w.SetContent(b.CanvasObject())
	w.Resize(fyne.NewSize(500, 400))

	// Render once so the browser subscribes to playback changes (registration
	// happens as rows render), then silence delivery so only the test drives
	// repaints - see the note at the top of this file.
	transportOf(t, b)
	audioPlayer().SetOnChange(nil)
	return b, w
}

// transportOf finds the first row's transport controls in a rendered browser.
func transportOf(t *testing.T, b *destFolderBrowser) *audioRowControls {
	t.Helper()
	var found *fyne.Container
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		if found != nil {
			return
		}
		if c, ok := o.(*fyne.Container); ok {
			if len(c.Objects) == 2 {
				if box, ok := c.Objects[1].(*fyne.Container); ok && len(box.Objects) == 3 {
					if _, ok := box.Objects[0].(*widget.Button); ok {
						found = c
						return
					}
				}
			}
			for _, child := range c.Objects {
				walk(child)
			}
			return
		}
		if w, ok := o.(fyne.Widget); ok {
			for _, child := range test.WidgetRenderer(w).Objects() {
				walk(child)
			}
		}
	}
	walk(b.CanvasObject())
	if found == nil {
		t.Fatal("no file row rendered")
	}
	return audioControlsFrom(found)
}

func playIcon(t *testing.T, c *audioRowControls) string {
	t.Helper()
	if c.play.Icon == nil {
		return "<none>"
	}
	return c.play.Icon.Name()
}

// TestTransportShowsSpinnerWhileLoading: a preview that has been tapped but
// hasn't reached its first byte shows a spinner in place of the play button.
func TestTransportShowsSpinnerWhileLoading(t *testing.T) {
	b, _ := newProbeBrowser(t, t.TempDir(), "rec.mp3")

	if c := transportOf(t, b); c.spinner.Visible() || c.restart.Visible() || !c.play.Visible() {
		t.Fatalf("idle row: restart=%v spinner=%v play=%v, want only play", c.restart.Visible(), c.spinner.Visible(), c.play.Visible())
	}

	// An opener that never returns holds the player in its loading state.
	blocked := make(chan struct{})
	defer close(blocked)
	audioPlayer().Toggle("rec.mp3", "rec.mp3", func(ctx context.Context) (io.ReadCloser, error) {
		<-blocked
		return nil, context.Canceled
	})
	waitFor(t, func() bool { return audioPlayer().State().Loading })
	refreshAudioRows()

	c := transportOf(t, b)
	if !c.spinner.Visible() {
		t.Error("no spinner while the preview was loading")
	}
	if c.play.Visible() {
		t.Error("play button still shown while the preview was loading")
	}
	if !c.restart.Visible() {
		t.Error("back-to-start not shown for the loading row")
	}
}

// TestTransportTracksPlayback is the end-to-end case: play swaps to pause,
// back-to-start appears, and pausing swaps back.
func TestTransportTracksPlayback(t *testing.T) {
	dir := t.TempDir()
	writeToneWAV(t, filepath.Join(dir, "tone.wav"), 10)
	b, _ := newProbeBrowser(t, dir, "tone.wav")

	audioPlayer().Toggle("tone.wav", "tone.wav", audioOpener(b.locs, "tone.wav"))
	waitFor(t, func() bool { return !audioPlayer().State().Loading })
	if err := audioPlayer().State().Err; err != nil {
		if strings.Contains(err.Error(), "audio device") {
			t.Skipf("no audio device available: %v", err)
		}
		t.Fatalf("playback failed: %v", err)
	}
	refreshAudioRows()

	c := transportOf(t, b)
	if got := playIcon(t, c); !strings.Contains(got, "pause") {
		t.Errorf("play button icon during playback = %s, want the pause icon", got)
	}
	if !c.restart.Visible() {
		t.Error("back-to-start not shown during playback")
	}
	if c.spinner.Visible() {
		t.Error("spinner still shown during playback")
	}

	waitFor(t, func() bool { return audioPlayer().State().Position > 0 })

	audioPlayer().Toggle("tone.wav", "tone.wav", audioOpener(b.locs, "tone.wav"))
	waitFor(t, func() bool { return !audioPlayer().State().Playing })
	refreshAudioRows()

	c = transportOf(t, b)
	if got := playIcon(t, c); !strings.Contains(got, "play") {
		t.Errorf("play button icon while paused = %s, want the play icon", got)
	}
	if !c.restart.Visible() {
		t.Error("back-to-start not shown while paused part-way through a file")
	}
}

// TestTransportUpdatesEveryLiveBrowser is the regression test for the bug that
// made all of this look broken in the app: Manage Files builds two browsers,
// and with a single playback callback only the last one built was ever
// repainted - so the browser being played from stayed frozen on its play
// button no matter what the player did.
func TestTransportUpdatesEveryLiveBrowser(t *testing.T) {
	dir := t.TempDir()
	first, _ := newProbeBrowser(t, dir, "rec.mp3")
	second, _ := newProbeBrowser(t, dir, "rec.mp3")

	blocked := make(chan struct{})
	defer close(blocked)
	audioPlayer().Toggle("rec.mp3", "rec.mp3", func(ctx context.Context) (io.ReadCloser, error) {
		<-blocked
		return nil, context.Canceled
	})
	waitFor(t, func() bool { return audioPlayer().State().Loading })
	refreshAudioRows()

	for label, b := range map[string]*destFolderBrowser{"first browser": first, "second browser": second} {
		if c := transportOf(t, b); !c.spinner.Visible() {
			t.Errorf("%s did not repaint: spinner=%v play=%v", label, c.spinner.Visible(), c.play.Visible())
		}
	}
}

// TestTransportHiddenForUnplayableRows keeps the controls off rows that aren't
// audio at all.
func TestTransportHiddenForUnplayableRows(t *testing.T) {
	b, _ := newProbeBrowser(t, t.TempDir(), "metadata.csv")
	c := transportOf(t, b)
	if c.play.Visible() || c.restart.Visible() || c.spinner.Visible() {
		t.Errorf("controls shown for a non-audio file: restart=%v spinner=%v play=%v",
			c.restart.Visible(), c.spinner.Visible(), c.play.Visible())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the player to reach the expected state")
}

// writeToneWAV writes a mono 16-bit sine tone of the given length, long enough
// that playback is still running when the assertions run.
func writeToneWAV(t *testing.T, path string, seconds int) {
	t.Helper()
	const rate = 44100
	frames := rate * seconds
	data := make([]byte, 0, frames*2)
	for i := 0; i < frames; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		data = binary.LittleEndian.AppendUint16(data, uint16(v))
	}
	var buf []byte
	buf = append(buf, "RIFF"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(36+len(data)))
	buf = append(buf, "WAVEfmt "...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint32(buf, rate)
	buf = binary.LittleEndian.AppendUint32(buf, rate*2)
	buf = binary.LittleEndian.AppendUint16(buf, 2)
	buf = binary.LittleEndian.AppendUint16(buf, 16)
	buf = append(buf, "data"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(data)))
	buf = append(buf, data...)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPlaybackNotifiesTheUI covers the half of the chain the tests above
// silence: the player telling the UI that something changed. Without this, a
// change that never fires would leave the controls frozen no matter how
// correct their rendering is - which is how this feature first shipped broken.
//
// The callback is a bare "something changed" signal and reads the state when it
// runs, so the state it observes is whatever is current by then. That's why the
// load is held open here rather than allowed to race to completion.
func TestPlaybackNotifiesTheUI(t *testing.T) {
	p := audio.NewPlayer()
	notified := make(chan struct{}, 16)
	p.SetOnChange(func() { notified <- struct{}{} })

	release := make(chan struct{})
	p.Toggle("f.wav", "f.wav", func(ctx context.Context) (io.ReadCloser, error) {
		<-release
		return nil, errors.New("could not reach the location")
	})

	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("pressing play was never reported to the UI, so no spinner would ever appear")
	}
	if st := p.State(); st.Err != nil && strings.Contains(st.Err.Error(), "audio device") {
		t.Skipf("no audio device available: %v", st.Err)
	} else if !st.Loading {
		t.Errorf("state while the file was still opening = %+v, want Loading", st)
	}

	close(release)
	deadline := time.After(2 * time.Second)
	for {
		if p.State().Err != nil {
			return
		}
		select {
		case <-notified:
		case <-deadline:
			t.Fatal("the failure was never reported to the UI")
		}
	}
}

// TestRestartPreservesPausedState: back-to-start on a live preview keeps
// playing from the beginning, but on a paused one it parks at the beginning
// and waits - pressing it must never start audio the user had stopped.
func TestRestartPreservesPausedState(t *testing.T) {
	dir := t.TempDir()
	writeToneWAV(t, filepath.Join(dir, "tone.wav"), 10)
	b, _ := newProbeBrowser(t, dir, "tone.wav")

	play := func() {
		audioPlayer().Toggle("tone.wav", "tone.wav", audioOpener(b.locs, "tone.wav"))
	}

	play()
	waitFor(t, func() bool { return !audioPlayer().State().Loading })
	if err := audioPlayer().State().Err; err != nil {
		if strings.Contains(err.Error(), "audio device") {
			t.Skipf("no audio device available: %v", err)
		}
		t.Fatalf("playback failed: %v", err)
	}
	waitFor(t, func() bool { return audioPlayer().State().Position > 0 })

	// Restart while playing: still playing, back at the beginning.
	audioPlayer().Restart()
	waitFor(t, func() bool { return !audioPlayer().State().Loading })
	if st := audioPlayer().State(); !st.Playing || st.AtStart {
		t.Errorf("restart while playing left playing=%v atStart=%v, want playing and not parked", st.Playing, st.AtStart)
	}

	// Pause, then restart: parked at the start, and silent.
	play()
	waitFor(t, func() bool { return !audioPlayer().State().Playing })
	audioPlayer().Restart()
	waitFor(t, func() bool { return !audioPlayer().State().Loading })

	st := audioPlayer().State()
	if st.Playing {
		t.Error("back-to-start resumed playback on a paused preview")
	}
	if !st.AtStart {
		t.Error("back-to-start did not park the paused preview at the start")
	}
	if st.Position != 0 {
		t.Errorf("position after restart = %v, want 0", st.Position)
	}

	// Give it a moment to prove it stays silent rather than merely starting late.
	time.Sleep(400 * time.Millisecond)
	if st := audioPlayer().State(); st.Playing || st.Position > 0 {
		t.Errorf("parked preview started itself: playing=%v position=%v", st.Playing, st.Position)
	}

	// The control itself is hidden while parked - there is nothing to go back to.
	refreshAudioRows()
	if c := transportOf(t, b); c.restart.Visible() {
		t.Error("back-to-start still shown while the file is parked at its start")
	}

	// And playing again from parked works.
	play()
	waitFor(t, func() bool { return audioPlayer().State().Playing })
	if st := audioPlayer().State(); st.AtStart {
		t.Error("atStart not cleared when a parked preview was played")
	}
}

// TestRapidStartStopDoesNotCrash reproduces the shape of a crash this had in
// the app: stopping a preview while its read-ahead was mid-read against the
// stream. Teardown closed that stream from the UI goroutine while the filler
// goroutine was inside a read on it, and the read dereferenced a reader that
// had just been cleared. Playing and stopping repeatedly - which is what
// browsing while a preview runs does - is how that window gets hit.
func TestRapidStartStopDoesNotCrash(t *testing.T) {
	dir := t.TempDir()
	writeToneWAV(t, filepath.Join(dir, "a.wav"), 30)
	writeToneWAV(t, filepath.Join(dir, "b.wav"), 30)
	b, _ := newProbeBrowser(t, dir, "a.wav")

	play := func(name string) {
		audioPlayer().Toggle(name, name, audioOpener(b.locs, name))
	}

	play("a.wav")
	waitFor(t, func() bool { return !audioPlayer().State().Loading })
	if err := audioPlayer().State().Err; err != nil {
		if strings.Contains(err.Error(), "audio device") {
			t.Skipf("no audio device available: %v", err)
		}
		t.Fatalf("playback failed: %v", err)
	}

	// Stop, restart, and switch files at intervals around the read-ahead's own
	// timing, so teardown lands at varying points inside a read.
	for i := 0; i < 25; i++ {
		time.Sleep(time.Duration(i%7) * time.Millisecond)
		switch i % 5 {
		case 0:
			audioPlayer().Stop()
		case 1:
			play("a.wav")
		case 2:
			audioPlayer().Restart()
		case 3:
			play("b.wav")
		case 4:
			stopAudio()
		}
	}

	audioPlayer().Stop()
	if st := audioPlayer().State(); st.Key != "" || st.Playing {
		t.Errorf("player did not come to rest: %+v", st)
	}
}
