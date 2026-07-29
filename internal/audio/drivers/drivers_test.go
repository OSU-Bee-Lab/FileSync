package drivers

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/audio"
)

// These tests build their fixtures instead of committing audio binaries: a
// 16-bit WAV is synthesized in Go, and the other formats are transcoded from
// it with ffmpeg, which is skipped over when ffmpeg isn't installed. The
// synthesized signal is a sine tone, so a decoder that gets byte order, bit
// depth, or channel interleaving wrong shows up as a wrong sample count or a
// wrong signal level rather than as silence that still "passes".

const (
	testRate   = 44100
	testFreq   = 440.0
	testFrames = testRate // one second
	testAmp    = 12000
)

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed; skipping transcoded-format coverage")
	}
	return path
}

// writeSineWAV writes a mono 16-bit PCM WAV of a sine tone and returns the
// samples it wrote.
func writeSineWAV(t *testing.T, path string) []int16 {
	return writeSineWAVFrames(t, path, testFrames)
}

// writeSineWAVFrames is writeSineWAV with an explicit length, for the tests
// that need a file long enough to measure partial reads against.
func writeSineWAVFrames(t *testing.T, path string, frames int) []int16 {
	t.Helper()
	samples := make([]int16, frames)
	for i := range samples {
		samples[i] = int16(testAmp * math.Sin(2*math.Pi*testFreq*float64(i)/testRate))
	}

	data := make([]byte, 0, len(samples)*2)
	for _, s := range samples {
		data = binary.LittleEndian.AppendUint16(data, uint16(s))
	}

	var buf []byte
	buf = append(buf, "RIFF"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(36+len(data)))
	buf = append(buf, "WAVEfmt "...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1)          // PCM
	buf = binary.LittleEndian.AppendUint16(buf, 1)          // mono
	buf = binary.LittleEndian.AppendUint32(buf, testRate)   // sample rate
	buf = binary.LittleEndian.AppendUint32(buf, testRate*2) // byte rate
	buf = binary.LittleEndian.AppendUint16(buf, 2)          // block align
	buf = binary.LittleEndian.AppendUint16(buf, 16)         // bits per sample
	buf = append(buf, "data"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(data)))
	buf = append(buf, data...)

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return samples
}

// transcode converts src to dst with ffmpeg, passing through any extra flags.
func transcode(t *testing.T, src, dst string, extra ...string) {
	t.Helper()
	ffmpeg := requireFFmpeg(t)
	args := append([]string{"-y", "-loglevel", "error", "-i", src}, extra...)
	args = append(args, dst)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// decodeFile runs a file through whichever registered driver claims its
// extension, returning the decoded 16-bit samples.
func decodeFile(t *testing.T, path string) (samples []int16, rate, ch int) {
	t.Helper()
	drv := audio.DriverFor(filepath.Base(path))
	if drv == nil {
		t.Fatalf("no driver registered for %s", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	dec, err := drv.Open(f)
	if err != nil {
		t.Fatalf("%s driver failed to open %s: %v", drv.Name(), filepath.Base(path), err)
	}
	defer dec.Close()

	raw, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("decoding %s: %v", filepath.Base(path), err)
	}
	out := make([]int16, len(raw)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return out, dec.SampleRate(), dec.Channels()
}

func rms(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// assertSimilarSignal checks a decoded signal against the reference by level
// and length rather than sample-by-sample, for the lossy and requantized
// cases where exact equality isn't the right bar.
func assertSimilarSignal(t *testing.T, label string, got, want []int16, lengthTol, levelTol float64) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("%s decoded to nothing", label)
	}
	lengthDiff := math.Abs(float64(len(got))-float64(len(want))) / float64(len(want))
	if lengthDiff > lengthTol {
		t.Errorf("%s decoded %d samples, want about %d (%.1f%% off)", label, len(got), len(want), lengthDiff*100)
	}
	gotRMS, wantRMS := rms(got), rms(want)
	levelDiff := math.Abs(gotRMS-wantRMS) / wantRMS
	if levelDiff > levelTol {
		t.Errorf("%s decoded at RMS %.0f, want about %.0f (%.1f%% off) - suspect bit depth, byte order, or sign handling", label, gotRMS, wantRMS, levelDiff*100)
	}
}

func TestWAVDriverDecodesSelfWrittenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tone.wav")
	want := writeSineWAV(t, path)

	got, rate, ch := decodeFile(t, path)
	if rate != testRate || ch != 1 {
		t.Fatalf("decoded as %d Hz / %d channels, want %d Hz / 1 channel", rate, ch, testRate)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d - 16-bit WAV must decode bit-exactly", i, got[i], want[i])
		}
	}
}

