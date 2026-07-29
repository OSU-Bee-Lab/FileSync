package audio

import (
	"bufio"
	"encoding/binary"
	"io"
)

// converter adapts a Decoder's PCM (whatever sample rate and channel count
// the recording happens to have) to the fixed format the audio device was
// opened with. The device's format can't change once a Context exists, and
// oto allows exactly one Context per process, so conversion has to happen
// here rather than by reopening the device per file.
//
// Resampling is linear interpolation between adjacent source frames. These
// are voice recordings being auditioned to hear an announcement, not
// material for analysis — the analysis pathway works on the files
// themselves, which this never modifies. When the rates already match, the
// interpolation degenerates to passing each source frame through unchanged.
//
// Like everything else in the chain, it reads lazily: one Read pulls only
// the source frames that Read's output needed.
type converter struct {
	src             io.Reader // the Decoder, buffered (see newConverter)
	srcCh           int
	dstCh           int
	step            float64 // source frames advanced per destination frame
	cur, next       []int32 // the two source frames being interpolated between
	pos             float64 // fractional position between cur and next
	frameBuf        []byte  // scratch for reading one source frame
	pending         []byte  // generated output, consumed from pendOff onwards
	pendOff         int
	primed, drained bool
	srcEnded        bool // source exhausted; cur may still be unplayed
}

func newConverter(src Decoder, dstRate, dstCh int) *converter {
	srcCh := src.Channels()
	if srcCh < 1 {
		srcCh = 1
	}
	srcRate := src.SampleRate()
	if srcRate < 1 {
		srcRate = dstRate
	}
	return &converter{
		// Buffered: resampling walks the source one frame (a handful of
		// bytes) at a time, which shouldn't mean a decoder call per frame.
		src:      bufio.NewReaderSize(src, 32*1024),
		srcCh:    srcCh,
		dstCh:    dstCh,
		step:     float64(srcRate) / float64(dstRate),
		frameBuf: make([]byte, srcCh*2),
	}
}

// readFrame reads one interleaved source frame. It returns io.EOF only at a
// clean frame boundary; a torn final frame (the file ending mid-frame) is
// treated as the end of the stream too, since there's nothing useful left to
// play.
func (c *converter) readFrame() ([]int32, error) {
	if _, err := io.ReadFull(c.src, c.frameBuf); err != nil {
		return nil, io.EOF
	}
	out := make([]int32, c.srcCh)
	for i := range out {
		out[i] = int32(int16(binary.LittleEndian.Uint16(c.frameBuf[i*2:])))
	}
	return out, nil
}

func (c *converter) prime() error {
	first, err := c.readFrame()
	if err != nil {
		c.drained = true
		return err
	}
	c.cur = first
	// A one-frame file still gets that frame played: next falls back to cur.
	if second, err := c.readFrame(); err == nil {
		c.next = second
	} else {
		c.next = first
		c.srcEnded = true
	}
	c.primed = true
	return nil
}

// sample interpolates destination channel ch at the current position.
// Sources with fewer channels than the device are spread across it (mono
// plays out of both speakers); extra source channels beyond the device's are
// dropped.
func (c *converter) sample(ch int) int16 {
	srcIdx := ch
	if c.srcCh == 1 {
		srcIdx = 0
	} else if srcIdx >= c.srcCh {
		srcIdx = c.srcCh - 1
	}
	a := float64(c.cur[srcIdx])
	b := float64(c.next[srcIdx])
	v := a + (b-a)*c.pos
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	}
	return int16(v)
}

// advance moves the interpolation position on by one destination frame,
// pulling however many source frames that crosses. Running out of source
// doesn't drain the converter immediately: the frame just read into cur still
// has to be emitted, so srcEnded is recorded and drained only follows once
// the last frame has been played out.
func (c *converter) advance() {
	c.pos += c.step
	for c.pos >= 1 {
		c.pos--
		if c.srcEnded {
			c.drained = true
			return
		}
		c.cur = c.next
		nxt, err := c.readFrame()
		if err != nil {
			c.srcEnded = true
			c.next = c.cur
			continue
		}
		c.next = nxt
	}
}

// generateChunk appends up to n destination frames of output to pending.
func (c *converter) generateChunk(n int) {
	for i := 0; i < n; i++ {
		if c.drained {
			return
		}
		for ch := 0; ch < c.dstCh; ch++ {
			c.pending = binary.LittleEndian.AppendUint16(c.pending, uint16(c.sample(ch)))
		}
		c.advance()
	}
}

func (c *converter) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !c.primed {
		if err := c.prime(); err != nil {
			return 0, io.EOF
		}
	}
	if c.pendOff >= len(c.pending) {
		c.pending = c.pending[:0]
		c.pendOff = 0
		frames := len(p) / (c.dstCh * 2)
		if frames < 1 {
			frames = 1
		}
		c.generateChunk(frames)
		if len(c.pending) == 0 {
			return 0, io.EOF
		}
	}
	n := copy(p, c.pending[c.pendOff:])
	c.pendOff += n
	return n, nil
}
