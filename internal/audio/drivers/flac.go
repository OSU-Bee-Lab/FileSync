package drivers

import (
	"encoding/binary"
	"io"

	"github.com/mewkiz/flac"

	"github.com/OSU-Bee-Lab/filesync/internal/audio"
)

func init() { audio.Register(flacDriver{}) }

type flacDriver struct{}

func (flacDriver) Name() string { return "flac" }

func (flacDriver) Extensions() []string { return []string{".flac"} }

// Open reads the FLAC signature and STREAMINFO block only; audio frames are
// parsed one at a time as Read asks for them.
func (flacDriver) Open(r io.Reader) (audio.Decoder, error) {
	st, err := flac.New(r)
	if err != nil {
		return nil, err
	}
	return &flacDecoder{
		st:   st,
		rate: int(st.Info.SampleRate),
		ch:   int(st.Info.NChannels),
		bps:  int(st.Info.BitsPerSample),
	}, nil
}

type flacDecoder struct {
	st   *flac.Stream
	rate int
	ch   int
	bps  int

	// pending holds one decoded frame's worth of interleaved int16, consumed
	// from off onwards. FLAC decodes a whole frame at a time, so output has
	// to be buffered between Reads.
	pending []byte
	off     int
}

func (f *flacDecoder) SampleRate() int { return f.rate }

func (f *flacDecoder) Channels() int { return f.ch }

// scale converts one sample from the file's bit depth to the 16-bit output
// every driver normalizes to.
func (f *flacDecoder) scale(s int32) int16 {
	switch {
	case f.bps > 16:
		s >>= uint(f.bps - 16)
	case f.bps < 16:
		s <<= uint(16 - f.bps)
	}
	switch {
	case s > 32767:
		return 32767
	case s < -32768:
		return -32768
	}
	return int16(s)
}

// nextFrame decodes one FLAC frame and interleaves its per-channel subframes
// into pending.
func (f *flacDecoder) nextFrame() error {
	frame, err := f.st.ParseNext()
	if err != nil {
		return err
	}
	if len(frame.Subframes) == 0 {
		return io.EOF
	}
	n := len(frame.Subframes[0].Samples)
	f.pending = f.pending[:0]
	f.off = 0
	for i := 0; i < n; i++ {
		for _, sub := range frame.Subframes {
			if i >= len(sub.Samples) {
				continue
			}
			f.pending = binary.LittleEndian.AppendUint16(f.pending, uint16(f.scale(sub.Samples[i])))
		}
	}
	return nil
}

func (f *flacDecoder) Read(p []byte) (int, error) {
	for f.off >= len(f.pending) {
		if err := f.nextFrame(); err != nil {
			return 0, err
		}
	}
	n := copy(p, f.pending[f.off:])
	f.off += n
	return n, nil
}

// Close is a no-op: flac.Stream.Close would close the underlying stream,
// which this decoder doesn't own (see audio.Decoder).
func (f *flacDecoder) Close() error { return nil }
