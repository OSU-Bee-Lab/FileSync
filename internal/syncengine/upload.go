package syncengine

import (
	"context"
	"path"
	"path/filepath"

	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/operations"
)

// UploadEvent is a lifecycle notification for a single file upload started
// via StartFileUpload, so a caller (e.g. a UI) can track in-flight and
// completed uploads without polling.
type UploadEvent int

const (
	UploadStarted UploadEvent = iota
	UploadDone
	UploadFailed
)

// StartFileUpload copies a single local file to relPath under dst,
// independent of any scan/batch. It's used by the recorder-offload
// feature (internal/recorder) to push each file to the cloud as soon as
// it lands locally and is verified, rather than waiting for a whole
// recorder or experiment to finish and going through a separate scan+sync
// pass.
//
// onEvent, if non-nil, is called synchronously with UploadStarted just
// before the copy begins and with UploadDone or UploadFailed (with the
// resulting error, if any) once it finishes.
func StartFileUpload(ctx context.Context, localPath string, dst Location, relPath string, onEvent func(UploadEvent, error)) error {
	if onEvent != nil {
		onEvent(UploadStarted, nil)
	}

	err := func() error {
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

	if onEvent != nil {
		if err != nil {
			onEvent(UploadFailed, err)
		} else {
			onEvent(UploadDone, nil)
		}
	}
	return err
}
