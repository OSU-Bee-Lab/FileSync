package audio

import (
	"io"
	"sync"
)

// Read-ahead sizing: 64 chunks of 32 KB is 2 MB of *compressed* audio held
// in flight, which is roughly two minutes of a 128 kbps MP3 — far more slack
// than any cloud round trip needs, while still bounded, so a paused or
// abandoned preview never quietly pulls down a whole file.
const (
	readAheadChunk  = 32 * 1024
	readAheadChunks = 64
)

// readAhead decouples the audio device's read timing from network latency:
// a goroutine pulls from the underlying stream into a bounded queue, so a
// slow round trip to a remote is absorbed by the queue instead of starving
// the device mid-word.
//
// It is bounded on purpose — once the queue is full the filler blocks, which
// is what keeps a paused preview from continuing to transfer.
//
// It also owns the stream it reads: the filler goroutine closes it on the way
// out, and nothing else may. Close only signals the filler and returns
// immediately (it cannot wait — the filler may be parked in a read against a
// remote), so a caller that closed the stream itself would be pulling it out
// from under a live read. That crashed the app: syncengine.FileStream is a
// single-consumer stream, and closing it mid-read nil'd the reader the filler
// was using. Cancel the stream's context to get the filler out of a blocked
// read promptly, then Close here.
type readAhead struct {
	chunks chan []byte
	done   chan struct{}

	cur    []byte
	mu     sync.Mutex
	srcErr error

	closeOnce sync.Once
}

func newReadAhead(rc io.ReadCloser) *readAhead {
	ra := &readAhead{
		chunks: make(chan []byte, readAheadChunks),
		done:   make(chan struct{}),
	}
	go ra.fill(rc)
	return ra
}

func (ra *readAhead) fill(rc io.ReadCloser) {
	defer rc.Close()
	defer close(ra.chunks)
	for {
		buf := make([]byte, readAheadChunk)
		n, err := rc.Read(buf)
		if n > 0 {
			select {
			case ra.chunks <- buf[:n]:
			case <-ra.done:
				return
			}
		}
		if err != nil {
			ra.mu.Lock()
			ra.srcErr = err
			ra.mu.Unlock()
			return
		}
		select {
		case <-ra.done:
			return
		default:
		}
	}
}

func (ra *readAhead) err() error {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	return ra.srcErr
}

func (ra *readAhead) Read(p []byte) (int, error) {
	if len(ra.cur) == 0 {
		select {
		case chunk, ok := <-ra.chunks:
			if !ok {
				if err := ra.err(); err != nil {
					return 0, err
				}
				return 0, io.EOF
			}
			ra.cur = chunk
		case <-ra.done:
			return 0, io.ErrClosedPipe
		}
	}
	n := copy(p, ra.cur)
	ra.cur = ra.cur[n:]
	return n, nil
}

// Close signals the filler goroutine to stop; the filler closes the underlying
// stream as it exits. Returns without waiting for that, so pair it with
// cancelling the stream's context when the filler may be mid-read.
func (ra *readAhead) Close() error {
	ra.closeOnce.Do(func() { close(ra.done) })
	return nil
}
