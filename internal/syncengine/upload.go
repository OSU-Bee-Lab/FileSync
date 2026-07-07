package syncengine

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/lib/random"
)

// UploadEvent is a lifecycle notification for a single file upload started
// via StartFileUpload, so a caller (e.g. a UI) can track in-flight and
// completed uploads without polling.
type UploadEvent int

const (
	// UploadQueued means a file has finished its local copy/verify and is
	// waiting for a free upload slot - it is not emitted by StartFileUpload
	// itself (which only ever runs once a slot is free) but is defined here
	// so callers that queue uploads behind a concurrency limit (see
	// internal/recorder/offload.go) can report it through the same
	// UploadProgressFunc/UploadEvent the UI already watches.
	UploadQueued UploadEvent = iota
	UploadStarted
	UploadProgress
	UploadDone
	UploadFailed
)

// UploadProgressFunc is called with a file upload's lifecycle events.
// BytesDone/BytesTotal are only meaningful for UploadProgress events (0/0
// otherwise); err is only set for UploadFailed.
type UploadProgressFunc func(ev UploadEvent, bytesDone, bytesTotal int64, err error)

// StartFileUpload copies a single local file to relPath under dst,
// independent of any scan/batch. It's used by the Sync Recorders
// feature (internal/recorder) to push each file to the cloud as soon as
// it lands locally and is verified, rather than waiting for a whole
// recorder or experiment to finish and going through a separate scan+sync
// pass.
//
// onEvent, if non-nil, is called synchronously with UploadStarted just
// before the copy begins, periodically with UploadProgress while it runs,
// and with UploadDone or UploadFailed (with the resulting error, if any)
// once it finishes.
func StartFileUpload(ctx context.Context, localPath string, dst Location, relPath string, onEvent UploadProgressFunc) error {
	var bytesTotal int64
	if info, statErr := os.Stat(localPath); statErr == nil {
		bytesTotal = info.Size()
	}

	if onEvent != nil {
		onEvent(UploadStarted, 0, bytesTotal, nil)
	}

	ctx = accounting.WithStatsGroup(ctx, "filesync-upload-"+random.String(8))
	stats := accounting.Stats(ctx)

	copyDone := make(chan error, 1)
	go func() {
		copyDone <- func() error {
			slashRel := filepath.ToSlash(relPath)
			dstDir := path.Dir(slashRel)
			if dstDir == "." {
				dstDir = ""
			}
			dstName := path.Base(slashRel)

			srcDir := filepath.Dir(localPath)
			srcName := filepath.Base(localPath)

			fsrc, err := cache.Get(ctx, srcDir)
			if err != nil {
				return err
			}
			fdst, err := cache.Get(ctx, joinSpec(dst.rcloneSpec(), dstDir))
			if err != nil {
				return err
			}

			return operations.CopyFile(ctx, fdst, fsrc, dstName, srcName)
		}()
	}()

	var err error
	if onEvent == nil {
		err = <-copyDone
	} else {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
	loop:
		for {
			select {
			case err = <-copyDone:
				break loop
			case <-ticker.C:
				onEvent(UploadProgress, stats.GetBytes(), bytesTotal, nil)
			}
		}
	}

	if onEvent != nil {
		if err != nil {
			onEvent(UploadFailed, 0, bytesTotal, err)
		} else {
			onEvent(UploadDone, bytesTotal, bytesTotal, nil)
		}
	}
	return err
}