// TestWAVDriverDecodesOtherBitDepths covers the conversions in wavDecoder.sample:
// 8-bit is unsigned and centered on 128, 24-bit is three-byte signed, and
// float samples are normalized to ±1.0 - each an easy thing to get wrong in a
// way that still produces plausible-looking output.
func TestWAVDriverDecodesOtherBitDepths(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	want := writeSineWAV(t, src)

	cases := []struct {
		name     string
		codec    string
		levelTol float64
	}{
		{"8-bit unsigned", "pcm_u8", 0.05},
		{"24-bit", "pcm_s24le", 0.01},
		{"32-bit", "pcm_s32le", 0.01},
		{"32-bit float", "pcm_f32le", 0.01},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(dir, tc.codec+".wav")
			transcode(t, src, dst, "-acodec", tc.codec)
			got, rate, ch := decodeFile(t, dst)
			if rate != testRate || ch != 1 {
				t.Fatalf("decoded as %d Hz / %d channels, want %d Hz / 1 channel", rate, ch, testRate)
			}
			assertSimilarSignal(t, tc.name+" WAV", got, want, 0.01, tc.levelTol)
		})
	}
}

// TestWAVDriverSkipsMetadataChunks: ffmpeg writes a LIST chunk ahead of the
// samples when there's metadata to store. Mistaking that for audio would play
// tag text as noise, so the chunk walk has to step over it.
func TestWAVDriverSkipsMetadataChunks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	want := writeSineWAV(t, src)

	dst := filepath.Join(dir, "tagged.wav")
	transcode(t, src, dst, "-metadata", "title=recorder setup announcement", "-metadata", "comment=WARS 2026-06-23")

	got, _, _ := decodeFile(t, dst)
	assertSimilarSignal(t, "WAV with metadata chunks", got, want, 0.01, 0.01)
}

func TestFLACDriverDecodesLosslessly(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	want := writeSineWAV(t, src)

	dst := filepath.Join(dir, "tone.flac")
	transcode(t, src, dst)

	got, rate, ch := decodeFile(t, dst)
	if rate != testRate || ch != 1 {
		t.Fatalf("decoded as %d Hz / %d channels, want %d Hz / 1 channel", rate, ch, testRate)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d - FLAC is lossless and must decode bit-exactly", i, got[i], want[i])
		}
	}
}

// TestFLACDriverInterleavesStereo: FLAC stores channels as separate
// subframes, so interleaving them is this driver's own work and worth
// checking - a stereo file that decodes to the right length but the wrong
// channel order sounds like nothing is wrong until it's played.
func TestFLACDriverInterleavesStereo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	want := writeSineWAV(t, src)

	// Upmix mono to stereo, then encode. ffmpeg attenuates an upmix by 3 dB to
	// preserve power, so the levels won't match the original - what matters
	// here is that both channels carry the same signal and that
	// de-interleaving recovers it, which is what a wrong subframe interleave
	// would break.
	dst := filepath.Join(dir, "stereo.flac")
	transcode(t, src, dst, "-ac", "2")

	got, rate, ch := decodeFile(t, dst)
	if rate != testRate || ch != 2 {
		t.Fatalf("decoded as %d Hz / %d channels, want %d Hz / 2 channels", rate, ch, testRate)
	}
	if len(got) != 2*len(want) {
		t.Fatalf("decoded %d samples, want %d", len(got), 2*len(want))
	}
	left := make([]int16, 0, len(want))
	for i := range want {
		if got[2*i] != got[2*i+1] {
			t.Fatalf("frame %d = (%d, %d) - an upmixed file's channels must be identical, so the subframes are interleaved wrongly", i, got[2*i], got[2*i+1])
		}
		left = append(left, got[2*i])
	}
	// 1/sqrt(2) of the original level, per ffmpeg's upmix; the tolerance is
	// wide enough to only catch a signal that decoded to nonsense.
	assertSimilarSignal(t, "upmixed stereo FLAC", left, want, 0.01, 0.35)
}

