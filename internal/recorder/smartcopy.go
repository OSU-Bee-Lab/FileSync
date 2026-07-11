// Package recorder handles offloading files from field recorders (Sony
// ICD-PX370, Olympus VN-541PC, etc.) onto local disk. smartcopy.go is a
// faithful port of the filesync project's files.py: a resumable,
// verified byte-copy so an interrupted transfer picks up where it left
// off instead of restarting, and destination files are only ever trusted
// as "complete" after their content has actually been checked against the
// source.
package recorder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// FileState mirrors the states filesync's file_states() assigns to a
// destination file relative to its source.
type FileState int

const (
	StateNonexistent FileState = iota
	StateConflict
	StatePartial
	StateComplete
)

func (s FileState) String() string {
	switch s {
	case StateNonexistent:
		return "nonexistent"
	case StateConflict:
		return "conflict"
	case StatePartial:
		return "partial"
	case StateComplete:
		return "complete"
	default:
		return "unknown"
	}
}

// DestinationTooLargeError is returned when a destination file is larger
// than the source it's supposed to be a (partial) copy of — it can never
// be reconciled by resuming, since smartcopy only ever appends.
type DestinationTooLargeError struct{ Paths []string }

func (e *DestinationTooLargeError) Error() string {
	return fmt.Sprintf("source file is smaller than destination file(s): %v", e.Paths)
}

// IrreconcilableError is returned when a partially-written destination
// file's tail can't be matched against the source, so resuming would risk
// corrupting the file.
type IrreconcilableError struct{ Path string }

func (e *IrreconcilableError) Error() string {
	return fmt.Sprintf("unable to find matchpoint of data in file %s", e.Path)
}

func getSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// readUpTo reads up to n bytes from f starting at its current position,
// returning fewer if the file doesn't have n bytes left — matching
// Python's file.read(n), which never errors on a short read.
func readUpTo(f *os.File, n int) ([]byte, error) {
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}

// nonidenticalSize returns the subset of destPaths whose size differs
// from sourcePath's.
func nonidenticalSize(sourcePath string, destPaths []string) ([]string, error) {
	sizeSource, err := getSize(sourcePath)
	if err != nil {
		return nil, err
	}

	var result []string
	for _, p := range destPaths {
		sz, err := getSize(p)
		if err != nil {
			return nil, err
		}
		if sz != sizeSource {
			result = append(result, p)
		}
	}
	return result, nil
}

// nonidenticalStart returns the subset of destPaths whose leading bytes
// don't match sourcePath's — enough to distinguish unrelated files sharing
// a size without reading the whole thing.
func nonidenticalStart(sourcePath string, destPaths []string) ([]string, error) {
	// Share syncengine's byte-prefix length so the recorder-side verify and
	// the cloud-side verify can never diverge (see PrefixCheckBytes): a
	// too-short prefix sits inside the audio metadata header and can match
	// across genuinely different recordings.
	checkSize := int(syncengine.PrefixCheckBytes)

	fileSource, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer fileSource.Close()

	chunkSource, err := readUpTo(fileSource, checkSize)
	if err != nil {
		return nil, err
	}

	var result []string
	for _, p := range destPaths {
		fileDest, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		chunkDest, err := readUpTo(fileDest, checkSize)
		fileDest.Close()
		if err != nil {
			return nil, err
		}
		// Compare only the shared leading length: a destination shorter than
		// the source is a partial copy (its size difference is caught
		// separately by nonidenticalSize), not a different-content conflict,
		// so a length difference alone must not count as a nonidentical start.
		// Mirrors syncengine's readPrefix/compareObjects shared-length check.
		shared := len(chunkSource)
		if len(chunkDest) < shared {
			shared = len(chunkDest)
		}
		if !bytes.Equal(chunkDest[:shared], chunkSource[:shared]) {
			result = append(result, p)
		}
	}
	return result, nil
}

// conflictingFiles returns the subset of destPaths that exist but whose
// content doesn't start the same way sourcePath's does — i.e. they're not
// a partial/complete copy of this source at all, just something else that
// happens to occupy that path.
func conflictingFiles(sourcePath string, destPaths []string) ([]string, error) {
	var destExists []string
	for _, p := range destPaths {
		if _, err := os.Stat(p); err == nil {
			destExists = append(destExists, p)
		}
	}
	if len(destExists) == 0 {
		return nil, nil
	}
	return nonidenticalStart(sourcePath, destExists)
}

// fileStates classifies every path in destPaths relative to sourcePath.
func fileStates(sourcePath string, destPaths []string) (map[string]FileState, error) {
	states := make(map[string]FileState, len(destPaths))

	var remaining []string
	for _, p := range destPaths {
		fi, err := os.Stat(p)
		if err != nil || fi.Size() == 0 {
			states[p] = StateNonexistent
		} else {
			remaining = append(remaining, p)
		}
	}
	if len(remaining) == 0 {
		return states, nil
	}

	conflicts, err := conflictingFiles(sourcePath, remaining)
	if err != nil {
		return nil, err
	}
	conflictSet := make(map[string]bool, len(conflicts))
	for _, p := range conflicts {
		conflictSet[p] = true
	}

	var afterConflicts []string
	for _, p := range remaining {
		if conflictSet[p] {
			states[p] = StateConflict
		} else {
			afterConflicts = append(afterConflicts, p)
		}
	}
	if len(afterConflicts) == 0 {
		return states, nil
	}

	partials, err := nonidenticalSize(sourcePath, afterConflicts)
	if err != nil {
		return nil, err
	}
	partialSet := make(map[string]bool, len(partials))
	for _, p := range partials {
		partialSet[p] = true
	}

	for _, p := range afterConflicts {
		if partialSet[p] {
			states[p] = StatePartial
		} else {
			states[p] = StateComplete
		}
	}
	return states, nil
}

