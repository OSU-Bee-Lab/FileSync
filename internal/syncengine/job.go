package syncengine

import (
	"context"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/cache"
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

// ProgressSnapshot is one point-in-time update on a running Job.
type ProgressSnapshot struct {
	BytesDone, BytesTotal int64
	FilesDone, FilesTotal int
	CurrentFile           string
	Status                JobStatus
	Err                   error
}

// Job is a running (or finished) copy started by StartBackup/StartDownload.
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

// StartBackup copies one whole experiment from src to dst (Location <->
// Location, mirrored under each side's own experiments/ root). expected
// should be the PreviewResult the user already confirmed, used to seed the
// progress bar's totals — the same relPath/filter that produced it is what
// actually runs here, so the set of files acted on can't drift from what
// was shown.
func StartBackup(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings, preserveModTime bool, expected PreviewResult) (*Job, <-chan ProgressSnapshot) {
	return startCopyPreserving(ctx, src.rcloneSpec(), dst.rcloneSpec(), experimentName, fset, preserveModTime, expected)
}

// StartDownload copies an arbitrary sub-path from src into destFolder,
// preserving srcRelPath's structure under destFolder (e.g. downloading
// "Luke - Zucchini/2026-06-23" into "/Downloads/foo" lands at
// "/Downloads/foo/Luke - Zucchini/2026-06-23/..."). destFolder is a raw
// local path, never a saved Location.
func StartDownload(ctx context.Context, src Location, srcRelPath string, destFolder string, fset FilterSettings, preserveModTime bool, expected PreviewResult) (*Job, <-chan ProgressSnapshot) {
	return startCopyPreserving(ctx, src.rcloneSpec(), destFolder, srcRelPath, fset, preserveModTime, expected)
}

// startCopyPreserving is the one place sync.CopyDir is called in this
// codebase. It must never be swapped for sync.Sync, which deletes
// destination-only files - see TestCopyPreserving_NeverDeletesDestinationOnlyFiles.
func startCopyPreserving(parent context.Context, srcRoot, dstRoot, relPath string, fset FilterSettings, preserveModTime bool, expected PreviewResult) (*Job, <-chan ProgressSnapshot) {
	ctx, cancel := context.WithCancel(parent)
	progress := make(chan ProgressSnapshot, 1)

	ctx, ci := fs.AddConfig(ctx)
	ci.NoUpdateModTime = preserveModTime
	ci.DryRun = false

	groupName := "expsync-job-" + random.String(8)
	ctx = accounting.WithStatsGroup(ctx, groupName)

	job := &Job{cancel: cancel}

	go func() {
		defer close(progress)

		ctx, err := withFilter(ctx, fset)
		if err != nil {
			progress <- ProgressSnapshot{Status: JobError, Err: err, FilesTotal: expected.CopyCount, BytesTotal: expected.TotalBytes}
			return
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

		emit := func(status JobStatus, err error) {
			var current string
			if transfers := stats.Transferred(); len(transfers) > 0 {
				current = transfers[len(transfers)-1].Name
			}
			progress <- ProgressSnapshot{
				BytesDone:   stats.GetBytes(),
				BytesTotal:  expected.TotalBytes,
				FilesDone:   int(stats.GetTransfers()),
				FilesTotal:  expected.CopyCount,
				CurrentFile: current,
				Status:      status,
				Err:         err,
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
