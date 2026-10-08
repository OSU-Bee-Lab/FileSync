package drivers

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
)

// realConfigTXT is the head of a CONFIG.TXT taken verbatim off a deployed
// AudioMoth (firmware 1.11.0), cut a few fields past the ones the driver
// reads, so the parser is exercised against the real layout - column-aligned
// field names, blank lines between groups - rather than a tidied-up one.
const realConfigTXT = `Device ID                       : 245AAA0666BC09F5
Firmware                        : AudioMoth-Firmware-Basic (1.11.0)

Device time                     : 2026-07-11 12:21:10 (UTC-4)

Sample rate (Hz)                : 16000
Gain                            : Medium
`

// writeCard builds a fake mounted card: CONFIG.TXT holding config (skipped
// when config is empty) plus an empty file for each name in files.
func writeCard(t *testing.T, config string, files ...string) recorder.Volume {
	t.Helper()
	dir := t.TempDir()
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, "CONFIG.TXT"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return recorder.Volume{MountPoint: dir, FSType: "msdos"}
}

func TestAudioMothDetectAndID(t *testing.T) {
	d := AudioMoth{}

	v := writeCard(t, realConfigTXT)
	if !d.Detect(v) {
		t.Fatal("did not detect a real AudioMoth CONFIG.TXT")
	}
	id, err := d.RecorderID(v)
	if err != nil {
		t.Fatal(err)
	}
	if id != "245AAA0666BC09F5" {
		t.Errorf("RecorderID = %q, want the CONFIG.TXT device ID", id)
	}

	// A card with no CONFIG.TXT at all (pre-1.4.0 firmware, or a card the
	// device has never started a deployment on) isn't claimed, rather than
	// claimed and then unable to produce an ID.
	if d.Detect(writeCard(t, "", "20260715_051500.WAV")) {
		t.Error("claimed a card with no CONFIG.TXT")
	}

	// Some other device's CONFIG.TXT: neither field the driver keys on is
	// present, so it must not be claimed.
	if d.Detect(writeCard(t, "mode: 3\nchannel: 7\n")) {
		t.Error("claimed an unrelated CONFIG.TXT")
	}

	// The near-misses that make bare presence of the file insufficient: a
	// plausible "Device ID" line with nothing saying AudioMoth wrote it...
	if d.Detect(writeCard(t, "Device ID : 245AAA0666BC09F5\nFirmware : SomeOtherThing (2.0)\n")) {
		t.Error("claimed a non-AudioMoth device's CONFIG.TXT")
	}
	// ...and the reverse, an AudioMoth firmware line with no well-formed
	// serial, leaving no stable identity to key a recorder directory on.
	if d.Detect(writeCard(t, "Device ID : unknown\nFirmware : AudioMoth-Firmware-Basic (1.11.0)\n")) {
		t.Error("claimed a card with no well-formed device ID")
	}
}

func TestAudioMothQuickReject(t *testing.T) {
	d := AudioMoth{}
	// Cards 32GB and under ship FAT32, larger ones exFAT, and the firmware
	// has supported both since 1.2.2 - so neither may be pre-filtered out.
	for _, fstype := range []string{"msdos", "vfat", "fat32", "exfat"} {
		if d.QuickReject(recorder.Volume{FSType: fstype}) {
			t.Errorf("QuickReject rejected %q, a format AudioMoth cards ship in", fstype)
		}
	}
	for _, fstype := range []string{"apfs", "hfs", "ntfs", "ext4"} {
		if !d.QuickReject(recorder.Volume{FSType: fstype}) {
			t.Errorf("QuickReject accepted %q", fstype)
		}
	}
}

func TestAudioMothSourceFiles(t *testing.T) {
	v := writeCard(t, realConfigTXT,
		"20260715_051500.WAV",
		"245AAA0666BC09F5_20260715_052500.WAV",
		"20260715_053500T.WAV",
		// A daily folder ("Use daily folder for WAV files"), which flattens
		// away - the date is already in every filename.
		"20260716/20260716_051500.WAV",
		// Not recordings. CONFIG.TXT in particular stays behind so the card
		// is still identifiable after an auto-delete offload.
		"notes.txt",
		"holiday.wav",
	)

	files, err := AudioMoth{}.SourceFiles(v)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(files))
	for _, f := range files {
		got[f.DestRelPath] = true
	}
	want := []string{
		"20260715_051500.WAV",
		"245AAA0666BC09F5_20260715_052500.WAV",
		"20260715_053500T.WAV",
		"20260716_051500.WAV",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d source files (%v), want %d", len(got), got, len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing source file %q", w)
		}
	}
}

func TestAudioMothParseAndRenameTimestamp(t *testing.T) {
	d := AudioMoth{}
	recorded := time.Date(2026, 7, 15, 5, 15, 0, 0, time.Local)
	fixed := time.Date(2025, 6, 14, 4, 14, 0, 0, time.Local)

	cases := []struct {
		name    string
		renamed string // "" when the name shouldn't parse at all
	}{
		{"20260715_051500.WAV", "20250614_041400.WAV"},
		{"245AAA0666BC09F5_20260715_051500.WAV", "245AAA0666BC09F5_20250614_041400.WAV"},
		{"SITE-A_20260715_051500.WAV", "SITE-A_20250614_041400.WAV"},
		{"20260715_051500T.WAV", "20250614_041400T.WAV"},
		{"20260715_051500_SYNC.WAV", "20250614_041400_SYNC.WAV"},
		{"20260715_051500.wav", "20250614_041400.WAV"},
		// Other drivers' naming must not be claimed, or a mixed destination
		// directory would be grouped under the wrong driver.
		{"260221_1421.mp3", ""},
		{"20260715_051500.mp3", ""},
		{"20261332_051500.WAV", ""}, // month 13, day 32
	}

	for _, c := range cases {
		got, ok := d.ParseTimestamp(c.name)
		if ok != (c.renamed != "") {
			t.Errorf("ParseTimestamp(%q) ok = %v, want %v", c.name, ok, c.renamed != "")
			continue
		}
		if !ok {
			continue
		}
		if !got.Equal(recorded) {
			t.Errorf("ParseTimestamp(%q) = %v, want %v", c.name, got, recorded)
		}
		if r := d.RenameForTimestamp(c.name, fixed); r != c.renamed {
			t.Errorf("RenameForTimestamp(%q) = %q, want %q", c.name, r, c.renamed)
		}
	}

	// Directories are preserved, per the TimestampParser contract.
	orig := filepath.Join("REC1", "20260715_051500.WAV")
	if r := d.RenameForTimestamp(orig, fixed); r != filepath.Join("REC1", "20250614_041400.WAV") {
		t.Errorf("RenameForTimestamp dropped the directory: %q", r)
	}
}
