package syncengine

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/operations"
)

// NWayResolutionKind is the user's explicit decision for one FileConflict
// entry — never guessed automatically (see the never-guess invariant /
// NOTES.md). NWayIgnore is the default and leaves every copy untouched.
type NWayResolutionKind int

const (
	NWayIgnore NWayResolutionKind = iota
	// NWayOverwrite propagates one present location's copy (the winner)
	// to every other location, overwriting their mismatched copies via
	// the ordinary copy path — never a delete, since a copy to an
	// existing destination path simply replaces that path's content.
	NWayOverwrite
	// NWayRename moves the file at each target location to a new,
	// unique name (in place, same location — see RenameConflictFile),
	// preserving the content rather than discarding it. The renamed
	// file then propagates to every other location on the next scan
	// exactly like any other new file, satisfying "the renamed file
	// should also get synced everywhere" with no special-case code.
	NWayRename
	// NWayDelete permanently deletes the file at each target location.
	// This is a deliberate, narrowly-scoped exception to the app's
	// otherwise-absolute never-delete-from-remotes rule (see
	// CLAUDE.md) — an explicit, informed decision by the project
	// owner specifically for user-confirmed N-way conflict resolution,
	// where the user has already seen every conflicting copy listed
	// with its size and chosen deletion deliberately, after an
	// irreversible-action confirmation. It must never be reachable any
	// other way (e.g. as a default, or without that confirmation).
	NWayDelete
)

// NWayConflictResolution is the caller's explicit per-conflict decision.
type NWayConflictResolution struct {
	// ExpName disambiguates RelPath across experiments when a caller is
	// resolving conflicts for more than one experiment at once — not used
	// by ApplyOverwriteResolutions itself (callers apply one experiment's
	// resolutions to that experiment's NWayScanResult at a time), but kept
	// with the resolution so a UI can round-trip which experiment a
	// resolution belongs to.
	ExpName string
	RelPath string
	Kind    NWayResolutionKind
	// WinnerLocationID: for NWayOverwrite, the one location whose copy
	// becomes canonical and is propagated to every other location.
	WinnerLocationID string
	// TargetLocationIDs: for NWayRename/NWayDelete, which location(s)'
	// copy of the file the action applies to.
	TargetLocationIDs []string
	// NewName: for NWayRename, the new base filename (with extension) the
	// user chose, applied at every target location (same directory as the
	// original — only the filename changes). Falls back to
	// SuggestConflictRenameName's suggestion if empty.
	NewName string
}

// ApplyOverwriteResolutions turns every FileConflict entry with an
// NWayOverwrite resolution into a FileMissingSome entry whose only present
// location is the chosen winner, so BuildNWayTransferPlan propagates the
// winner's copy to every other location, overwriting their mismatched
// copies via the ordinary copy path. Conflicts with no resolution, or
// resolved as NWayIgnore, are left untouched (still FileConflict, so
// BuildNWayTransferPlan continues to skip them). NWayRename/NWayDelete
// resolutions are not handled here — see RenameConflictFile/
// DeleteConflictFile, which are real operations applied before a re-scan,
// not a transformation of an already-scanned result.
func ApplyOverwriteResolutions(result NWayScanResult, resolutions []NWayConflictResolution) NWayScanResult {
	winners := make(map[string]string, len(resolutions))
	for _, r := range resolutions {
		if r.Kind == NWayOverwrite && r.WinnerLocationID != "" {
			winners[r.RelPath] = r.WinnerLocationID
		}
	}
	if len(winners) == 0 {
		return result
	}

	resolved := result
	resolved.Files = make([]FileConvergencePlan, len(result.Files))
	resolved.MissingSomeCount = result.MissingSomeCount
	resolved.ConflictCount = result.ConflictCount
	for i, f := range result.Files {
		winnerID, ok := winners[f.RelPath]
		if !ok || f.Status != FileConflict {
			resolved.Files[i] = f
			continue
		}

		states := make([]FileLocationState, len(f.States))
		copy(states, f.States)
		for j := range states {
			if states[j].Location.ID != winnerID {
				states[j].Exists = false
				states[j].Size = 0
				states[j].object = nil
			}
		}
		resolved.Files[i] = FileConvergencePlan{RelPath: f.RelPath, States: states, Status: FileMissingSome}
		resolved.ConflictCount--
		resolved.MissingSomeCount++
	}
	return resolved
}

// SuggestConflictRenameName returns a default new base filename (with
// extension) for a renamed conflict copy, e.g.
// "foo (conflict copy 2026-07-09).mp3" — a starting point the user is free
// to edit before confirming, not the only option.
func SuggestConflictRenameName(relPath string) string {
	ext := path.Ext(relPath)
	base := strings.TrimSuffix(path.Base(relPath), ext)
	return fmt.Sprintf("%s (conflict copy %s)%s", base, time.Now().Format("2006-01-02"), ext)
}

// SuggestConflictRenameNameAt is SuggestConflictRenameName with the source
// location's name baked in, e.g. "foo (Lab NAS conflict copy 2026-07-09).mp3".
// Used by keep-all-versions resolutions, where every location's copy is
// renamed and must end up with a distinct name — renaming two differing
// copies to the same name would just recreate the conflict under that name.
func SuggestConflictRenameNameAt(relPath, locationName string) string {
	ext := path.Ext(relPath)
	base := strings.TrimSuffix(path.Base(relPath), ext)
	return fmt.Sprintf("%s (%s conflict copy %s)%s", base, sanitizeNameForFilename(locationName), time.Now().Format("2006-01-02"), ext)
}

// sanitizeNameForFilename strips characters from a user-chosen location name
// that are path separators or otherwise unsafe in a filename on at least one
// of the synced backends.
func sanitizeNameForFilename(name string) string {
	return strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "'", "<", "(", ">", ")", "|", "-").Replace(name)
}

// RenameConflictFile renames relPath at loc to newRelPath, in place at the
// same location (rclone MoveFile with fdst == fsrc). The original content
// is preserved under the new name — never lost — so this doesn't touch the
// never-delete invariant.
func RenameConflictFile(ctx context.Context, loc Location, relPath, newRelPath string) error {
	f, err := cache.Get(ctx, loc.rcloneSpec())
	if err != nil {
		return err
	}
	if err := operations.MoveFile(ctx, f, f, newRelPath, relPath); err != nil {
		return fmt.Errorf("renaming %s at %s: %w", relPath, loc.Name, err)
	}
	return nil
}

// DeleteConflictFile permanently deletes relPath at loc. See NWayDelete's
// doc comment: this is a deliberate, explicit, narrowly-scoped exception to
// the app's never-delete-from-remotes rule, reachable only through
// user-confirmed N-way conflict resolution.
func DeleteConflictFile(ctx context.Context, loc Location, relPath string) error {
	f, err := cache.Get(ctx, loc.rcloneSpec())
	if err != nil {
		return err
	}
	obj, err := f.NewObject(ctx, relPath)
	if err != nil {
		return fmt.Errorf("finding %s at %s: %w", relPath, loc.Name, err)
	}
	if err := operations.DeleteFile(ctx, obj); err != nil {
		return fmt.Errorf("deleting %s at %s: %w", relPath, loc.Name, err)
	}
	return nil
}
