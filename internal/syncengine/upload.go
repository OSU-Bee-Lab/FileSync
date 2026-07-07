package syncengine

import (
	"context"
	"path"
	"path/filepath"

	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/operations"
)

// StartFileUpload copies a single local file to relPath under dst,
// independent of any scan/batch. It's used by the recorder-offload
// feature (internal/recorder) to push each file to the cloud as soon as
// it lands locally and is verified, rather than waiting for a whole
// recorder or experiment to finish and going through a separate scan+sync
// pass.
func StartFileUpload(ctx context.Context, localPath string, dst Location, relPath string) error {
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
}
