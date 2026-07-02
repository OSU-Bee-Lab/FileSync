package syncengine

import (
	"context"
	"errors"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/walk"
)

// PreviewAction describes what a copy would do to one file.
type PreviewAction int

const (
	ActionCopy PreviewAction = iota
	ActionSkipIdentical
)

// PreviewEntry is one file a dry-run inspected.
type PreviewEntry struct {
	RelPath string
	Size    int64
	Action  PreviewAction
}

// PreviewResult summarizes a dry-run: what a real copy would transfer.
type PreviewResult struct {
	Entries    []PreviewEntry
	TotalBytes int64
	CopyCount  int
	SkipCount  int
}

// PreviewBackup dry-runs syncing one whole experiment from src to dst
// (Location <-> Location, mirrored under each side's own experiments/
// root). Read-only, safe to call anytime.
func PreviewBackup(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings) (PreviewResult, error) {
	return previewCopyPreserving(ctx, src.rcloneSpec(), dst.rcloneSpec(), experimentName, fset)
}

// PreviewDownload dry-runs copying an arbitrary sub-path (any depth: a
// whole experiment, one deployment date, one recorder directory, even a
// single file) from src into destFolder, preserving srcRelPath's structure
// under destFolder rather than flattening. destFolder is a raw local path
// (from an OS folder picker), never a saved Location.
func PreviewDownload(ctx context.Context, src Location, srcRelPath string, destFolder string, fset FilterSettings) (PreviewResult, error) {
	return previewCopyPreserving(ctx, src.rcloneSpec(), destFolder, srcRelPath, fset)
}

// previewCopyPreserving is the shared dry-run implementation behind both
// PreviewBackup and PreviewDownload: it walks <srcRoot>/<relPath> (through
// fset's filter) and diffs each file against <dstRoot>/<relPath>, without
// transferring anything.
func previewCopyPreserving(ctx context.Context, srcRoot, dstRoot, relPath string, fset FilterSettings) (PreviewResult, error) {
	ctx, err := withFilter(ctx, fset)
	if err != nil {
		return PreviewResult{}, err
	}

	fsrc, err := cache.Get(ctx, joinSpec(srcRoot, relPath))
	if err != nil {
		return PreviewResult{}, err
	}
	fdst, err := cache.Get(ctx, joinSpec(dstRoot, relPath))
	if err != nil {
		return PreviewResult{}, err
	}

	objs, _, err := walk.GetAll(ctx, fsrc, "", false, -1)
	if err != nil {
		return PreviewResult{}, err
	}

	var result PreviewResult
	for _, srcObj := range objs {
		relFile := srcObj.Remote()
		action := ActionCopy

		dstObj, err := fdst.NewObject(ctx, relFile)
		switch {
		case err == nil:
			if operations.Equal(ctx, srcObj, dstObj) {
				action = ActionSkipIdentical
			}
		case errors.Is(err, fs.ErrorObjectNotFound):
			// not present at dest yet - stays ActionCopy
		default:
			return PreviewResult{}, err
		}

		result.Entries = append(result.Entries, PreviewEntry{
			RelPath: relFile,
			Size:    srcObj.Size(),
			Action:  action,
		})
		if action == ActionCopy {
			result.CopyCount++
			result.TotalBytes += srcObj.Size()
		} else {
			result.SkipCount++
		}
	}
	return result, nil
}
