package syncengine

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

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
// Location's rename/move/merge operation, plus its size (from the same
// recursive listing PlanMove already had to do, so callers that want sizes
// for a preview don't need a second one).
type PlannedMove struct {
	SrcRelPath string
	DstRelPath string
	Size       int64
}

// MovePlan is the full set of per-file moves a rename/move/merge would
// perform at one Location, plus any destination paths that already exist
// (collisions the caller must resolve before applying).
type MovePlan struct {
	Moves      []PlannedMove
	Collisions []string // DstRelPath values that already exist
	// SrcRoot is the srcRelPath PlanMove was called with. ApplyMove uses it
	// afterward to clean up any directories left empty by moving every file
	// out of them - rclone remotes generally have no real "rename a
	// directory" operation (a directory is just an inferred prefix of its
	// objects' paths), so a move is always applied file-by-file, and the
	// now-pathless source directories don't disappear on their own.
	SrcRoot string
}

// PlanMove lists everything under srcRelPath and computes each file's new
// path under dstRelPath, preserving the relative structure beneath
// srcRelPath — the same computation whether the destination is a sibling
// name (rename) or an existing directory with its own contents (move/merge):
// only the prefix changes, so there's a single code path for both. Every
// computed destination is checked against a single recursive listing of
// dstRelPath (below) rather than one existence lookup per file - on a
// remote like SharePoint/OneDrive, each such lookup is its own network
// round trip, so doing it per file turns a rename of a few hundred files
// into a few hundred sequential API calls before anything even moves.
func PlanMove(ctx context.Context, loc Location, srcRelPath, dstRelPath string) (MovePlan, error) {
	// The source and destination listings are independent - run them
	// concurrently rather than paying two sequential recursive-listing
	// round trips on a slow remote.
	var entries, dstEntries []ManageEntry
	var srcErr, dstErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		entries, srcErr = ListRecursive(ctx, loc, srcRelPath)
	}()
	go func() {
		defer wg.Done()
		// dstRelPath may not exist yet (the common case for a rename/move)
		// - that just means nothing collides, not an error.
		dstEntries, dstErr = ListRecursive(ctx, loc, dstRelPath)
		if dstErr != nil && errors.Is(dstErr, fs.ErrorDirNotFound) {
			dstEntries, dstErr = nil, nil
		}
	}()
	wg.Wait()
	if srcErr != nil {
		return MovePlan{}, srcErr
	}
	if dstErr != nil {
		return MovePlan{}, dstErr
	}

	existing := make(map[string]bool, len(dstEntries))
	for _, e := range dstEntries {
		existing[e.RelPath] = true
	}

	plan := MovePlan{SrcRoot: srcRelPath}
	for _, e := range entries {
		suffix := strings.TrimPrefix(strings.TrimPrefix(e.RelPath, srcRelPath), "/")
		dst := path.Join(dstRelPath, suffix)
		plan.Moves = append(plan.Moves, PlannedMove{SrcRelPath: e.RelPath, DstRelPath: dst, Size: e.Size})
		if existing[dst] {
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
// overwrite. Moves run up to fs.Config.Transfers at a time (the same
// concurrency the rest of the app uses for copies - see SetTransfers):
// rclone remotes have no bulk "rename a directory" call, so this is applied
// one MoveFile per file, and on a remote like SharePoint/OneDrive each of
// those is its own network round trip - running them one at a time turns a
// few-hundred-file rename into a few-hundred-round-trip wait even though
// the backend moves each file server-side (no re-upload). Once every file
// has moved, it also removes any directory under plan.SrcRoot (including
// SrcRoot itself) left with nothing in it.
func ApplyMove(ctx context.Context, loc Location, plan MovePlan, resolutions map[string]CollisionResolution) error {
	f, err := cache.Get(ctx, loc.rcloneSpec())
	if err != nil {
		return err
	}
	collides := make(map[string]bool, len(plan.Collisions))
	for _, c := range plan.Collisions {
		collides[c] = true
	}

	workers := fs.GetConfig(ctx).Transfers
	if workers < 1 {
		workers = DefaultTransfers
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

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
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := operations.MoveFile(ctx, f, f, dst, m.SrcRelPath); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("moving %s to %s at %s: %w", m.SrcRelPath, dst, loc.Name, err)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	if plan.SrcRoot != "" {
		if err := operations.Rmdirs(ctx, f, plan.SrcRoot, false); err != nil {
			return fmt.Errorf("cleaning up empty directories under %s at %s: %w", plan.SrcRoot, loc.Name, err)
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
