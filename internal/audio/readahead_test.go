package audio

import (
	"bytes"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// countingSource reports how much has been pulled out of it, standing in for
// a network stream whose read volume is what we care about bounding.
type countingSource struct {
	data []byte
	off  int
	read atomic.Int64
}

func (s *countingSource) Read(p []byte) (int, error) {
	if s.off >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.off:])
	s.off += n
	s.read.Add(int64(n))
	return n, nil
}

// waitForStable returns once the source has stopped being read for a beat,
// i.e. the read-ahead has filled up and blocked.
func waitForStable(t *testing.T, s *countingSource) int64 {
	t.Helper()
	last := int64(-1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if n := s.read.Load(); n == last {
			return n
		} else {
			last = n
		}
	}
	t.Fatal("read-ahead never stopped reading - it is not bounded")
	return 0
}

// TestReadAheadIsBounded is the property that makes pausing a preview safe: a
// consumer that stops reading (paused playback) must leave the rest of the
// file untransferred, not quietly pull it all down in the background.
func TestReadAheadIsBounded(t *testing.T) {
	const total = 64 << 20 // 64 MB, far larger than the read-ahead
	src := &countingSource{data: make([]byte, total)}

	ra := newReadAhead(src)
	defer ra.Close()

	buffered := waitForStable(t, src)
	if buffered == 0 {
		t.Fatal("read-ahead never read anything")
	}
	// The queue plus one chunk in flight is the ceiling; allow a little slack
	// but stay far below the file size.
	max := int64(readAheadChunk * (readAheadChunks + 2))
	if buffered > max {
		t.Fatalf("read-ahead buffered %d bytes with no consumer, want at most %d", buffered, max)
	}
	if buffered >= total {
		t.Fatal("read-ahead pulled the whole file down with no consumer")
	}
}

// TestReadAheadResumesAfterConsumption is the resume side of the same
// property: once the consumer starts reading again, the filler picks back up.
func TestReadAheadResumesAfterConsumption(t *testing.T) {
	const total = 8 << 20
	src := &countingSource{data: make([]byte, total)}

	ra := newReadAhead(src)
	defer ra.Close()

	before := waitForStable(t, src)
	buf := make([]byte, readAheadChunk*4)
	if _, err := io.ReadFull(ra, buf); err != nil {
		t.Fatal(err)
	}
	after := waitForStable(t, src)
	if after <= before {
		t.Fatalf("source read %d bytes before consuming and %d after - the filler did not resume", before, after)
	}
}

func TestReadAheadDeliversEverythingInOrder(t *testing.T) {
	want := make([]byte, 300*1024)
	for i := range want {
		want[i] = byte(i % 253)
	}
	ra := newReadAhead(bytes.NewReader(want))
	defer ra.Close()

	got, err := io.ReadAll(ra)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, want %d, and contents differ", len(got), len(want))
	}
}
