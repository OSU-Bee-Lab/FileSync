package recorder

import "strings"

// isHiddenEntry reports whether name is a dotfile/dot-directory, e.g. macOS's
// .Spotlight-V100, .Trashes, .fseventsd. These are OS-managed, sometimes
// permission-protected, and never contain recordings, so directory walks
// skip them rather than failing outright when the OS denies access to them.
func isHiddenEntry(name string) bool {
	return strings.HasPrefix(name, ".")
}

// Volume is a single mounted filesystem, as reported by the OS — the unit
// hotplug detection and drivers work with. No hub/port information is
// captured here on purpose: recorder identity comes from a tag file on the
// device itself (see identity.go), not from where it's physically plugged
// in.
type Volume struct {
	MountPoint string
}

// SourceFile is one file on a recorder that needs to land at DestRelPath
// under the recorder's destination directory. DestRelPath is driver-chosen:
// Sony preserves the recorder's own relative path, Olympus renames to a
// timestamp derived from filesystem metadata.
type SourceFile struct {
	AbsPath     string
	DestRelPath string
}

// Driver implements recorder-model-specific detection and file layout.
// Storage layouts vary too much between recorder hardware to have one
// generic implementation (see sony.go vs olympus.go), so each supported
// model gets its own Driver.
type Driver interface {
	// Name identifies the driver, e.g. "sony-icd-px370".
	Name() string

	// Detect reports whether v looks like this driver's recorder model.
	Detect(v Volume) bool

	// RecorderID returns v's persistent recorder ID, read from the device.
	// This app never assigns or writes an ID to a recorder — it only reads
	// whatever identity the device already has (Sony: its REC_FILE
	// recordings-directory name, e.g. "01_02"; Olympus: its ID.txt tag
	// file, assigned by an out-of-band process). If no ID is present,
	// RecorderID returns an error rather than manufacturing one.
	RecorderID(v Volume) (id string, err error)

	// SourceFiles lists every file on the recorder that should be
	// offloaded, with the relative path it should land at under the
	// recorder's destination directory.
	SourceFiles(v Volume) ([]SourceFile, error)
}

// Drivers is the registry of supported recorder models, checked in order
// against each newly attached volume. A volume matching none of them is
// surfaced in the UI as an unrecognized device; no action is taken on it.
var Drivers = []Driver{
	SonyICDPX370{},
	OlympusVN541PC{},
}

// Detect returns the first driver in Drivers that claims v, or nil if none
// do.
func Detect(v Volume) Driver {
	for _, d := range Drivers {
		if d.Detect(v) {
			return d
		}
	}
	return nil
}
