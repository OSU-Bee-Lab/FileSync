// Package audio plays back experiment recordings streamed straight from a
// Location — local or remote — so a researcher can hear the metadata that
// gets announced into the microphone when a recorder is first set up,
// without pulling the file down first.
//
// Nothing here downloads a whole file. A Decoder reads from an io.Reader
// that is fed lazily (internal/syncengine.FileStream), and the audio device
// pulls on that chain only while playback is actually running, so stopping
// or pausing stops the transfer at whatever byte it had reached.
//
// Format support is a driver registry, mirroring internal/recorder's: one
// file per format under internal/audio/drivers, registered from an init()
// there. Adding a format is a matter of dropping in a new file — nothing in
// this package changes.
//
// MP3, FLAC and WAV are supported. WMA is deliberately not: there is no Go
// decoder for it, and macOS dropped WMA support too, so it would mean
// depending on an external ffmpeg binary. That's a separate decision from
// this feature — a driver for it drops in here when it's made.
package audio

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
)

// Decoder turns one audio format's byte stream into raw PCM. Read yields
// interleaved little-endian signed 16-bit samples (the one format every
// driver normalizes to, so the player only has to deal with sample rate and
// channel count), and must keep reading from its source as it goes rather
// than decoding the input up front — the whole point is that a stopped
// playback stops the transfer.
type Decoder interface {
	io.Reader

	// SampleRate and Channels describe the PCM coming out of Read. Both
	// must be valid as soon as the Decoder is constructed (i.e. the driver
	// reads far enough into the stream to parse its header in Open), and
	// must not change afterwards.
	SampleRate() int
	Channels() int

	// Close releases the decoder. It does not close the underlying source —
	// the caller owns that.
	Close() error
}

// Driver decodes one audio format. Deliberately minimal: a format is an
// extension list plus a way to wrap a byte stream in a Decoder.
type Driver interface {
	// Name identifies the driver, e.g. "mp3".
	Name() string

	// Extensions are the lowercase filename extensions, leading dot
	// included (".mp3"), this driver claims.
	Extensions() []string

	// Open wraps r — a lazily-fed stream positioned at the first byte of the
	// file — in a Decoder, reading only as far as it needs to parse the
	// format's header.
	Open(r io.Reader) (Decoder, error)
}

var (
	mu      sync.RWMutex
	byExt   = make(map[string]Driver)
	drivers []Driver
)

// Register adds d to the registry. Called from a driver file's init(); it
// panics on a duplicate extension, since that's a build-time mistake rather
// than a runtime condition.
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	for _, ext := range d.Extensions() {
		ext = strings.ToLower(ext)
		if existing, dup := byExt[ext]; dup {
			panic(fmt.Sprintf("audio: extension %s claimed by both %s and %s", ext, existing.Name(), d.Name()))
		}
		byExt[ext] = d
	}
	drivers = append(drivers, d)
}

// DriverFor returns the driver claiming filename's extension, or nil if the
// format isn't supported. Matching is on extension alone: these are the
// lab's own recorder outputs with conventional extensions, and sniffing
// magic bytes would mean a network round trip per file just to decide
// whether to draw a play button.
func DriverFor(filename string) Driver {
	ext := strings.ToLower(path.Ext(filename))
	if ext == "" {
		return nil
	}
	mu.RLock()
	defer mu.RUnlock()
	return byExt[ext]
}

// CanPlay reports whether filename names a format some registered driver can
// decode — what the UI asks before offering a play button.
func CanPlay(filename string) bool { return DriverFor(filename) != nil }

// SupportedExtensions lists every claimed extension, sorted, for user-facing
// text about what can be previewed.
func SupportedExtensions() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(byExt))
	for ext := range byExt {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}
