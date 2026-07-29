package drivers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/OSU-Bee-Lab/filesync/internal/audio"
)

func init() { audio.Register(wavDriver{}) }

// WAVE format tags from the fmt chunk. Extensible wraps a real format tag in
// a subformat GUID; the recorders that write extensible headers still write
// plain PCM, so it's treated as PCM with the bit depth the header states.
const (
	wavFormatPCM        = 1
	wavFormatFloat      = 3
	wavFormatExtensible = 0xFFFE
)

type wavDriver struct{}

func (wavDriver) Name() string { return "wav" }

func (wavDriver) Extensions() []string { return []string{".wav", ".wave"} }

// Open walks the RIFF chunk list forward — reading and discarding chunks it
// doesn't care about rather than seeking over them — and stops at the start
// of the data chunk. Everything after that is read on demand, so a WAV on a
// remote transfers only as far as it is listened to. WAV being uncompressed
// makes that the difference between a few hundred KB and hundreds of MB.
func (wavDriver) Open(r io.Reader) (audio.Decoder, error) {
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return nil, fmt.Errorf("reading RIFF header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a WAV file")
	}

	d := &wavDecoder{r: r}
	haveFmt := false
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, fmt.Errorf("reading WAV chunks: %w", err)
		}
		id := string(hdr[0:4])
		size := int64(binary.LittleEndian.Uint32(hdr[4:8]))

		if id == "data" {
			if !haveFmt {
				return nil, fmt.Errorf("WAV data chunk before its fmt chunk")
			}
			// A zero or maxed-out size is what streamed/never-finalized WAVs
			// carry; read to the end of the stream in that case.
			d.remaining = size
			if size <= 0 || size == math.MaxUint32 {
				d.remaining = -1
			}
			return d, nil
		}

		if id == "fmt " {
			if size < 16 {
				return nil, fmt.Errorf("WAV fmt chunk too short (%d bytes)", size)
			}
			buf := make([]byte, size)
			if _, err := io.ReadFull(r, buf); err != nil {
				return nil, fmt.Errorf("reading WAV fmt chunk: %w", err)
			}
			if err := d.parseFmt(buf); err != nil {
				return nil, err
			}
			haveFmt = true
		} else if _, err := io.CopyN(io.Discard, r, size); err != nil {
			return nil, fmt.Errorf("skipping WAV %q chunk: %w", id, err)
		}
		// Chunks are word-aligned: an odd size is followed by a pad byte.
		if size%2 == 1 {
			if _, err := io.CopyN(io.Discard, r, 1); err != nil {
				return nil, fmt.Errorf("skipping WAV chunk padding: %w", err)
			}
		}
	}
}

type wavDecoder struct {
	r io.Reader

	format int
	rate   int
	ch     int
	bps    int // bits per sample in the file

	// remaining counts down the data chunk; -1 means "until the stream ends".
	remaining int64

	srcFrame []byte // one source frame's worth, sized from the fmt chunk
	srcBlock []byte // scratch for a batch of source frames
	out      []byte // converted output, consumed from off onwards
	off      int
}

func (w *wavDecoder) parseFmt(b []byte) error {
	w.format = int(binary.LittleEndian.Uint16(b[0:2]))
	w.ch = int(binary.LittleEndian.Uint16(b[2:4]))
	w.rate = int(binary.LittleEndian.Uint32(b[4:8]))
	w.bps = int(binary.LittleEndian.Uint16(b[14:16]))

	if w.ch < 1 || w.rate < 1 {
		return fmt.Errorf("WAV header reports %d channels at %d Hz", w.ch, w.rate)
	}
	switch w.format {
	case wavFormatPCM, wavFormatExtensible:
		switch w.bps {
		case 8, 16, 24, 32:
		default:
			return fmt.Errorf("unsupported WAV bit depth %d", w.bps)
		}
	case wavFormatFloat:
		if w.bps != 32 && w.bps != 64 {
			return fmt.Errorf("unsupported WAV float bit depth %d", w.bps)
		}
	default:
		return fmt.Errorf("unsupported WAV format tag %d (compressed WAV isn't supported)", w.format)
	}
	w.srcFrame = make([]byte, w.ch*w.bps/8)
	return nil
}

func (w *wavDecoder) SampleRate() int { return w.rate }

func (w *wavDecoder) Channels() int { return w.ch }

// sample converts one source sample, at the file's bit depth and encoding,
// into the 16-bit output every driver normalizes to.
func (w *wavDecoder) sample(b []byte) int16 {
	if w.format == wavFormatFloat {
		var f float64
		if w.bps == 32 {
			f = float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
		} else {
			f = math.Float64frombits(binary.LittleEndian.Uint64(b))
		}
		v := f * 32767
		switch {
		case v > 32767:
			return 32767
		case v < -32768:
			return -32768
		}
		return int16(v)
	}
	switch w.bps {
	case 8:
		// 8-bit WAV is unsigned, centered on 128.
		return int16(int(b[0])-128) << 8
	case 16:
		return int16(binary.LittleEndian.Uint16(b))
	case 24:
		v := int32(b[0]) | int32(b[1])<<8 | int32(int8(b[2]))<<16
		return int16(v >> 8)
	default: // 32
		return int16(int32(binary.LittleEndian.Uint32(b)) >> 16)
	}
}

// wavBlockFrames is how many source frames one nextBlock call converts.
// Batching keeps this off a read-per-sample cadence without buffering any
// meaningful amount of the file (a block is a fraction of a second).
const wavBlockFrames = 2048

// nextBlock reads a batch of source frames and converts them into out,
// respecting the data chunk's declared length so trailing chunks (e.g. LIST
// metadata after the samples) are never played as noise.
func (w *wavDecoder) nextBlock() error {
	frameLen := int64(len(w.srcFrame))
	frames := int64(wavBlockFrames)
	if w.remaining >= 0 {
		avail := w.remaining / frameLen
		if avail == 0 {
			return io.EOF
		}
		if avail < frames {
			frames = avail
		}
	}
	want := frames * frameLen
	if int64(cap(w.srcBlock)) < want {
		w.srcBlock = make([]byte, want)
	}
	block := w.srcBlock[:want]

	// A short read is the file ending mid-block (or a truncated recording):
	// convert whatever whole frames arrived and let the next call report EOF.
	n, err := io.ReadFull(w.r, block)
	if w.remaining > 0 {
		w.remaining -= int64(n)
	}
	whole := int64(n) / frameLen
	if whole == 0 {
		return io.EOF
	}
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}

	step := w.bps / 8
	w.out = w.out[:0]
	w.off = 0
	for f := int64(0); f < whole; f++ {
		frame := block[f*frameLen:]
		for i := 0; i < w.ch; i++ {
			w.out = binary.LittleEndian.AppendUint16(w.out, uint16(w.sample(frame[i*step:])))
		}
	}
	return nil
}

func (w *wavDecoder) Read(p []byte) (int, error) {
	if w.off >= len(w.out) {
		if err := w.nextBlock(); err != nil {
			return 0, err
		}
	}
	n := copy(p, w.out[w.off:])
	w.off += n
	return n, nil
}

func (w *wavDecoder) Close() error { return nil }
