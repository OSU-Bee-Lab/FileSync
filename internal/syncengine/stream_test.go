package syncengine

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamFileReadsWholeFile(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("bee-audio-", 5000)
	writeFile(t, filepath.Join(root, "exp", "rec.mp3"), content)

	stream, loc, err := StreamFile(context.Background(), []Location{localLoc(root)}, "exp/rec.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if loc.RootPath != root {
		t.Fatalf("streamed from %q, want %q", loc.RootPath, root)
	}
	if stream.Size() != int64(len(content)) {
		t.Fatalf("Size() = %d, want %d", stream.Size(), len(content))
	}

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("streamed content differs from the file (%d bytes read)", len(got))
	}
	if stream.Offset() != int64(len(content)) {
		t.Fatalf("Offset() = %d after full read, want %d", stream.Offset(), len(content))
	}
}

// TestStreamFileIsLazy is the property the audio preview depends on: nothing
// is transferred beyond what the consumer actually reads, so pausing a
// preview a few seconds in leaves the rest of the file where it is.
func TestStreamFileIsLazy(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", 1<<20)
	writeFile(t, filepath.Join(root, "big.wav"), content)

	stream, _, err := StreamFile(context.Background(), []Location{localLoc(root)}, "big.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	buf := make([]byte, 1024)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatal(err)
	}
	if stream.Offset() != 1024 {
		t.Fatalf("Offset() = %d after a 1 KB read, want 1024 - the stream read ahead of its consumer", stream.Offset())
	}
}

// TestStreamFileResumesAtOffset covers the reopen path: a paused preview
// leaves an idle connection that a remote eventually drops, and the stream
// has to pick up at the exact byte it left off so the decoder sees one
// continuous stream. Dropping the open reader mid-file simulates that.
func TestStreamFileResumesAtOffset(t *testing.T) {
	root := t.TempDir()
	content := make([]byte, 4096)
	for i := range content {
		content[i] = byte(i % 251)
	}
	writeFile(t, filepath.Join(root, "rec.flac"), string(content))

	stream, _, err := StreamFile(context.Background(), []Location{localLoc(root)}, "rec.flac")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	first := make([]byte, 1000)
	if _, err := io.ReadFull(stream, first); err != nil {
		t.Fatal(err)
	}

	// Drop the underlying reader, as a dropped connection would.
	stream.rc.Close()
	stream.rc = nil

	rest, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(first, rest...), content) {
		t.Fatal("resumed read did not reconstruct the file byte-for-byte")
	}
}

func TestStreamFilePrefersEarlierLocation(t *testing.T) {
	preferred, fallback := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(preferred, "rec.mp3"), "preferred")
	writeFile(t, filepath.Join(fallback, "rec.mp3"), "fallback")

	// The caller ranks locs (internal/ui's normalizedLocationOrder); StreamFile
	// must honor that order rather than whichever responds first.
	stream, loc, err := StreamFile(context.Background(), []Location{localLoc(preferred), localLoc(fallback)}, "rec.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if loc.RootPath != preferred {
		t.Fatalf("streamed from %q, want the first-ranked %q", loc.RootPath, preferred)
	}
}

// TestStreamFileSkipsLocationsWithoutTheFile is the case of a file that only
// exists on some locations - normal, since a preview can be started before
// everything has synced.
func TestStreamFileSkipsLocationsWithoutTheFile(t *testing.T) {
	empty, holder := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(holder, "rec.mp3"), "here")

	stream, loc, err := StreamFile(context.Background(), []Location{localLoc(empty), localLoc(holder)}, "rec.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if loc.RootPath != holder {
		t.Fatalf("streamed from %q, want %q", loc.RootPath, holder)
	}
}

func TestStreamFileErrorsWhenNoLocationHasIt(t *testing.T) {
	if _, _, err := StreamFile(context.Background(), []Location{localLoc(t.TempDir())}, "missing.mp3"); err == nil {
		t.Fatal("expected an error when no location holds the file")
	}
}
