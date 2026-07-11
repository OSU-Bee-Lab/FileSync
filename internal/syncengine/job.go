package syncengine

import (
	"context"
	stdsync "sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/filter"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/fs/sync"
	"github.com/rclone/rclone/lib/random"
)

// copyRetries is the number of times a whole-directory copy is retried
// after a transient error (e.g. a dropped HTTP/2 connection) before it's
// reported to the UI as a failure. This mirrors the outer retry loop the
// rclone CLI runs (its --retries flag), which FileSync doesn't get for
// free since it calls sync.CopyDir directly instead of going through
// rclone's cmd.Run.
const copyRetries = 3

// copyRetriesInterval is the pause between retry attempts.
const copyRetriesInterval = 5 * time.Second

// JobStatus is the lifecycle state of a running copy.
type JobStatus int

const (
	JobRunning JobStatus = iota
	JobDone
	JobError
	JobCanceled
)

// FileProgress tracks progress of a single file.
type FileProgress struct {
	BytesDone int64
	Done      bool
	Err       error
}

// ProgressSnapshot is one point-in-time update on a running Job.
type ProgressSnapshot struct {
	BytesDone, BytesTotal int64
	FilesDone, FilesTotal int
	CurrentFile           string
	Status                JobStatus
	Err                   error
	Speed                 float64
	Files                 map[string]FileProgress

	// Retrying is true while the job is paused between attempts after a
	// transient error (see copyDirWithRetry). The job is still JobRunning
	// at this point - it isn't a failure until every retry is exhausted -
	// so callers should show this as a "will resolve itself" notice
	// rather than an error state.
	Retrying               bool
	RetryAttempt, RetryMax int
	RetryErr               error
}

// Job is a running (or finished) copy started by StartSyncExperiments/StartPullFiles.
type Job struct {
	cancel context.CancelFunc
}

// Cancel stops a running Job. rclone's fs/sync and fs/operations check
// ctx.Err() between file operations, so this stops the copy promptly
// rather than instantly, leaving whatever has already completed in place
// (never a partial file: rclone copies to a temp name and renames on
// completion).
func (j *Job) Cancel() {
	j.cancel()
}

// StartSyncExperiments copies one whole experiment from src to dst (Location <->
// Location, mirrored under each side's own experiments/ root). expected
// should be the ScanResult the user already confirmed, used to seed the
// progress bar's totals — the same relPath/filter that produced it is what
// actually runs here, so the set of files acted on can't drift from what
// was shown.
func StartSyncExperiments(ctx context.Context, src, dst Location, experimentName string, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	return startCopyPreserving(ctx, src.rcloneSpec(), dst.rcloneSpec(), experimentName, expected)
}

// StartPullFiles copies an arbitrary sub-path from src into destFolder,
// preserving srcRelPath's structure under destFolder (e.g. pulling
// "Luke - Zucchini/2026-06-23" into "/Downloads/foo" lands at
// "/Downloads/foo/Luke - Zucchini/2026-06-23/..."). destFolder is a raw
// local path, never a saved Location.
func StartPullFiles(ctx context.Context, src Location, srcRelPath string, destFolder string, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	return startCopyPreserving(ctx, src.rcloneSpec(), destFolder, srcRelPath, expected)
}

// filesFromFilter builds an rclone filter that restricts the copy to
// exactly the files the scan identified as needing transfer. This
// avoids the redundant source+destination traversal that would otherwise
// repeat the work the scan already did. Returns nil when no files
// need copying (the caller should still proceed — CopyDir is a no-op
// when nothing matches).
func filesFromFilter(expected ScanResult) *filter.Filter {
	if expected.CopyCount == 0 {
		return nil
	}
	f, _ := filter.NewFilter(nil) // no opts → can't error
	for _, entry := range expected.Entries {
		if entry.Action == ActionCopy {
			_ = f.AddFile(entry.RelPath) // path-only, can't error
		}
	}
	return f
}

// copyDirWithRetry runs sync.CopyDir, retrying on transient errors (dropped
// connections, timeouts, etc. per fserrors.ShouldRetry) up to copyRetries
// times before giving up. Files already copied in a failed attempt are left
// in place (CopyDir never deletes), so a retry just picks up whatever the
// destination is still missing - it isn't redone from scratch, though a
// retried attempt does re-list source and destination.
//
// onRetry, if non-nil, is called just before each retry sleep so a caller
// can surface a "retrying" indicator in the UI without the error being
// treated as a hard failure - the copy is still in flight, not stalled.
func copyDirWithRetry(ctx context.Context, fdst, fsrc fs.Fs, onRetry func(attempt, max int, err error)) error {
	var err error
	for attempt := 1; attempt <= copyRetries; attempt++ {
		err = sync.CopyDir(ctx, fdst, fsrc, false)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if !fserrors.ShouldRetry(err) {
			return err
		}
		if attempt == copyRetries {
			break
		}
		debugf("copy %s -> %s: transient error (attempt %d/%d), retrying in %s: %v", fsrc.Root(), fdst.Root(), attempt, copyRetries, copyRetriesInterval, err)
		if onRetry != nil {
			onRetry(attempt, copyRetries, err)
		}
		select {
		case <-time.After(copyRetriesInterval):
		case <-ctx.Done():
			return err
		}
		if onRetry != nil {
			onRetry(attempt, copyRetries, nil) // resumed: clear the "retrying" indicator
		}
	}
	return err
}

