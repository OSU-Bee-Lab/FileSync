package recorder

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// bigContent builds deterministic pseudo-random bytes of size n. Copies are
// verified byte-for-byte, so the content has to be something a
// chunk-boundary bug could actually scramble - repeated filler would survive
// a misordered or duplicated chunk unnoticed.
func bigContent(seed int64, n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewSource(seed))
	r.Read(b)
	return b
}

// startOffloadTest runs StartOffload against a card directory to completion,
// returning the final status and the last error. Unlike runOffload it doesn't
// fail the test on a non-Done outcome, so the conflict and cancellation paths
// can assert on it.
func startOffloadTest(t *testing.T, ctx context.Context, card, destRoot string, driver Driver, autoDelete bool) (OffloadStatus, error) {
	t.Helper()
	_, progress := StartOffload(ctx, ctx, driver, Volume{MountPoint: card}, "REC1",
		[]string{destRoot}, "", "exp", nil, autoDelete, true, nil)

	status := OffloadRunning
	var lastErr error
	var prevDone int
	for p := range progress {
		status = p.Status
		lastErr = p.Err
		// FilesDone is an aggregate maintained across concurrent workers; a
		// decrease would mean setFile's bookkeeping had lost track of an
		// entry it had already counted.
		if p.FilesDone < prevDone {
			t.Errorf("FilesDone went backwards: %d then %d", prevDone, p.FilesDone)
		}
		prevDone = p.FilesDone
	}
	return status, lastErr
}

// TestOffloadCopiesEveryFileConcurrently is the core check on the worker
// pool: with far more files than workers, and files large enough to span
// several of smartcopy's pipeline chunks, every byte still has to arrive
// intact at the destination.
func TestOffloadCopiesEveryFileConcurrently(t *testing.T) {
	card := t.TempDir()
	destRoot := t.TempDir()

	want := make(map[string][]byte)
	for i := 0; i < 24; i++ {
		name := fmt.Sprintf("rec%02d.wav", i)
		// A couple of files straddle the 4 MiB chunk size in both
		// directions; the rest stay small so the test stays quick.
		size := 1000 + i
		switch i {
		case 3:
			size = (4 << 20) + 12345
		case 7:
			size = (9 << 20) + 7
		case 11:
			size = 4 << 20
		}
		content := bigContent(int64(i), size)
		if err := os.WriteFile(filepath.Join(card, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
		want[name] = content
	}

	status, err := startOffloadTest(t, context.Background(), card, destRoot, offloadDriver{id: "REC1"}, false)
	if status != OffloadDone {
		t.Fatalf("status = %v (err %v), want OffloadDone", status, err)
	}

	destDir := filepath.Join(destRoot, "exp", "REC1")
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(destDir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(got) != len(content) {
			t.Errorf("%s: got %d bytes, want %d", name, len(got), len(content))
			continue
		}
		for j := range content {
			if got[j] != content[j] {
				t.Errorf("%s: byte %d = %d, want %d", name, j, got[j], content[j])
				break
			}
		}
	}
}

// TestOffloadResumesPartialFileAcrossChunks covers smartcopy's resume path
// through the read-ahead pipeline: a destination holding a prefix that isn't
// a whole number of chunks must be completed, not restarted or corrupted.
func TestOffloadResumesPartialFileAcrossChunks(t *testing.T) {
	card := t.TempDir()
	destRoot := t.TempDir()

	content := bigContent(99, (5<<20)+4321)
	if err := os.WriteFile(filepath.Join(card, "rec.wav"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(destRoot, "exp", "REC1")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Deliberately not a chunk multiple, and past the first chunk.
	if err := os.WriteFile(filepath.Join(destDir, "rec.wav"), content[:(1<<20)+777], 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := startOffloadTest(t, context.Background(), card, destRoot, offloadDriver{id: "REC1"}, false)
	if status != OffloadDone {
		t.Fatalf("status = %v (err %v), want OffloadDone", status, err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "rec.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(content) {
		t.Fatalf("resumed file is %d bytes, want %d", len(got), len(content))
	}
	for j := range content {
		if got[j] != content[j] {
			t.Fatalf("resumed file differs at byte %d", j)
		}
	}
}

// TestOffloadConflictHaltsRun checks that one worker hitting a conflict still
// ends the whole run as OffloadConflict, rather than being lost among the
// other workers' successes or downgraded to a generic error by the
// cancellation it triggers.
func TestOffloadConflictHaltsRun(t *testing.T) {
	card := t.TempDir()
	destRoot := t.TempDir()

	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("rec%02d.wav", i)
		if err := os.WriteFile(filepath.Join(card, name), bigContent(int64(i), 200000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	destDir := filepath.Join(destRoot, "exp", "REC1")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same size as the source, entirely different bytes: a conflict, not a
	// partial copy.
	if err := os.WriteFile(filepath.Join(destDir, "rec05.wav"), bigContent(12345, 200000), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := startOffloadTest(t, context.Background(), card, destRoot, offloadDriver{id: "REC1"}, false)
	if status != OffloadConflict {
		t.Fatalf("status = %v (err %v), want OffloadConflict", status, err)
	}
	if err == nil {
		t.Error("conflict reported no error")
	}
}

// TestOffloadCancelStopsEveryWorker checks the cancellation path reports
// OffloadCanceled rather than surfacing one worker's aborted copy as a
// failure of that file, and that the run actually terminates.
func TestOffloadCancelStopsEveryWorker(t *testing.T) {
	card := t.TempDir()
	destRoot := t.TempDir()

	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("rec%02d.wav", i)
		if err := os.WriteFile(filepath.Join(card, name), bigContent(int64(i), 2<<20), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, progress := StartOffload(ctx, ctx, offloadDriver{id: "REC1"}, Volume{MountPoint: card}, "REC1",
		[]string{destRoot}, "", "exp", nil, false, true, nil)

	status := OffloadRunning
	canceled := false
	for p := range progress {
		status = p.Status
		if !canceled && p.FilesDone > 2 {
			canceled = true
			cancel()
		}
	}
	cancel()

	if status != OffloadCanceled && status != OffloadDone {
		t.Fatalf("status = %v, want OffloadCanceled (or OffloadDone if it beat the cancel)", status)
	}
}