// TestMP3DriverDecodes uses level and length rather than exact samples: MP3 is
// lossy, and encoders pad the start of the stream.
func TestMP3DriverDecodes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	want := writeSineWAV(t, src)

	dst := filepath.Join(dir, "tone.mp3")
	transcode(t, src, dst, "-b:a", "128k")

	got, rate, ch := decodeFile(t, dst)
	if rate != testRate {
		t.Fatalf("decoded at %d Hz, want %d Hz", rate, testRate)
	}
	if ch != 2 {
		t.Fatalf("decoded %d channels, want 2 (go-mp3 always outputs stereo)", ch)
	}
	// Stereo output of a mono source: one channel's worth is half the samples.
	left := make([]int16, 0, len(got)/2)
	for i := 0; i+1 < len(got); i += 2 {
		left = append(left, got[i])
	}
	assertSimilarSignal(t, "MP3", left, want, 0.1, 0.1)
}

// TestSupportedExtensions pins what the UI will offer a play button for, so
// adding a format is a deliberate change to this list.
func TestSupportedExtensions(t *testing.T) {
	want := map[string]bool{".flac": true, ".mp3": true, ".wav": true, ".wave": true}
	got := audio.SupportedExtensions()
	if len(got) != len(want) {
		t.Fatalf("supported extensions = %v, want exactly %d entries", got, len(want))
	}
	for _, ext := range got {
		if !want[ext] {
			t.Errorf("unexpected supported extension %q", ext)
		}
	}
}

// streamOnly hides an *os.File's Seeker and ReaderAt behind a plain
// io.Reader, and counts what gets pulled through it. This is the shape the
// app actually decodes from - a lazily-fed remote read - and it differs from
// a local file in a way that matters: go-mp3, handed a Seeker, scans the
// entire file up front to compute its length.
type streamOnly struct {
	r    io.Reader
	read int64
}

func (s *streamOnly) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.read += int64(n)
	return n, err
}

// TestDriversDecodeLazilyFromNonSeekableStreams is the property the whole
// feature rests on: listening to the first moments of a recording must
// transfer the first moments of the file, not all of it. A driver that
// scanned or buffered the file up front would pass every other test here and
// still make previewing a recording off SharePoint download the whole thing.
func TestDriversDecodeLazilyFromNonSeekableStreams(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()

	// 30 seconds, so "the first two seconds" is a small fraction of the file.
	const seconds = 30
	src := filepath.Join(dir, "long.wav")
	writeSineWAVFrames(t, src, seconds*testRate)

	for _, name := range []string{"long.wav", "long.mp3", "long.flac"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if name != "long.wav" {
				transcode(t, src, path)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			stream := &streamOnly{r: f}

			dec, err := audio.DriverFor(name).Open(stream)
			if err != nil {
				t.Fatalf("opening %s from a non-seekable stream: %v", name, err)
			}
			defer dec.Close()

			// Two seconds of PCM at whatever the decoder reports.
			want := 2 * dec.SampleRate() * dec.Channels() * 2
			if _, err := io.ReadFull(dec, make([]byte, want)); err != nil {
				t.Fatalf("decoding 2s of %s: %v", name, err)
			}

			// Generous ceiling: a quarter of the file for two seconds out of
			// thirty. Anything that reads the file up front blows past it.
			max := info.Size() / 4
			if stream.read > max {
				t.Errorf("decoding 2s of %s read %d of %d bytes, want at most %d - the driver is not streaming",
					name, stream.read, info.Size(), max)
			}
		})
	}
}
