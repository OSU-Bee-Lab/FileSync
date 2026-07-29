package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/oto/v3"
)

// The audio device is opened once, at a fixed format, and every recording is
// converted to it (see converter). oto permits exactly one Context per
// process, so this is a package-level singleton — but a lazy one: nothing
// touches the sound hardware until the user actually presses play.
const (
	deviceSampleRate  = 48000
	deviceChannels    = 2
	deviceBytesPerSec = deviceSampleRate * deviceChannels * 2

	// pollInterval is how often playback position is refreshed and the end
	// of a file is noticed. Position is only used to decide whether the
	// "back to start" control applies, so this doesn't need to be tight.
	pollInterval = 200 * time.Millisecond
)

var (
	deviceOnce sync.Once
	deviceCtx  *oto.Context
	deviceErr  error
)

func device() (*oto.Context, error) {
	deviceOnce.Do(func() {
		ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate:   deviceSampleRate,
			ChannelCount: deviceChannels,
			Format:       oto.FormatSignedInt16LE,
		})
		if err != nil {
			deviceErr = err
			return
		}
		<-ready
		deviceCtx = ctx
	})
	return deviceCtx, deviceErr
}

// Opener opens a file for streaming playback, positioned at its first byte.
// It is called afresh for every play and every restart, so "back to start"
// is a new stream rather than a seek — which keeps the whole pipeline
// forward-only and works identically on a local drive and a remote.
//
// The ctx handed in is cancelled when playback stops, aborting any read in
// flight.
type Opener func(ctx context.Context) (io.ReadCloser, error)

// State is everything the UI needs to draw playback controls.
type State struct {
	// Key identifies what is loaded (the UI passes a file's path). Empty
	// when nothing is loaded — including after a file plays to its end.
	Key string

	// Loading is set between pressing play and the first byte reaching the
	// audio device (opening a remote object, parsing a header).
	Loading bool

	Playing  bool
	Position time.Duration

	// Err is the last playback failure, cleared when playback next starts.
	Err error
}

// Player plays one file at a time, streamed rather than downloaded. Playback
// continues until the user pauses or stops it, or the file ends; while
// paused, the stream stays open at its current byte offset and no further
// data is pulled.
//
// Safe for concurrent use. Callers on a UI goroutine should marshal
// OnChange back onto it themselves.
type Player struct {
	mu       sync.Mutex
	onChange func()

	// gen invalidates the results of an in-flight load whose playback has
	// since been superseded (user pressed play on another file) or stopped.
	gen int

	key      string
	filename string
	opener   Opener

	st     State
	lastUI uiState

	cancel    context.CancelFunc
	src       io.Closer
	ra        *readAhead
	dec       Decoder
	oto       *oto.Player
	counter   *countingReader
	paused    bool
	stopWatch chan struct{}
}

// uiState is the subset of State the controls actually render, used to
// suppress no-op OnChange calls — the position poll ticks several times a
// second and must not repaint the file list each time.
type uiState struct {
	key     string
	loading bool
	playing bool
	atStart bool
	err     string
}

func NewPlayer() *Player { return &Player{} }

// SetOnChange registers a callback fired whenever the rendered playback
// state changes. It runs on an internal goroutine.
func (p *Player) SetOnChange(fn func()) {
	p.mu.Lock()
	p.onChange = fn
	p.mu.Unlock()
}

// State returns the current playback state.
func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

// Toggle is the play/pause button's whole behavior: pause if key is already
// playing, resume if it's paused, otherwise start it (stopping whatever else
// was playing — one preview at a time).
func (p *Player) Toggle(key, filename string, open Opener) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if key == p.key && p.st.Err == nil {
		switch {
		case p.st.Loading:
			// Still opening; ignore the tap rather than racing the load.
			return
		case p.oto == nil:
			// Nothing live to toggle - fall through to a fresh start.
		case p.paused:
			p.paused = false
			p.oto.Play()
			p.st.Playing = true
			p.notifyLocked()
			return
		default:
			p.paused = true
			p.oto.Pause()
			p.st.Playing = false
			p.notifyLocked()
			return
		}
	}
	p.startLocked(key, filename, open)
}

// Restart takes playback back to the start of the currently loaded file and
// plays from there. No-op when nothing is loaded.
func (p *Player) Restart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key == "" || p.opener == nil {
		return
	}
	p.startLocked(p.key, p.filename, p.opener)
}

// Stop ends playback and releases the stream.
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key == "" && p.oto == nil {
		return
	}
	p.teardownLocked()
	p.key = ""
	p.filename = ""
	p.opener = nil
	p.st = State{}
	p.notifyLocked()
}

func (p *Player) startLocked(key, filename string, open Opener) {
	p.teardownLocked()

	p.gen++
	gen := p.gen
	p.key = key
	p.filename = filename
	p.opener = open
	p.paused = false
	p.st = State{Key: key, Loading: true}
	p.notifyLocked()

	go p.load(gen, filename, open)
}

