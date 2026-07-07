package syncengine

import (
	"context"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/filter"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/fs/sync"
	"github.com/rclone/rclone/lib/random"
)

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
func StartSyncExperiments(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings, preserveModTime bool, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	return startCopyPreserving(ctx, src.rcloneSpec(), dst.rcloneSpec(), experimentName, fset, preserveModTime, expected)
}

// StartPullFiles copies an arbitrary sub-path from src into destFolder,
// preserving srcRelPath's structure under destFolder (e.g. pulling
// "Luke - Zucchini/2026-06-23" into "/Downloads/foo" lands at
// "/Downloads/foo/Luke - Zucchini/2026-06-23/..."). destFolder is a raw
// local path, never a saved Location.
func StartPullFiles(ctx context.Context, src Location, srcRelPath string, destFolder string, fset FilterSettings, preserveModTime bool, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	return startCopyPreserving(ctx, src.rcloneSpec(), destFolder, srcRelPath, fset, preserveModTime, expected)
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

// startCopyPreserving is the one place sync.CopyDir is called in this
// codebase. It must never be swapped for sync.Sync, which deletes
// destination-only files - see TestCopyPreserving_NeverDeletesDestinationOnlyFiles.
//
// When expected contains files to copy (CopyCount > 0), the scan's
// file list is used to build a files-from filter so rclone copies only
// those files without re-scanning source or destination. This turns the
// scan→copy round-trip from O(2×listing) into O(listing + copies).
func startCopyPreserving(parent context.Context, srcRoot, dstRoot, relPath string, fset FilterSettings, preserveModTime bool, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	ctx, cancel := context.WithCancel(parent)
	progress := make(chan ProgressSnapshot, 1)

	ctx, ci := fs.AddConfig(ctx)
	ci.NoUpdateModTime = preserveModTime
	ci.DryRun = false

	// When we have cached scan results, skip the full traversal and
	// copy only the files the scan identified. This replaces any
	// FilterSettings-based filter (which was already applied during the
	// scan) with a precise files-from list and disables destination
	// listing.
	cachedFilter := filesFromFilter(expected)
	if cachedFilter != nil {
		ctx = filter.ReplaceConfig(ctx, cachedFilter)
		ci.NoTraverse = true
	}

	groupName := "filesync-job-" + random.String(8)
	ctx = accounting.WithStatsGroup(ctx, groupName)

	job := &Job{cancel: cancel}

	go func() {
		defer close(progress)

		// Only apply the user's FilterSettings when we aren't using
		// cached scan results (i.e. the scan found nothing to
		// copy, so CopyDir will confirm the no-op via a full scan).
		if cachedFilter == nil {
			var err error
			ctx, err = withFilter(ctx, fset)
			if err != nil {
				progress <- ProgressSnapshot{Status: JobError, Err: err, FilesTotal: expected.CopyCount, BytesTotal: expected.TotalBytes}
				return
			}
		}

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

		done := make(chan error, 1)
		go func() { done <- sync.CopyDir(ctx, fdst, fsrc, false) }()

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

			progress <- ProgressSnapshot{
				BytesDone:   currentBytes,
				BytesTotal:  expected.TotalBytes,
				FilesDone:   int(stats.GetTransfers()),
				FilesTotal:  expected.CopyCount,
				CurrentFile: current,
				Status:      status,
				Err:         err,
				Speed:       currentSpeed,
				Files:       filesMap,
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
				emit(status, err)
				return
			case <-ticker.C:
				emit(JobRunning, nil)
			}
		}
	}()

	return job, progress
}
