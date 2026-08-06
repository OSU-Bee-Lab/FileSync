package recorder

import "path"

// TimestampGroup is one candidate recorder directory found by
// GroupTimestampFiles, plus whichever registered driver's TimestampParser
// recognized its files' naming pattern.
type TimestampGroup struct {
	// RecorderID is the recorder directory's own name - per SCHEMA.md,
	// recorder directories are named only by the recorder ID.
	RecorderID string
	// RelDir is the recorder directory's path exactly as it appeared in the
	// relPaths GroupTimestampFiles was given (e.g. relative to a Location
	// root, if that's what the caller's paths were relative to).
	RelDir string
	Parser TimestampParser
	// Files' DestRelPath is relative to RelDir - just the filename when
	// Parser.RecorderDirDepth is 0 (matching
	// TimestampParser.RenameForTimestamp's dir=="." case), or nested under
	// a device category subdirectory (e.g. Olympus's "MUSIC/...") when it's
	// greater, exactly as SourceFiles produces for that driver.
	Files []SourceFile
}

// ancestorDir splits dir (a "path"-style, forward-slash directory) into the
// ancestor n levels up and the trailing n components removed to reach it,
// e.g. ancestorDir("a/b/c", 1) -> ("a/b", "c"), ancestorDir("a/b/c", 2) ->
// ("a", "b/c"). Used by GroupTimestampFiles to find a file's actual recorder
// directory when the driver's files live a fixed number of levels below it
// (see TimestampParser.RecorderDirDepth) - trailer is then prepended back
// onto the filename to keep each SourceFile's DestRelPath relative to the
// recorder directory, matching what that driver's own SourceFiles produces.
// Stops early (trailer short of n components) if dir runs out of parents.
func ancestorDir(dir string, n int) (ancestor, trailer string) {
	for i := 0; i < n; i++ {
		if dir == "." || dir == "/" {
			break
		}
		base := path.Base(dir)
		if trailer == "" {
			trailer = base
		} else {
			trailer = path.Join(base, trailer)
		}
		dir = path.Dir(dir)
	}
	return dir, trailer
}

// GroupTimestampFiles groups relPaths (forward-slash paths, however the
// caller obtained them - a recursive directory listing, an rclone recursive
// listing, ...) into candidate recorder directories. This is filesystem/
// Location-agnostic on purpose: Sync Recorders' destinations are always
// local, but Manage Files' Retime can point at any kind of Location (local
// or remote), and grouping/matching a driver's naming pattern needs no I/O
// either way.
//
// Each file's containing directory is first matched against a driver's
// TimestampParser by filename pattern, then walked up by that driver's
// RecorderDirDepth to find its actual recorder directory - 0 levels for a
// driver whose recorder directory holds files directly (Sony), more for one
// that nests them under a device category subdirectory (Olympus's MUSIC,
// TALK, etc.). Every file that resolves to the same recorder directory is
// merged into one TimestampGroup regardless of which category subdirectory
// it came from, so a multi-category recorder (Olympus) still reads as one
// recorder rather than one card per category sharing the category's name as
// a bogus "recorder ID".
//
// A directory is only matched if at least one registered driver's
// TimestampParser recognizes at least one of its files' names (see
// Drivers); directories with no parseable recording names - a non-recorder
// directory like the experiment root holding metadata.csv - are silently
// skipped.
func GroupTimestampFiles(relPaths []string) []TimestampGroup {
	byDir := make(map[string][]string)
	var dirOrder []string
	for _, rp := range relPaths {
		dir := path.Dir(rp)
		name := path.Base(rp)
		if _, ok := byDir[dir]; !ok {
			dirOrder = append(dirOrder, dir)
		}
		byDir[dir] = append(byDir[dir], name)
	}

	groupsByDir := make(map[string]*TimestampGroup)
	var order []string
	for _, dir := range dirOrder {
		names := byDir[dir]
		var parser TimestampParser
		for _, d := range Drivers {
			p, ok := d.(TimestampParser)
			if !ok {
				continue
			}
			for _, n := range names {
				if _, ok := p.ParseTimestamp(n); ok {
					parser = p
					break
				}
			}
			if parser != nil {
				break
			}
		}
		if parser == nil {
			continue
		}

		recorderDir, trailer := ancestorDir(dir, parser.RecorderDirDepth())

		group, ok := groupsByDir[recorderDir]
		if !ok {
			group = &TimestampGroup{
				RecorderID: path.Base(recorderDir),
				RelDir:     recorderDir,
				Parser:     parser,
			}
			groupsByDir[recorderDir] = group
			order = append(order, recorderDir)
		}
		for _, n := range names {
			destRel := n
			if trailer != "" {
				destRel = path.Join(trailer, n)
			}
			group.Files = append(group.Files, SourceFile{DestRelPath: destRel})
		}
	}

	groups := make([]TimestampGroup, len(order))
	for i, dir := range order {
		groups[i] = *groupsByDir[dir]
	}
	return groups
}