// load does everything that can block — opening the audio device, reaching
// the location, parsing the format header — off the caller's goroutine, then
// installs the pipeline if it hasn't been superseded in the meantime.
func (p *Player) load(gen int, filename string, open Opener) {
	drv := DriverFor(filename)
	if drv == nil {
		p.failLoad(gen, fmt.Errorf("no audio driver for %s", filename))
		return
	}
	dev, err := device()
	if err != nil {
		p.failLoad(gen, fmt.Errorf("opening the audio device: %w", err))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	rc, err := open(ctx)
	if err != nil {
		cancel()
		p.failLoad(gen, err)
		return
	}
	ra := newReadAhead(rc)
	dec, err := drv.Open(ra)
	if err != nil {
		ra.Close()
		rc.Close()
		cancel()
		p.failLoad(gen, fmt.Errorf("reading %s: %w", filename, err))
		return
	}
	counter := &countingReader{r: newConverter(dec, deviceSampleRate, deviceChannels)}
	otoPlayer := dev.NewPlayer(counter)

	p.mu.Lock()
	if gen != p.gen {
		// Superseded while loading: this pipeline was never heard, so drop it.
		p.mu.Unlock()
		otoPlayer.Close()
		dec.Close()
		ra.Close()
		rc.Close()
		cancel()
		return
	}
	p.cancel = cancel
	p.src = rc
	p.ra = ra
	p.dec = dec
	p.oto = otoPlayer
	p.counter = counter
	p.st.Loading = false
	p.st.Playing = true
	p.st.Position = 0
	otoPlayer.Play()
	stop := make(chan struct{})
	p.stopWatch = stop
	p.notifyLocked()
	p.mu.Unlock()

	go p.watch(gen, stop)
}

func (p *Player) failLoad(gen int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != p.gen {
		return
	}
	p.st.Loading = false
	p.st.Playing = false
	p.st.Err = err
	p.notifyLocked()
}

// watch refreshes position, notices the file ending, and surfaces a mid-play
// stream failure (a location that went away, say) that no user action could
// have reported.
func (p *Player) watch(gen int, stop chan struct{}) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		p.mu.Lock()
		if gen != p.gen || p.oto == nil {
			p.mu.Unlock()
			return
		}
		p.st.Position = p.positionLocked()
		if err := p.streamErrLocked(); err != nil {
			p.teardownLocked()
			p.st.Loading = false
			p.st.Playing = false
			p.st.Err = err
			p.notifyLocked()
			p.mu.Unlock()
			return
		}
		if !p.paused && !p.oto.IsPlaying() {
			// Played through to the end: release the stream and go back to
			// an idle "nothing loaded" state.
			p.teardownLocked()
			p.key = ""
			p.filename = ""
			p.opener = nil
			p.st = State{}
			p.notifyLocked()
			p.mu.Unlock()
			return
		}
		p.notifyLocked()
		p.mu.Unlock()
	}
}

// positionLocked converts bytes handed to the audio device into elapsed
// playback time, discounting what the device is still holding un-played.
func (p *Player) positionLocked() time.Duration {
	if p.counter == nil || p.oto == nil {
		return 0
	}
	played := p.counter.count() - int64(p.oto.BufferedSize())
	if played <= 0 {
		return 0
	}
	return time.Duration(played) * time.Second / time.Duration(deviceBytesPerSec)
}

// streamErrLocked reports a genuine failure from either end of the pipeline.
// io.EOF is the file simply ending, which is not an error.
func (p *Player) streamErrLocked() error {
	if p.oto != nil {
		if err := p.oto.Err(); err != nil {
			return err
		}
	}
	if p.ra != nil {
		if err := p.ra.err(); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}
	return nil
}

// teardownLocked closes the pipeline from the device end inwards and cancels
// any read in flight. Leaves key/opener alone: Restart needs them.
func (p *Player) teardownLocked() {
	if p.stopWatch != nil {
		close(p.stopWatch)
		p.stopWatch = nil
	}
	if p.oto != nil {
		p.oto.Close()
		p.oto = nil
	}
	if p.dec != nil {
		p.dec.Close()
		p.dec = nil
	}
	if p.ra != nil {
		p.ra.Close()
		p.ra = nil
	}
	if p.src != nil {
		p.src.Close()
		p.src = nil
	}
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.counter = nil
	p.paused = false
}

func (p *Player) notifyLocked() {
	cur := uiState{
		key:     p.st.Key,
		loading: p.st.Loading,
		playing: p.st.Playing,
		atStart: p.st.Position == 0,
	}
	if p.st.Err != nil {
		cur.err = p.st.Err.Error()
	}
	if cur == p.lastUI {
		return
	}
	p.lastUI = cur
	if p.onChange != nil {
		go p.onChange()
	}
}

// countingReader tracks how many PCM bytes have been pulled by the audio
// device, which is what playback position is derived from.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countingReader) count() int64 { return c.n.Load() }
