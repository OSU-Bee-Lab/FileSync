// Package drivers holds one file per supported audio format, each
// registering itself with internal/audio from an init(). Import it for side
// effects (see internal/audio/drivers/register.go's doc) to make the formats
// available; nothing here is referenced directly.
package drivers

import (
	"io"

	"github.com/hajimehoshi/go-mp3"

	"github.com/OSU-Bee-Lab/filesync/internal/audio"
)

func init() { audio.Register(mp3Driver{}) }

type mp3Driver struct{}

func (mp3Driver) Name() string { return "mp3" }

func (mp3Driver) Extensions() []string { return []string{".mp3"} }

// Open parses just the first frame header to learn the sample rate, then
// decodes on demand. go-mp3 would otherwise scan the entire file up front to
// compute its length — it skips that when handed a non-seekable reader,
// which a streamed remote read is, so streaming is what keeps this from
// pulling the whole file just to press play.
func (mp3Driver) Open(r io.Reader) (audio.Decoder, error) {
	d, err := mp3.NewDecoder(r)
	if err != nil {
		return nil, err
	}
	return &mp3Decoder{d: d}, nil
}

type mp3Decoder struct {
	d *mp3.Decoder
}

func (m *mp3Decoder) Read(p []byte) (int, error) { return m.d.Read(p) }

func (m *mp3Decoder) SampleRate() int { return m.d.SampleRate() }

// Channels is always 2: go-mp3 upmixes a mono MP3 to stereo itself.
func (m *mp3Decoder) Channels() int { return 2 }

func (m *mp3Decoder) Close() error { return nil }
