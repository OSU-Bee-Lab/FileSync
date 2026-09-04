package drivers

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
)

// AudioMoth detects and offloads AudioMoth recorders. Unlike the Sony and
// Olympus units, an AudioMoth has no USB storage mode of its own: it writes
// to a microSD card that gets pulled and mounted on its own, so everything
// here works off a bare card with no device-specific volume layout to lean
// on. What a deployed card does carry is CONFIG.TXT: from firmware 1.4.0 on,
// the AudioMoth writes it at the card root as a summary of its own settings
// before it writes any WAV files, on every switch to CUSTOM or DEFAULT. Its
// "Device ID" line is the device's factory-unique, immutable serial - the
// identity this driver reports (see RecorderID). A card off a device running
// firmware older than 1.4.0 therefore won't be detected, since nothing on it
// says what wrote it.
type AudioMoth struct{}

func init() {
	recorder.Register(AudioMoth{})
}

func (AudioMoth) Name() string { return "audiomoth" }

// QuickReject rules out volumes that aren't FAT- or exFAT-formatted flash
// storage without touching the disk. Both formats have to be accepted:
// AudioMoth firmware has supported FAT32 and exFAT alike since 1.2.2, and
// cards are used as supplied - 32GB and under normally ship FAT32, larger
// ones exFAT - so neither can be ruled out. Accepting exFAT makes this a
// weaker pre-filter than the USB recorders' (a large external HDD is
// typically exFAT too), but Detect below is a single stat plus a short
// header read at the mount root, not a directory walk, so falling through
// to it is cheap.
func (AudioMoth) QuickReject(v recorder.Volume) bool {
	return !isFATFamily(v.FSType) && !isExFAT(v.FSType)
}

func (AudioMoth) configPath(v recorder.Volume) string {
	return filepath.Join(v.MountPoint, "CONFIG.TXT")
}

// Detect reports whether v's root holds a CONFIG.TXT that parses as an
// AudioMoth one. The file's presence alone isn't enough - "CONFIG.TXT" is a
// generic enough name that other hardware (and plenty of hand-written files)
// uses it - so this insists on both fields that identify the writer: a
// firmware line naming AudioMoth, and a well-formed Device ID.
func (d AudioMoth) Detect(v recorder.Volume) bool {
	cfg, err := d.parseConfig(v)
	return err == nil && cfg.deviceID != "" && cfg.isAudioMoth
}

// RecorderID returns the AudioMoth's factory device ID, read from CONFIG.TXT
// (e.g. "245AAA0666BC09F5"). This is burned into the chip and rewritten by
// the device itself each time it starts a deployment, so it survives
// reformatting the card, swapping cards between devices, and renaming the
// volume - and it identifies the recorder rather than the card, which is
// what the destination directory is supposed to be keyed on. Nothing is ever
// written to the card for identity purposes; as with the other drivers, a
// card with no readable ID is an error, not something to fill in.
func (d AudioMoth) RecorderID(v recorder.Volume) (string, error) {
	cfg, err := d.parseConfig(v)
	if err != nil {
		return "", err
	}
	if cfg.deviceID == "" {
		return "", fmt.Errorf("audiomoth: no device ID in %s", d.configPath(v))
	}
	return cfg.deviceID, nil
}

// audioMothConfig is the handful of CONFIG.TXT fields this driver cares
// about. The file has ~30 more (sample rate, gain, schedule, GPS...) that
// are deployment settings rather than identity, so they're skipped.
type audioMothConfig struct {
	deviceID    string
	isAudioMoth bool
}

// audioMothDeviceID matches the 16-hex-digit serial the firmware reports.
// Anchored and length-checked so a CONFIG.TXT from unrelated hardware that
// happens to have a "Device ID" line can't be mistaken for an AudioMoth's.
var audioMothDeviceID = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)

