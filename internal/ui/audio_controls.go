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

// audioRowControls is the per-row transport: play/pause, plus a back-to-start
// button that only appears once the file being previewed has moved off its
// first sample. Both are hidden for rows that aren't a playable audio file.
type audioRowControls struct {
	restart *widget.Button
	play    *widget.Button
	box     *fyne.Container
}

func newAudioRowControls() *audioRowControls {
	c := &audioRowControls{
		restart: widget.NewButtonWithIcon("", theme.MediaSkipPreviousIcon(), nil),
		play:    widget.NewButtonWithIcon("", theme.MediaPlayIcon(), nil),
	}
	c.restart.Importance = widget.LowImportance
	c.play.Importance = widget.LowImportance
	c.restart.Hide()
	c.play.Hide()
	c.box = container.NewHBox(c.restart, c.play)
	return c
}

// hide blanks the controls, for a row that isn't a file at all (a folder, or
// the trailing add-folder row).
func (c *audioRowControls) hide() {
	c.play.Hide()
	c.play.OnTapped = nil
	c.restart.Hide()
	c.restart.OnTapped = nil
}

// update points the controls at the file at relPath (streamed from locs) and
// renders them against the current playback state. Called from a list row's
// update function, so it must handle being pointed at a different file than
// last time - rows are pooled and reused as the list scrolls.
func (c *audioRowControls) update(locs []syncengine.Location, relPath, filename string) {
	if !audio.CanPlay(filename) || len(locs) == 0 {
		c.hide()
		return
	}

	p := audioPlayer()
	st := p.State()
	active := st.Key == relPath

	c.play.Show()
	switch {
	case active && st.Loading:
		// Opening the file / reaching the location: the tap has registered,
		// but there's nothing to pause yet.
		c.play.SetIcon(theme.MediaPauseIcon())
		c.play.Disable()
	case active && st.Playing:
		c.play.SetIcon(theme.MediaPauseIcon())
		c.play.Enable()
	default:
		c.play.SetIcon(theme.MediaPlayIcon())
		c.play.Enable()
	}
	c.play.OnTapped = func() {
		p.Toggle(relPath, filename, audioOpener(locs, relPath))
	}

	if active && !st.Loading && (st.Playing || st.Position > 0) {
		c.restart.Show()
		c.restart.OnTapped = func() { p.Restart() }
	} else {
		c.restart.Hide()
		c.restart.OnTapped = nil
	}
}

// audioRow lays a list row's main content out with transport controls pinned
// to its trailing edge. The objects are passed to container.New explicitly
// (rather than via container.NewBorder) so their order in Objects is fixed
// and a pooled row can be picked apart again by index.
func audioRow(content fyne.CanvasObject, controls *audioRowControls) *fyne.Container {
	return container.New(layout.NewBorderLayout(nil, nil, nil, controls.box), content, controls.box)
}
