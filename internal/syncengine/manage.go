package syncengine

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/walk"
)

// ManageEntry is one file found while recursively listing a subtree for the
// Manage Files tool (rename/move/merge/delete previews).
type ManageEntry struct {
	RelPath string // relative to the Location root, forward-slash separated
	Size    int64
}

// ListRecursive walks every file under <loc>/<relPath>, one true recursive
// listing — unlike ListChildren (browser.go), which only ever looks one
// level deep. Used to build move/merge and delete previews, where the tool
// needs to know about every file that will actually move or be removed, not
// just the immediate children of the selected path.
func ListRecursive(ctx context.Context, loc Location, relPath string) ([]ManageEntry, error) {
	f, err := cache.Get(ctx, joinSpec(loc.rcloneSpec(), relPath))
	if err != nil {
		return nil, err
	}
	var out []ManageEntry
	err = walk.ListR(ctx, f, "", false, -1, walk.ListObjects, func(entries fs.DirEntries) error {
		for _, e := range entries {
			if obj, ok := e.(fs.Object); ok {
				out = append(out, ManageEntry{RelPath: path.Join(relPath, obj.Remote()), Size: obj.Size()})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out, nil
}

// PlannedMove is one file's source and destination path within a single
// Location's rename/move/merge operation.
type PlannedMove struct {
	SrcRelPath string
	DstRelPath string
}

// MovePlan is the full set of per-file moves a rename/move/merge would
// perform at one Location, plus any destination paths that already exist
// (collisions the caller must resolve before applying).
type MovePlan struct {
	Moves      []PlannedMove
	Collisions []string // DstRelPath values that already exist
}

// PlanMove lists everything under srcRelPath and computes each file's new
// path under dstRelPath, preserving the relative structure beneath
// srcRelPath — the same computation whether the destination is a sibling
// name (rename) or an existing directory with its own contents (move/merge):
// only the prefix changes, so there's a single code path for both. Each
// computed destination is checked for existence at the Location; anything
// already present is reported as a collision rather than silently decided.
func PlanMove(ctx context.Context, loc Location, srcRelPath, dstRelPath string) (MovePlan, error) {
	entries, err := ListRecursive(ctx, loc, srcRelPath)
	if err != nil {
		return MovePlan{}, err
	}

	f, err := cache.Get(ctx, loc.rcloneSpec())
	if err != nil {
		return MovePlan{}, err
	}

	var plan MovePlan
	for _, e := range entries {
		suffix := strings.TrimPrefix(strings.TrimPrefix(e.RelPath, srcRelPath), "/")
		dst := path.Join(dstRelPath, suffix)
		plan.Moves = append(plan.Moves, PlannedMove{SrcRelPath: e.RelPath, DstRelPath: dst})
		if _, err := f.NewObject(ctx, dst); err == nil {
			plan.Collisions = append(plan.Collisions, dst)
		}
	}
	return plan, nil
}

// CollisionResolution is the user's explicit per-path decision for a
// colliding destination in a MovePlan — never guessed automatically.
type CollisionResolution int

const (
	// CollisionSkip leaves the source file where it is; it is not moved.
	CollisionSkip CollisionResolution = iota
	// CollisionOverwrite moves the source file onto the existing
	// destination, replacing its content.
	CollisionOverwrite
	// CollisionKeepBoth renames the incoming file (appending a numbered
	// suffix, e.g. "foo (2).mp3") before moving it, so neither copy is lost.
	CollisionKeepBoth
)

// ApplyMove executes a MovePlan at loc. resolutions supplies the decision
// for every path in plan.Collisions (by DstRelPath); any collision without
// an explicit resolution is treated as CollisionSkip, never as an implicit
// overwrite.
func ApplyMove(ctx context.Context, loc Location, plan MovePlan, resolutions map[string]CollisionResolution) error {
	f, err := cache.Get(ctx, loc.rcloneSpec())
	if err != nil {
		return err
	}
	collides := make(map[string]bool, len(plan.Collisions))
	for _, c := range plan.Collisions {
		collides[c] = true
	}
	for _, m := range plan.Moves {
		dst := m.DstRelPath
		if collides[dst] {
			switch resolutions[dst] {
			case CollisionSkip:
				continue
			case CollisionKeepBoth:
				dst = uniqueDstPath(ctx, f, dst)
			case CollisionOverwrite:
				// fall through: MoveFile onto an existing dst replaces it.
			}
		}
		if err := operations.MoveFile(ctx, f, f, dst, m.SrcRelPath); err != nil {
			return fmt.Errorf("moving %s to %s at %s: %w", m.SrcRelPath, dst, loc.Name, err)
		}
	}
	return nil
}

// uniqueDstPath appends a numbered suffix to relPath's base name until it no
// longer collides with an existing object at f, e.g. "foo.mp3" -> "foo
// (2).mp3" -> "foo (3).mp3".
func uniqueDstPath(ctx context.Context, f fs.Fs, relPath string) string {
	ext := path.Ext(relPath)
	base := strings.TrimSuffix(relPath, ext)
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, n, ext)
		if _, err := f.NewObject(ctx, candidate); err != nil {
			return candidate
		}
	}
}

// DeletePlan is every file that a delete of relPath would remove at one
// Location, for a final-state preview before the user confirms.
type DeletePlan struct {
	Entries []ManageEntry
}

// PlanDelete lists everything under relPath at loc, for preview purposes.
func PlanDelete(ctx context.Context, loc Location, relPath string) (DeletePlan, error) {
	entries, err := ListRecursive(ctx, loc, relPath)
	if err != nil {
		return DeletePlan{}, err
	}
	return DeletePlan{Entries: entries}, nil
}

// ApplyDelete permanently deletes relPath (file or directory) at loc. See
// CLAUDE.md: this is a deliberate, narrowly-scoped exception to the app's
// never-delete-from-remotes rule, reachable only through the dev-gated
// Manage Files tool after the user has typed the exact relative path,
// previewed every file it will remove, and confirmed an irreversible-action
// prompt.
func ApplyDelete(ctx context.Context, loc Location, relPath string) error {
	f, err := cache.Get(ctx, loc.rcloneSpec())
	if err != nil {
		return err
	}
	if obj, err := f.NewObject(ctx, relPath); err == nil {
		if err := operations.DeleteFile(ctx, obj); err != nil {
			return fmt.Errorf("deleting %s at %s: %w", relPath, loc.Name, err)
		}
		return nil
	}
	dirFs, err := cache.Get(ctx, joinSpec(loc.rcloneSpec(), relPath))
	if err != nil {
		return err
	}
	if err := operations.Purge(ctx, dirFs, ""); err != nil {
		return fmt.Errorf("deleting %s at %s: %w", relPath, loc.Name, err)
	}
	return nil
}
