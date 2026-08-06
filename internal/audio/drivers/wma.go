package drivers

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"

	"github.com/OSU-Bee-Lab/filesync/internal/audio"
)

// wmaSampleRate and wmaChannels are the PCM format every WMA is resampled to
// by ffmpeg. There's no header for this driver to parse itself the way
// wav/mp3/flac do - ffmpeg decodes the container - so asking it for a fixed
// output format is what lets SampleRate/Channels be known immediately, as
// Decoder requires, without probing the file first.
const (
	wmaSampleRate = 44100
	wmaChannels   = 2
)

// init only registers the driver when a system ffmpeg is actually on PATH.
// WMA has no Go decoder (see the package doc on internal/audio), so this is
// the only way to support it without bundling a binary - and it's opt-in per
// machine: if ffmpeg isn't there, CanPlay(".wma") simply reports false and
// the UI never offers a play button, exactly as before this driver existed.
func init() {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return
	}
	audio.Register(wmaDriver{})
}

type wmaDriver struct{}

func (wmaDriver) Name() string { return "wma" }

func (wmaDriver) Extensions() []string { return []string{".wma"} }

// Open shells out to ffmpeg and streams through it: r is piped to its stdin
// from a goroutine as bytes arrive, and decoded PCM is read back from its
// stdout, so nothing has to buffer the whole file first.
func (wmaDriver) Open(r io.Reader) (audio.Decoder, error) {
	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",
		"-i", "pipe:0",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", strconv.Itoa(wmaSampleRate),
		"-ac", strconv.Itoa(wmaChannels),
		"pipe:1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("wma: opening ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("wma: opening ffmpeg stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("wma: starting ffmpeg: %w", err)
	}

	go func() {
		io.Copy(stdin, r)
		stdin.Close()
	}()

	return &wmaDecoder{cmd: cmd, stdin: stdin, stdout: stdout, stderr: &stderr}, nil
}

type wmaDecoder struct {
	cmd    *exec.Cmd
	stdin  io.Closer
	stdout io.ReadCloser
	stderr *bytes.Buffer
	waited bool
}

func (d *wmaDecoder) Read(p []byte) (int, error) {
	n, err := d.stdout.Read(p)
	if err == io.EOF {
		if werr := d.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func (d *wmaDecoder) SampleRate() int { return wmaSampleRate }

func (d *wmaDecoder) Channels() int { return wmaChannels }

// wait reaps the process exactly once, folding stderr into the error so a
// bad or truncated WMA reports something readable instead of just "exit
// status 1".
func (d *wmaDecoder) wait() error {
	if d.waited {
		return nil
	}
	d.waited = true
	if err := d.cmd.Wait(); err != nil {
		if msg := bytes.TrimSpace(d.stderr.Bytes()); len(msg) > 0 {
			return fmt.Errorf("ffmpeg: %s", msg)
		}
		return fmt.Errorf("ffmpeg: %w", err)
	}
	return nil
}

// Close kills ffmpeg rather than letting it finish: playback commonly stops
// mid-file, and there's no reason to let it keep decoding audio nobody will
// read. The kill's own "signal: killed" error is expected, not reported.
func (d *wmaDecoder) Close() error {
	if d.cmd.Process != nil {
		d.cmd.Process.Kill()
	}
	d.stdin.Close()
	d.stdout.Close()
	d.wait()
	return nil
}
