package recorder

import "path"

// TimestampGroup is one candidate recorder directory found by
// GroupTimestampFiles: every file directly inside it (see SCHEMA.md's
// recorder directories, which hold audio files directly with no further
// nesting), plus whichever registered driver's TimestampParser recognized
// its naming pattern.
type TimestampGroup struct {
	// RecorderID is the directory's own name - per SCHEMA.md, recorder
	// directories are named only by the recorder ID.
	RecorderID string
	// RelDir is the directory's path exactly as it appeared in the
	// relPaths GroupTimestampFiles was given (e.g. relative to a Location
	// root, if that's what the caller's paths were relative to).
	RelDir string
	Parser TimestampParser
	// Files' DestRelPath is just the filename (RelDir holds them
	// directly), matching TimestampParser.RenameForTimestamp's dir=="."
	// case.
	Files []SourceFile
}

// GroupTimestampFiles groups relPaths (forward-slash paths, however the
// caller obtained them - a recursive directory listing, an rclone recursive
// listing, ...) by their containing directory, treating each directory as a
// candidate recorder directory - SCHEMA.md's free-form intermediate
// directories mean a recorder directory's depth doesn't matter, only that
// it holds files directly. This is filesystem/Location-agnostic on purpose:
// Sync Recorders' destinations are always local, but Manage Files' Retime
// can point at any kind of Location (local or remote), and grouping/
// matching a driver's naming pattern needs no I/O either way.
//
// A directory is only returned if at least one registered driver's
// TimestampParser recognizes at least one of its files' names (see
// Drivers); directories with no parseable recording names - an Olympus
// recorder's already-metadata-derived names, or a non-recorder directory
// like the experiment root holding metadata.csv - are silently skipped.
func GroupTimestampFiles(relPaths []string) []TimestampGroup {
	byDir := make(map[string][]SourceFile)
	var order []string
	for _, rp := range relPaths {
		dir := path.Dir(rp)
		name := path.Base(rp)
		if _, ok := byDir[dir]; !ok {
			order = append(order, dir)
		}
		byDir[dir] = append(byDir[dir], SourceFile{DestRelPath: name})
	}

	var groups []TimestampGroup
	for _, dir := range order {
		files := byDir[dir]
		var parser TimestampParser
		for _, d := range Drivers {
			p, ok := d.(TimestampParser)
			if !ok {
				continue
			}
			for _, f := range files {
				if _, ok := p.ParseTimestamp(f.DestRelPath); ok {
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
		groups = append(groups, TimestampGroup{
			RecorderID: path.Base(dir),
			RelDir:     dir,
			Parser:     parser,
			Files:      files,
		})
	}
	return groups
}