// startCopyPreserving is the one place sync.CopyDir is called in this
// codebase. It must never be swapped for sync.Sync, which deletes
// destination-only files - see TestCopyPreserving_NeverDeletesDestinationOnlyFiles.
//
// When expected contains files to copy (CopyCount > 0), the scan's
// file list is used to build a files-from filter so rclone copies only
// those files without re-scanning source or destination. This turns the
// scan→copy round-trip from O(2×listing) into O(listing + copies).
func startCopyPreserving(parent context.Context, srcRoot, dstRoot, relPath string, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	ctx, cancel := context.WithCancel(parent)
	progress := make(chan ProgressSnapshot, 1)

	// filesFromFilter returns nil exactly when the scan the user confirmed
	// found nothing to copy. Return a Done snapshot immediately in that case:
	// running CopyDir "to confirm the no-op" would re-list both trees and fall
	// back to rclone's native size+modtime equality — hashing whole audio
	// files over a cloud remote on any modtime drift, the exact cost the
	// scan's size+prefix check exists to avoid.
	cachedFilter := filesFromFilter(expected)
	if cachedFilter == nil {
		progress <- ProgressSnapshot{Status: JobDone}
		close(progress)
		return &Job{cancel: cancel}, progress
	}

	ctx, ci := fs.AddConfig(ctx)

	// Copy only the files the scan identified, via a precise files-from list
	// with destination listing disabled — the user's FilterSettings were
	// already applied during the scan, so no filter or full traversal is
	// needed here.
	ctx = filter.ReplaceConfig(ctx, cachedFilter)
	ci.NoTraverse = true

	groupName := "filesync-job-" + random.String(8)
	ctx = accounting.WithStatsGroup(ctx, groupName)

	job := &Job{cancel: cancel}

	go func() {
		defer close(progress)

		fsrc, err := cache.Get(ctx, joinSpec(srcRoot, relPath))
		if err != nil {
			progress <- ProgressSnapshot{Status: JobError, Err: err, FilesTotal: expected.CopyCount, BytesTotal: expected.TotalBytes}
			return
		}
		fdst, err := cache.Get(ctx, joinSpec(dstRoot, relPath))
		if err != nil {
			progress <- ProgressSnapshot{Status: JobError, Err: err, FilesTotal: expected.CopyCount, BytesTotal: expected.TotalBytes}
			return
		}

		debugf("copy %s -> %s: starting, %d files queued", fsrc.Root(), fdst.Root(), expected.CopyCount)
		done := make(chan error, 1)

		var retryMu stdsync.Mutex
		var retrying bool
		var retryAttempt, retryMax int
		var retryErr error
		onRetry := func(attempt, max int, err error) {
			retryMu.Lock()
			retrying, retryAttempt, retryMax, retryErr = err != nil, attempt, max, err
			retryMu.Unlock()
		}

		go func() { done <- copyDirWithRetry(ctx, fdst, fsrc, onRetry) }()

		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		stats := accounting.Stats(ctx)

		var lastBytes int64
		var lastTime = time.Now()
		var currentSpeed float64

		emit := func(status JobStatus, err error) {
			// Calculate speed
			now := time.Now()
			dur := now.Sub(lastTime)
			currentBytes := stats.GetBytes()
			if dur > 0 {
				speed := float64(currentBytes-lastBytes) / dur.Seconds()
				if lastBytes == 0 && currentBytes > 0 {
					currentSpeed = speed
				} else {
					currentSpeed = 0.8*currentSpeed + 0.2*speed
				}
			}
			lastBytes = currentBytes
			lastTime = now

			filesMap := make(map[string]FileProgress)
			for _, t := range stats.Transferred() {
				filesMap[t.Name] = FileProgress{
					BytesDone: t.Bytes,
					Done:      !t.CompletedAt.IsZero() || t.Error != nil || t.Bytes == t.Size,
					Err:       t.Error,
				}
			}

			// Transferred() above only reports completed transfers, so a
			// file actively being copied would otherwise sit at 0 bytes
			// until it finishes (the bar looked like it jumped 0%→100%).
			// RemoteStats' "transferring" list carries live in-progress
			// byte counts for files currently being copied.
			var current string
			if remoteStats, rcErr := stats.RemoteStats(false); rcErr == nil {
				if transferring, ok := remoteStats["transferring"].([]rc.Params); ok {
					for _, t := range transferring {
						name, _ := t["name"].(string)
						bytesDone, _ := t["bytes"].(int64)
						if name == "" {
							continue
						}
						filesMap[name] = FileProgress{BytesDone: bytesDone}
						current = name
					}
				}
			}

			if current != "" {
				debugf("copy %s -> %s: transferring %s (%d/%d bytes)", fsrc.Root(), fdst.Root(), current, currentBytes, expected.TotalBytes)
			}

			retryMu.Lock()
			snapRetrying, snapAttempt, snapMax, snapRetryErr := retrying, retryAttempt, retryMax, retryErr
			retryMu.Unlock()

			progress <- ProgressSnapshot{
				BytesDone:    currentBytes,
				BytesTotal:   expected.TotalBytes,
				FilesDone:    int(stats.GetTransfers()),
				FilesTotal:   expected.CopyCount,
				CurrentFile:  current,
				Status:       status,
				Err:          err,
				Speed:        currentSpeed,
				Files:        filesMap,
				Retrying:     snapRetrying,
				RetryAttempt: snapAttempt,
				RetryMax:     snapMax,
				RetryErr:     snapRetryErr,
			}
		}

		for {
			select {
			case err := <-done:
				status := JobDone
				if err != nil {
					status = JobError
					if ctx.Err() != nil {
						status = JobCanceled
					}
				}
				debugf("copy %s -> %s: finished, status=%v err=%v", fsrc.Root(), fdst.Root(), status, err)
				emit(status, err)
				return
			case <-ticker.C:
				emit(JobRunning, nil)
			}
		}
	}()

	return job, progress
}
