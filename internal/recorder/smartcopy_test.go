package recorder

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// patternBytes returns deterministic, non-repeating-enough content so
// tests can tell "resumed correctly" from "silently corrupted".
func patternBytes(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i%251 + 1) // +1 so it's never the zero byte used below
	}
	return buf
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestFileStates(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	sourceData := patternBytes(20000)
	writeFile(t, source, sourceData)

	nonexistentDest := filepath.Join(dir, "nonexistent.bin")

	completeDest := filepath.Join(dir, "complete.bin")
	writeFile(t, completeDest, sourceData)

	conflictDest := filepath.Join(dir, "conflict.bin")
	writeFile(t, conflictDest, bytes.Repeat([]byte{0x00}, len(sourceData)))

	partialDest := filepath.Join(dir, "partial.bin")
	writeFile(t, partialDest, sourceData[:8000])

	states, err := fileStates(source, []string{nonexistentDest, completeDest, conflictDest, partialDest})
	if err != nil {
		t.Fatalf("fileStates: %v", err)
	}

	want := map[string]FileState{
		nonexistentDest: StateNonexistent,
		completeDest:    StateComplete,
		conflictDest:    StateConflict,
		partialDest:     StatePartial,
	}
	for path, wantState := range want {
		if got := states[path]; got != wantState {
			t.Errorf("fileStates[%s] = %s, want %s", path, got, wantState)
		}
	}
}

func TestSmartcopyFreshCopy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	sourceData := patternBytes(3_000_000) // multiple chunks (chunkSize = 1MiB)
	writeFile(t, source, sourceData)

	dest := filepath.Join(dir, "dest.bin")
	progress := &CopyProgress{}
	if err := smartcopy(source, []string{dest}, progress); err != nil {
		t.Fatalf("smartcopy: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if !bytes.Equal(got, sourceData) {
		t.Fatal("dest content does not match source after fresh copy")
	}
	if progress.ByteCurrent != int64(len(sourceData)) {
		t.Errorf("progress.ByteCurrent = %d, want %d", progress.ByteCurrent, len(sourceData))
	}
}

func TestSmartcopyResumesPartialTransfer(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	sourceData := patternBytes(2_500_000)
	writeFile(t, source, sourceData)

	// Simulate an interrupted prior copy: dest has an exact prefix of
	// source, cut off mid-stream.
	dest := filepath.Join(dir, "dest.bin")
	writeFile(t, dest, sourceData[:1_200_345])

	if err := smartcopy(source, []string{dest}, nil); err != nil {
		t.Fatalf("smartcopy resume: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if !bytes.Equal(got, sourceData) {
		t.Fatal("resumed copy does not match source byte-for-byte")
	}
}

func TestSmartcopyAlreadyComplete(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	sourceData := patternBytes(10000)
	writeFile(t, source, sourceData)

	dest := filepath.Join(dir, "dest.bin")
	writeFile(t, dest, sourceData)

	// Should be a no-op: same size as source, so pickupByte == sizeSource
	// and smartcopy returns immediately without touching the file.
	if err := smartcopy(source, []string{dest}, nil); err != nil {
		t.Fatalf("smartcopy on already-complete dest: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if !bytes.Equal(got, sourceData) {
		t.Fatal("already-complete dest was modified")
	}
}

func TestSmartcopyDestinationTooLarge(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	writeFile(t, source, patternBytes(1000))

	dest := filepath.Join(dir, "dest.bin")
	writeFile(t, dest, patternBytes(2000))

	err := smartcopy(source, []string{dest}, nil)
	if _, ok := err.(*DestinationTooLargeError); !ok {
		t.Fatalf("smartcopy error = %v (%T), want *DestinationTooLargeError", err, err)
	}
}

func TestSmartcopyIrreconcilable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	sourceData := patternBytes(50000)
	writeFile(t, source, sourceData)

	// dest is the same size as a valid partial copy would be, but its
	// content has nothing in common with source anywhere near that
	// boundary, so the matchpoint search must fail.
	dest := filepath.Join(dir, "dest.bin")
	writeFile(t, dest, bytes.Repeat([]byte{0x00}, 20000))

	err := smartcopy(source, []string{dest}, nil)
	if _, ok := err.(*IrreconcilableError); !ok {
		t.Fatalf("smartcopy error = %v (%T), want *IrreconcilableError", err, err)
	}
}