// parseConfig reads the "<field> : <value>" lines of CONFIG.TXT. Only the
// header is needed, and reading a whole card's worth of file would be
// pointless, so this stops as soon as both fields of interest are in hand or
// the header is clearly past (see audioMothConfigScanLimit).
func (d AudioMoth) parseConfig(v recorder.Volume) (audioMothConfig, error) {
	path := d.configPath(v)
	f, err := os.Open(path)
	if err != nil {
		return audioMothConfig{}, fmt.Errorf("audiomoth: %w", err)
	}
	defer f.Close()

	var cfg audioMothConfig
	scanner := bufio.NewScanner(f)
	for lines := 0; lines < audioMothConfigScanLimit && scanner.Scan(); lines++ {
		field, value, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		field = strings.TrimSpace(field)
		value = strings.TrimSpace(value)

		switch {
		case strings.EqualFold(field, "Device ID"):
			if audioMothDeviceID.MatchString(value) {
				cfg.deviceID = strings.ToUpper(value)
			}
		case strings.EqualFold(field, "Firmware"):
			// e.g. "AudioMoth-Firmware-Basic (1.11.0)". Custom firmware
			// builds are common in the field and don't all keep that exact
			// name, so this only asks for the word itself.
			if strings.Contains(strings.ToLower(value), "audiomoth") {
				cfg.isAudioMoth = true
			}
		}
		if cfg.deviceID != "" && cfg.isAudioMoth {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return audioMothConfig{}, fmt.Errorf("audiomoth: reading %s: %w", path, err)
	}
	return cfg, nil
}

// audioMothConfigScanLimit caps how far into CONFIG.TXT parseConfig reads.
// Device ID and Firmware are the first two lines the Configuration app
// writes; this leaves generous room for firmware versions that reorder or
// add fields while still bailing out fast on a large file that merely
// happens to be named CONFIG.TXT.
const audioMothConfigScanLimit = 200

// audioMothRecordingPattern matches the firmware's recording filenames,
// following the patterns Open Acoustic Devices' own AudioMoth-Utils uses:
//
//	20260715_051500.WAV                   plain YYYYMMDD_HHMMSS
//	245AAA0666BC09F5_20260715_051500.WAV  "Use device ID in WAV file name"
//	SITE-A_20260715_051500.WAV            custom filename prefix
//	20260715_051500T.WAV                  amplitude/frequency-triggered
//	20260715_051500_SYNC.WAV              GPS-time-synced recording
//
// The prefix is left broad (the device-ID form is just one case of a
// free-text prefix) and the greedy match backtracks off an unprefixed name,
// since a bare timestamp's own leading "YYYYMMDD_" would otherwise be eaten
// as a prefix. The timestamp is written in whatever timezone the device was
// configured with (CONFIG.TXT records it as e.g. "UTC-4"), so as with the
// other drivers it's read as a local time.
var audioMothRecordingPattern = regexp.MustCompile(`(?i)^([A-Z0-9_-]+_)?(\d{8})_(\d{6})(T)?(_SYNC)?\.wav$`)

// SourceFiles lists the card's recordings. CONFIG.TXT is deliberately not
// among them: SourceFiles is also the delete list under
// AutoDeleteAfterVerify (see offload.go), and removing CONFIG.TXT would
// strip the card of the only thing that identifies it, leaving a freshly
// offloaded card unrecognizable until it's put back through the
// Configuration app.
//
// Recordings sit at the card root, or one level down in a per-day directory
// when "Use daily folder for WAV files" is enabled. Those directories are
// just chunking - the day is already in every filename - so destination
// paths flatten to the bare filename either way, which also keeps a card
// that had the setting toggled mid-deployment landing in one flat recorder
// directory. uniqueDestRel guards the flattening (two days' folders can't
// collide, since the date is part of the name, but a duplicate is still
// cheaper to disambiguate than to lose).
func (AudioMoth) SourceFiles(v recorder.Volume) ([]recorder.SourceFile, error) {
	var files []recorder.SourceFile
	used := make(map[string]bool)

	err := recorder.WalkFiles(v.MountPoint, func(path string, info os.FileInfo) error {
		if !audioMothRecordingPattern.MatchString(info.Name()) {
			return nil
		}
		files = append(files, recorder.SourceFile{
			AbsPath:     path,
			DestRelPath: uniqueDestRel(used, ".", info.Name()),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// ParseTimestamp implements recorder.TimestampParser. An AudioMoth's clock
// is set from the host machine by the Configuration app rather than typed in
// on the device, so the wrong-year/wrong-AM-PM faults this feeds are rarer
// here than on the Sony - but a card configured against a machine with a bad
// clock, or deployed after the internal clock drifted or reset on a battery
// change, lands the same way, so the check applies.
func (AudioMoth) ParseTimestamp(destRelPath string) (time.Time, bool) {
	m := audioMothRecordingPattern.FindStringSubmatch(filepath.Base(destRelPath))
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102_150405", m[2]+"_"+m[3], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// RenameForTimestamp implements recorder.TimestampParser, rebuilding the
// firmware's own name for t. Only the timestamp changes: the prefix and the
// T/_SYNC markers say what the file is and which device made it, so they
// carry over unaltered, as does destRelPath's directory.
func (AudioMoth) RenameForTimestamp(destRelPath string, t time.Time) string {
	dir := filepath.Dir(destRelPath)
	base := filepath.Base(destRelPath)
	var prefix, triggered, sync string
	if m := audioMothRecordingPattern.FindStringSubmatch(base); m != nil {
		prefix, triggered, sync = m[1], m[4], m[5]
	}
	newBase := prefix + t.Format("20060102_150405") + triggered + sync + ".WAV"
	if dir == "." {
		return newBase
	}
	return filepath.Join(dir, newBase)
}

// MetadataFiles implements recorder.MetadataFileLister: CONFIG.TXT is the
// device's own record of the settings a deployment was recorded under -
// sample rate, gain, filter, schedule, location - none of which is
// recoverable from the WAV files alone, so it's worth archiving next to the
// audio it describes. It lands at the root of the recorder directory under
// its own name.
//
// It must never be deleted from the card, which is what keeps it out of
// SourceFiles: it's the only thing identifying the device (see Detect), so
// an auto-delete offload that took it would leave the card unrecognizable
// until the AudioMoth next started a deployment on it.
func (d AudioMoth) MetadataFiles(v recorder.Volume) ([]recorder.SourceFile, error) {
	path := d.configPath(v)
	if _, err := os.Stat(path); err != nil {
		// Detect gates every offload, so a card being handled by this
		// driver has a readable CONFIG.TXT; if it vanished between then and
		// now the card has been swapped or pulled, and the offload's own
		// identity re-check is the right place for that to surface.
		return nil, nil
	}
	return []recorder.SourceFile{{AbsPath: path, DestRelPath: "CONFIG.TXT"}}, nil
}

// RecorderDirDepth implements recorder.TimestampParser: SourceFiles flattens
// every recording into the recorder directory itself, so a matched file's
// own containing directory is that recorder directory.
func (AudioMoth) RecorderDirDepth() int { return 0 }
