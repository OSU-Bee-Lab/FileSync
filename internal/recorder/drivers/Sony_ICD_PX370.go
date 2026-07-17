// Package drivers holds recorder.Driver implementations for each supported
// recorder model. Adding a new model is a matter of dropping in a new file
// here that registers itself via recorder.Register from an init() — no
// other package needs to change. This package is imported (blank) once from
// main so those init()s run.
package drivers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
)

// SonyICDPX370 detects and offloads Sony ICD-PX370 recorders. Ported from
// filesync's recorders.py, with all hub/USB-port identity logic dropped —
// recorder identity now comes from a tag file (see identity.go), not
// physical port position.
type SonyICDPX370 struct{}

func init() {
	recorder.Register(SonyICDPX370{})
}

func (SonyICDPX370) Name() string { return "sony-icd-px370" }

// QuickReject rules out volumes that aren't FAT-formatted flash storage
// without touching the disk - see isFATFamily.
func (SonyICDPX370) QuickReject(v recorder.Volume) bool {
	return !isFATFamily(v.FSType)
}

// recFileDir returns the REC_FILE directory for v, checking both the
// internal-memory layout (<mount>/REC_FILE) and the SD-card layout
// (<mount>/PRIVATE/SONY/REC_FILE), or "" if neither is present.
func (SonyICDPX370) recFileDir(v recorder.Volume) string {
	internal := filepath.Join(v.MountPoint, "REC_FILE")
	if fi, err := os.Stat(internal); err == nil && fi.IsDir() {
		return internal
	}
	sdCard := filepath.Join(v.MountPoint, "PRIVATE", "SONY", "REC_FILE")
	if fi, err := os.Stat(sdCard); err == nil && fi.IsDir() {
		return sdCard
	}
	return ""
}

func (d SonyICDPX370) Detect(v recorder.Volume) bool {
	return d.recFileDir(v) != ""
}

// RecorderID reads the recorder's ID directly off its own storage layout:
// the REC_FILE recordings-directory name (e.g. "01_02") is the device's own
// stable identity, ported from filesync's get_identity/recorder_number.
// Unlike Olympus, nothing is ever written to a Sony recorder for identity
// purposes - do not add a tag-file scheme here.
func (d SonyICDPX370) RecorderID(v recorder.Volume) (string, error) {
	dir, err := d.recordingsDir(v)
	if err != nil {
		return "", err
	}
	return filepath.Base(dir), nil
}

var sonyFolderPattern = regexp.MustCompile(`^FOLDER\d`)

// recordingsDir returns the single non-FOLDER* subdirectory of REC_FILE
// that holds the actual recordings, matching filesync's get_recorder_dir.
func (d SonyICDPX370) recordingsDir(v recorder.Volume) (string, error) {
	recFile := d.recFileDir(v)
	if recFile == "" {
		return "", fmt.Errorf("sony-icd-px370: %s does not look like a recorder", v.MountPoint)
	}

	entries, err := os.ReadDir(recFile)
	if err != nil {
		return "", err
	}

	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == ".sony_recording" {
			continue
		}
		if sonyFolderPattern.MatchString(e.Name()) {
			continue
		}
		candidates = append(candidates, e.Name())
	}

	if len(candidates) != 1 {
		return "", fmt.Errorf("sony-icd-px370: expected exactly one recordings directory under %s, found %d", recFile, len(candidates))
	}

	return filepath.Join(recFile, candidates[0]), nil
}

func (d SonyICDPX370) SourceFiles(v recorder.Volume) ([]recorder.SourceFile, error) {
	dir, err := d.recordingsDir(v)
	if err != nil {
		return nil, err
	}
	return walkRelative(dir)
}

// sonyTimestampPattern matches the Sony ICD-PX370's own recording filenames:
// YYMMDD_HHMM.mp3, e.g. "260221_1421.mp3" (Feb 21 2026, 2:21 PM), sometimes
// prepended with "tmp_" for reasons the recorder itself doesn't document,
// e.g. "tmp_260221_1421.mp3".
var sonyTimestampPattern = regexp.MustCompile(`(?i)^(?:tmp_)?(\d{2})(\d{2})(\d{2})_(\d{2})(\d{2})\.mp3$`)

// ParseTimestamp implements recorder.TimestampParser: it recovers the
// recording time the Sony encoded into its own filename, so a bad-timestamp
// detector can compare it across a whole recorder's files without needing
// the device itself (which may already be disconnected, or wiped, by the
// time such a check runs).
func (SonyICDPX370) ParseTimestamp(destRelPath string) (time.Time, bool) {
	m := sonyTimestampPattern.FindStringSubmatch(filepath.Base(destRelPath))
	if m == nil {
		return time.Time{}, false
	}
	yy, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	dd, _ := strconv.Atoi(m[3])
	hh, _ := strconv.Atoi(m[4])
	min, _ := strconv.Atoi(m[5])
	if mm < 1 || mm > 12 || dd < 1 || dd > 31 || hh > 23 || min > 59 {
		return time.Time{}, false
	}
	return time.Date(2000+yy, time.Month(mm), dd, hh, min, 0, 0, time.Local), true
}

// RenameForTimestamp implements recorder.TimestampParser: it rebuilds the
// Sony-style YYMMDD_HHMM.mp3 name for t, preserving destRelPath's directory
// and "tmp_" prefix (if any) - only the timestamp portion changes.
func (SonyICDPX370) RenameForTimestamp(destRelPath string, t time.Time) string {
	dir := filepath.Dir(destRelPath)
	base := filepath.Base(destRelPath)
	prefix := ""
	if len(base) >= 4 && (base[:4] == "tmp_" || base[:4] == "TMP_") {
		prefix = base[:4]
	}
	newBase := prefix + t.Format("060102_1504") + ".mp3"
	if dir == "." {
		return newBase
	}
	return filepath.Join(dir, newBase)
}

// walkRelative lists every regular file under dir, recursively, with
// DestRelPath set to its path relative to dir — preserving the recorder's
// own layout, matching filesync's list_files_relative.
func walkRelative(dir string) ([]recorder.SourceFile, error) {
	var files []recorder.SourceFile
	err := recorder.WalkFiles(dir, func(path string, info os.FileInfo) error {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, recorder.SourceFile{AbsPath: path, DestRelPath: rel})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
