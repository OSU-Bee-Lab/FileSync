package audio

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// fakeDecoder is a Decoder over a fixed PCM buffer, for exercising the
// converter without any real audio format in the way.
type fakeDecoder struct {
	r    io.Reader
	rate int
	ch   int
}

func (f *fakeDecoder) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *fakeDecoder) SampleRate() int            { return f.rate }
func (f *fakeDecoder) Channels() int              { return f.ch }
func (f *fakeDecoder) Close() error               { return nil }

func pcm(samples ...int16) []byte {
	buf := make([]byte, 0, len(samples)*2)
	for _, s := range samples {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(s))
	}
	return buf
}

func decodePCM(t *testing.T, b []byte) []int16 {
	t.Helper()
	if len(b)%2 != 0 {
		t.Fatalf("output is %d bytes - not a whole number of 16-bit samples", len(b))
	}
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

func convertAll(t *testing.T, src []byte, rate, ch, dstRate, dstCh int) []int16 {
	t.Helper()
	c := newConverter(&fakeDecoder{r: bytes.NewReader(src), rate: rate, ch: ch}, dstRate, dstCh)
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	return decodePCM(t, got)
}

// TestConverterPassesMatchingFormatThrough is the common case for these
// recorders: already at the device's format, so conversion must be exact and
// must not drop the final frame.
func TestConverterPassesMatchingFormatThrough(t *testing.T) {
	in := []int16{100, -100, 200, -200, 300, -300, 32767, -32768}
	got := convertAll(t, pcm(in...), 48000, 2, 48000, 2)
	if len(got) != len(in) {
		t.Fatalf("got %d samples, want %d: %v", len(got), len(in), got)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("sample %d = %d, want %d (full output %v)", i, got[i], in[i], got)
		}
	}
}

// TestConverterSpreadsMonoAcrossChannels: a mono recording must come out of
// both speakers, not just the left one.
func TestConverterSpreadsMonoAcrossChannels(t *testing.T) {
	got := convertAll(t, pcm(500, -500, 1000), 48000, 1, 48000, 2)
	want := []int16{500, 500, -500, -500, 1000, 1000}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d (full output %v)", i, got[i], want[i], got)
		}
	}
}

// TestConverterUpsamplesToDeviceRate: a 24 kHz recording on a 48 kHz device
// must come out at roughly twice the length, or it would play back at double
// speed and half the pitch.
func TestConverterUpsamplesToDeviceRate(t *testing.T) {
	const frames = 100
	in := make([]int16, frames)
	for i := range in {
		in[i] = int16(i * 100)
	}
	got := convertAll(t, pcm(in...), 24000, 1, 48000, 1)
	// Two output frames per input frame, give or take the tail frame.
	if len(got) < 2*frames-2 || len(got) > 2*frames+2 {
		t.Fatalf("got %d samples from %d input frames at half the device rate, want about %d", len(got), frames, 2*frames)
	}
	// Interpolated, so monotonic input must stay monotonic.
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("output is not monotonic at %d (%d after %d) - resampling is mixing frames up", i, got[i], got[i-1])
		}
	}
}

// TestConverterDownsamplesToDeviceRate is the mirror case: a 96 kHz recording
// must be halved rather than played at double speed.
func TestConverterDownsamplesToDeviceRate(t *testing.T) {
	const frames = 100
	in := make([]int16, frames)
	for i := range in {
		in[i] = int16(i * 100)
	}
	got := convertAll(t, pcm(in...), 96000, 1, 48000, 1)
	if len(got) < frames/2-2 || len(got) > frames/2+2 {
		t.Fatalf("got %d samples from %d input frames at twice the device rate, want about %d", len(got), frames, frames/2)
	}
}

// TestConverterHandlesEmptyAndSingleFrameSources guards the priming edge
// cases - a zero-length or one-frame file must end cleanly rather than hang
// or panic.
func TestConverterHandlesEmptyAndSingleFrameSources(t *testing.T) {
	if got := convertAll(t, nil, 48000, 2, 48000, 2); len(got) != 0 {
		t.Fatalf("empty source produced %d samples", len(got))
	}
	if got := convertAll(t, pcm(1234, -1234), 48000, 2, 48000, 2); len(got) != 2 {
		t.Fatalf("single-frame source produced %d samples, want 2: %v", len(got), got)
	}
}

// TestConverterDropsExtraSourceChannels: a device opened for stereo must not
// be fed a 4-channel recording's extra channels.
func TestConverterDropsExtraSourceChannels(t *testing.T) {
	got := convertAll(t, pcm(10, 20, 30, 40, 50, 60, 70, 80), 48000, 4, 48000, 2)
	want := []int16{10, 20, 50, 60}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d (full output %v)", i, got[i], want[i], got)
		}
	}
}