// CopyProgress is a live snapshot smartcopy updates as it works, read
// concurrently by a caller's progress ticker (see offload.go). The fields
// are atomic so concurrent read/write is well-defined rather than a data
// race — the copy goroutine Stores them while the ticker Loads them.
type CopyProgress struct {
	ByteCurrent atomic.Int64
	BytesTotal  atomic.Int64
}

// smartcopy copies sourcePath to every path in destPaths, resuming from
// wherever the destination files (assumed identical to each other, since
// they're written simultaneously) already left off. Callers must have
// already checked file_states and only pass paths in the "nonexistent" or
// "partial" state — smartcopy does not itself re-derive that classification.
//
// ctx is checked between chunks of the main copy loop so a caller can abort
// a large in-flight copy promptly (e.g. the source recorder was unplugged
// mid-transfer) instead of it running to completion — or erroring out and
// getting silently retried — against a source path that may no longer point
// at the same physical device.
func smartcopy(ctx context.Context, sourcePath string, destPaths []string, progress *CopyProgress) error {
	const chunkSize = 4096 * 256

	fileSource, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer fileSource.Close()

	for _, p := range destPaths {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			f, err := os.Create(p)
			if err != nil {
				return err
			}
			f.Close()
		}
	}

	filesDest := make([]*os.File, len(destPaths))
	for i, p := range destPaths {
		f, err := os.OpenFile(p, os.O_RDWR, 0o644)
		if err != nil {
			return err
		}
		filesDest[i] = f
	}
	// On any error path, close the destination files best-effort (the copy
	// already failed, so a close error changes nothing). On the success path
	// we instead fsync and close each one explicitly at the end, surfacing
	// those errors: the caller may delete the source recorder file once
	// smartcopy returns nil, so a swallowed flush/close failure must never
	// pass a short or unflushed destination off as complete.
	copyOK := false
	defer func() {
		if copyOK {
			return
		}
		for _, f := range filesDest {
			f.Close()
		}
	}()

	sizeSource, err := fileSource.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	sizesDest := make([]int64, len(filesDest))
	for i, f := range filesDest {
		sz, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		sizesDest[i] = sz
	}

	var badDests []string
	for i, sz := range sizesDest {
		if sizeSource < sz {
			badDests = append(badDests, destPaths[i])
		}
	}
	if len(badDests) > 0 {
		return &DestinationTooLargeError{Paths: badDests}
	}

	pickupByte := sizesDest[0]
	for _, sz := range sizesDest[1:] {
		if sz < pickupByte {
			pickupByte = sz
		}
	}
	if pickupByte == sizeSource {
		return nil
	}

	if _, err := fileSource.Seek(pickupByte, io.SeekStart); err != nil {
		return err
	}
	for _, f := range filesDest {
		if _, err := f.Seek(pickupByte, io.SeekStart); err != nil {
			return err
		}
	}

	if progress != nil {
		progress.ByteCurrent.Store(pickupByte)
		progress.BytesTotal.Store(sizeSource)
	}

	const checkSize = 1000
	const attemptTolerance = 10

	if pickupByte > 0 {
		checkPrecedingStep := func() (bool, error) {
			if _, err := fileSource.Seek(-checkSize, io.SeekCurrent); err != nil {
				return false, err
			}
			stepSource, err := readUpTo(fileSource, checkSize)
			if err != nil {
				return false, err
			}

			match := true
			for _, f := range filesDest {
				if _, err := f.Seek(-checkSize, io.SeekCurrent); err != nil {
					return false, err
				}
				stepDest, err := readUpTo(f, checkSize)
				if err != nil {
					return false, err
				}
				if !bytes.Equal(stepDest, stepSource) {
					match = false
				}
			}
			return match, nil
		}

		tries := 1
		chunksMatch, err := checkPrecedingStep()
		if err != nil {
			return err
		}
		for !chunksMatch && tries < attemptTolerance {
			tries++
			if _, err := fileSource.Seek(-checkSize, io.SeekCurrent); err != nil {
				return err
			}
			for _, f := range filesDest {
				if _, err := f.Seek(-checkSize, io.SeekCurrent); err != nil {
					return err
				}
			}
			chunksMatch, err = checkPrecedingStep()
			if err != nil {
				return err
			}
		}
		// Only irreconcilable if we exhausted the attempts without ever
		// matching — a match found on the final window is still a match.
		if !chunksMatch {
			return &IrreconcilableError{Path: sourcePath}
		}
	}

	buf := make([]byte, chunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := fileSource.Read(buf)
		if n > 0 {
			for _, f := range filesDest {
				if _, werr := f.Write(buf[:n]); werr != nil {
					return werr
				}
			}
			if progress != nil {
				pos, err := fileSource.Seek(0, io.SeekCurrent)
				if err != nil {
					return err
				}
				progress.ByteCurrent.Store(pos)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	// Flush and close every destination now, before returning success, so a
	// caller that deletes the source recorder file on completion only does so
	// once the copied bytes are durably on disk. A flush/close error fails the
	// copy (the deferred best-effort close then still runs, harmlessly).
	for _, f := range filesDest {
		if err := f.Sync(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	copyOK = true
	return nil
}
