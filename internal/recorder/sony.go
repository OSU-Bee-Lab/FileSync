package recorder

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// SonyICDPX370 detects and offloads Sony ICD-PX370 recorders. Ported from
// filesync's recorders.py, with all hub/USB-port identity logic dropped —
// recorder identity now comes from a tag file (see identity.go), not
// physical port position.
type SonyICDPX370 struct{}

func (SonyICDPX370) Name() string { return "sony-icd-px370" }

// recFileDir returns the REC_FILE directory for v, checking both the
// internal-memory layout (<mount>/REC_FILE) and the SD-card layout
// (<mount>/PRIVATE/SONY/REC_FILE), or "" if neither is present.
func (SonyICDPX370) recFileDir(v Volume) string {
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

func (d SonyICDPX370) Detect(v Volume) bool {
	return d.recFileDir(v) != ""
}

// RecorderID reads the recorder's ID directly off its own storage layout:
// the REC_FILE recordings-directory name (e.g. "01_02") is the device's own
// stable identity, ported from filesync's get_identity/recorder_number.
// Unlike Olympus, nothing is ever written to a Sony recorder for identity
// purposes - do not add a tag-file scheme here.
func (d SonyICDPX370) RecorderID(v Volume) (string, error) {
	dir, err := d.recordingsDir(v)
	if err != nil {
		return "", err
	}
	return filepath.Base(dir), nil
}

var sonyFolderPattern = regexp.MustCompile(`^FOLDER\d`)

// recordingsDir returns the single non-FOLDER* subdirectory of REC_FILE
// that holds the actual recordings, matching filesync's get_recorder_dir.
func (d SonyICDPX370) recordingsDir(v Volume) (string, error) {
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

func (d SonyICDPX370) SourceFiles(v Volume) ([]SourceFile, error) {
	dir, err := d.recordingsDir(v)
	if err != nil {
		return nil, err
	}
	return walkRelative(dir)
}

// walkRelative lists every regular file under dir, recursively, with
// DestRelPath set to its path relative to dir — preserving the recorder's
// own layout, matching filesync's list_files_relative.
func walkRelative(dir string) ([]SourceFile, error) {
	var files []SourceFile
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, SourceFile{AbsPath: path, DestRelPath: rel})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
