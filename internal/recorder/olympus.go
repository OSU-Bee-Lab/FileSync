package recorder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OlympusVN541PC detects and offloads Olympus VN-541PC recorders. Ported
// from /Users/luke/Tools/olympus/{offload.py,recorder_core.py}: recordings
// are .wma files anywhere under the mount, and since the WMA-internal
// creation timestamp is bogus/null on this hardware, files are renamed to
// a timestamp derived from filesystem metadata instead of keeping their
// original names.
type OlympusVN541PC struct{}

func (OlympusVN541PC) Name() string { return "olympus-vn-541pc" }

// olympusSignatureDirs are the directory names an Olympus VN-541PC always
// has at its mount root, ported from recorder_core.py's SIGNATURE_DIRS.
var olympusSignatureDirs = []string{"RECORDER", "SYSTEM"}

func (OlympusVN541PC) Detect(v Volume) bool {
	entries, err := os.ReadDir(v.MountPoint)
	if err != nil {
		return false
	}
	found := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			found[strings.ToUpper(e.Name())] = true
		}
	}
	for _, sig := range olympusSignatureDirs {
		if !found[sig] {
			return false
		}
	}
	return true
}

func (OlympusVN541PC) idFilePath(v Volume) string {
	return filepath.Join(v.MountPoint, "ID.txt")
}

func (d OlympusVN541PC) RecorderID(v Volume) (string, error) {
	return readIDFile(d.idFilePath(v))
}

func (d OlympusVN541PC) SourceFiles(v Volume) ([]SourceFile, error) {
	var files []SourceFile
	used := make(map[string]bool)

	err := walkFiles(v.MountPoint, func(path string, info os.FileInfo) error {
		if !strings.EqualFold(filepath.Ext(info.Name()), ".wma") {
			return nil
		}

		relDir, err := filepath.Rel(v.MountPoint, filepath.Dir(path))
		if err != nil {
			return err
		}
		relDir = stripRecorderPrefix(relDir)

		ts := bestCreationTime(info)
		base := ts.Format("20060102_150405") + ".wma"
		destRel := uniqueDestRel(used, relDir, base)

		files = append(files, SourceFile{AbsPath: path, DestRelPath: destRel})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// stripRecorderPrefix drops a leading "RECORDER" path component: every
// recording lives under <mount>/RECORDER/<category>/..., but RECORDER itself
// is just the device's fixed storage-root name, not a meaningful grouping —
// so destination paths start at the category (TALK, LP, etc.) instead.
func stripRecorderPrefix(relDir string) string {
	first, rest, found := strings.Cut(relDir, string(filepath.Separator))
	if found && strings.EqualFold(first, "RECORDER") {
		return rest
	}
	if !found && strings.EqualFold(relDir, "RECORDER") {
		return "."
	}
	return relDir
}

// uniqueDestRel avoids collisions within a single offload batch (two
// recordings on the same device timestamped to the same second), matching
// offload.py's unique_path in spirit. Unlike the Python version this
// doesn't check the real destination directory for pre-existing files from
// earlier sessions — that's intentionally left to the generic
// conflict/partial/complete detection in smartcopy.go, which compares file
// content rather than blindly appending a numeric suffix.
func uniqueDestRel(used map[string]bool, relDir, base string) string {
	rel := base
	if relDir != "." {
		rel = filepath.Join(relDir, base)
	}
	if !used[rel] {
		used[rel] = true
		return rel
	}

	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s_%d%s", stem, n, ext)
		candidateRel := candidate
		if relDir != "." {
			candidateRel = filepath.Join(relDir, candidate)
		}
		if !used[candidateRel] {
			used[candidateRel] = true
			return candidateRel
		}
	}
}
