package ui

import (
	"context"
	"encoding/binary"
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
// Two things about testing this. Playback state changes reach the UI through
// fyne.Do, which does nothing in a test binary (there is no app event loop to
// drain it), so these tests call refreshAudioRows directly at the point the
// app would have run it - TestPlaybackNotifiesTheUI covers the delivery that
// leads up to that. And a list row only re-runs its update function when the
// list is refreshed, so a browser must be rendered (transportOf does it)
// before any state change, or the assertions measure the first render rather
// than the update.

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

	b := newDestFolderBrowser(w, false)
	b.showFiles = true
	b.selectFiles = true
	b.multiSelect = true
	b.locs = []syncengine.Location{{ID: "l", Name: "l", Kind: syncengine.LocationLocal, RootPath: dir}}
	b.entries = []syncengine.Entry{{Name: filename, Size: 100}}

	w.SetContent(b.CanvasObject())
	w.Resize(fyne.NewSize(500, 400))
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
	transportOf(t, b) // render the row so later refreshes update it

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
	transportOf(t, first) // render both, so both subscribe
	transportOf(t, second)

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

// TestPlaybackNotifiesTheUI covers the half of the chain the tests above skip:
// the player telling the UI that something changed. Without this, a change
// that never fires would leave the controls frozen no matter how correct their
// rendering is - which is exactly how this feature first shipped broken.
func TestPlaybackNotifiesTheUI(t *testing.T) {
	p := audio.NewPlayer()
	notified := make(chan audio.State, 8)
	p.SetOnChange(func() { notified <- p.State() })

	// An unrecognized extension fails before the audio device is touched,
	// while still exercising the notify path either side of the load.
	p.Toggle("f.unplayable", "f.unplayable", func(ctx context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil
	})

	var loading, failed bool
	for i := 0; i < 2; i++ {
		select {
		case st := <-notified:
			if st.Loading {
				loading = true
			}
			if st.Err != nil {
				failed = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("playback state change was never reported to the UI")
		}
	}
	if !loading {
		t.Error("no notification carried the loading state, so no spinner would ever appear")
	}
	if !failed {
		t.Error("the failure was never reported")
	}
}
