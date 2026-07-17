package drivers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
)

// OlympusVN541PC detects and offloads Olympus VN-541PC recorders. Ported
// from /Users/luke/Tools/olympus/{offload.py,recorder_core.py}: recordings
// are .wma files anywhere under the mount, and since the WMA-internal
// creation timestamp is bogus/null on this hardware, files are renamed to
// a timestamp derived from filesystem metadata instead of keeping their
// original names.
type OlympusVN541PC struct{}

func init() {
	recorder.Register(OlympusVN541PC{})
}

func (OlympusVN541PC) Name() string { return "olympus-vn-541pc" }

// QuickReject rules out volumes that aren't FAT-formatted flash storage
// without touching the disk - see isFATFamily.
func (OlympusVN541PC) QuickReject(v recorder.Volume) bool {
	return !isFATFamily(v.FSType)
}

// olympusSignatureDirs are the directory names an Olympus VN-541PC always
// has at its mount root, ported from recorder_core.py's SIGNATURE_DIRS.
var olympusSignatureDirs = []string{"RECORDER", "SYSTEM"}

func (OlympusVN541PC) Detect(v recorder.Volume) bool {
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

func (OlympusVN541PC) idFilePath(v recorder.Volume) string {
	return filepath.Join(v.MountPoint, "ID.txt")
}

func (d OlympusVN541PC) RecorderID(v recorder.Volume) (string, error) {
	return recorder.ReadIDFile(d.idFilePath(v))
}

func (d OlympusVN541PC) SourceFiles(v recorder.Volume) ([]recorder.SourceFile, error) {
	var files []recorder.SourceFile
	used := make(map[string]bool)

	err := recorder.WalkFiles(v.MountPoint, func(path string, info os.FileInfo) error {
		if !strings.EqualFold(filepath.Ext(info.Name()), ".wma") {
			return nil
		}

		relDir, err := filepath.Rel(v.MountPoint, filepath.Dir(path))
		if err != nil {
			return err
		}
		relDir = stripRecorderPrefix(relDir)

		ts := recorder.BestCreationTime(info)
		base := ts.Format("20060102_150405") + ".wma"
		destRel := uniqueDestRel(used, relDir, base)

		files = append(files, recorder.SourceFile{AbsPath: path, DestRelPath: destRel})
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

// olympusTimestampPattern matches the destination names this driver itself
// generates in SourceFiles: "20060102_150405.wma", with an optional
// "_<n>" disambiguator appended by uniqueDestRel when two recordings on the
// device landed in the same second.
var olympusTimestampPattern = regexp.MustCompile(`(?i)^(\d{8})_(\d{6})(?:_\d+)?\.wma$`)

// ParseTimestamp implements recorder.TimestampParser. Unlike the WMA-
// internal timestamp (bogus/null on this hardware, see SourceFiles' doc),
// BestCreationTime still ultimately comes from the recorder's own real-time
// clock as recorded on its FAT filesystem - the same single point of
// failure as Sony's filename-encoded timestamp. A recorder whose clock was
// set with the wrong date, or in the wrong half of the day, produces a
// wrong destination timestamp here exactly as it would a wrong filename on
// Sony, so this device is not exempt from bad-timestamp detection despite
// the renaming step.
func (OlympusVN541PC) ParseTimestamp(destRelPath string) (time.Time, bool) {
	m := olympusTimestampPattern.FindStringSubmatch(filepath.Base(destRelPath))
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102_150405", m[1]+"_"+m[2], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// RenameForTimestamp implements recorder.TimestampParser, rebuilding this
// driver's own "20060102_150405.wma" name for t. Any "_<n>" disambiguator
// on destRelPath is dropped, since the corrected file no longer necessarily
// collides with whatever it originally shared a second with; a fresh
// collision at the new timestamp is left to the normal offload/copy
// machinery to catch, same as it would for a first-time offload.
func (OlympusVN541PC) RenameForTimestamp(destRelPath string, t time.Time) string {
	dir := filepath.Dir(destRelPath)
	newBase := t.Format("20060102_150405") + ".wma"
	if dir == "." {
		return newBase
	}
	return filepath.Join(dir, newBase)
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
