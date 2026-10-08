package recorder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// offloadDriver is a Driver stand-in for StartOffload tests: it reports
// whatever files were planted on a fake "card" directory, splitting them into
// recordings (deletable) and metadata (copy-only) by name.
type offloadDriver struct {
	id string
	// metadata, when non-empty, is the one filename on the card that this
	// driver reports via MetadataFiles rather than SourceFiles.
	metadata string
}

func (offloadDriver) Name() string                        { return "offload-test" }
func (offloadDriver) QuickReject(Volume) bool             { return false }
func (offloadDriver) Detect(Volume) bool                  { return true }
func (d offloadDriver) RecorderID(Volume) (string, error) { return d.id, nil }

func (d offloadDriver) SourceFiles(v Volume) ([]SourceFile, error) {
	return d.list(v, false), nil
}

func (d offloadDriver) MetadataFiles(v Volume) ([]SourceFile, error) {
	if d.metadata == "" {
		return nil, nil
	}
	return d.list(v, true), nil
}

func (d offloadDriver) list(v Volume, wantMetadata bool) []SourceFile {
	entries, err := os.ReadDir(v.MountPoint)
	if err != nil {
		return nil
	}
	var files []SourceFile
	for _, e := range entries {
		if e.IsDir() || (e.Name() == d.metadata) != wantMetadata {
			continue
		}
		files = append(files, SourceFile{
			AbsPath:     filepath.Join(v.MountPoint, e.Name()),
			DestRelPath: e.Name(),
		})
	}
	return files
}

// runOffload drives a full StartOffload to completion against a card holding
// files (name -> content), returning the destination root and the final
// status.
func runOffload(t *testing.T, driver Driver, autoDelete bool, files map[string]string) (card, destRoot string, status OffloadStatus) {
	t.Helper()
	card = t.TempDir()
	destRoot = t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(card, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	_, progress := StartOffload(ctx, ctx, driver, Volume{MountPoint: card}, "REC1",
		[]string{destRoot}, "", "exp", nil, autoDelete, true, nil)

	status = OffloadRunning
	for p := range progress {
		status = p.Status
		if p.Err != nil && p.Status != OffloadRunning {
			t.Fatalf("offload failed: %v", p.Err)
		}
	}
	return card, destRoot, status
}

// TestOffloadCopiesMetadataButNeverDeletesIt is the point of
// MetadataFileLister: the metadata file has to land at the destination
// exactly like a recording, and has to survive an auto-delete offload that
// wipes every recording off the card.
func TestOffloadCopiesMetadataButNeverDeletesIt(t *testing.T) {
	driver := offloadDriver{id: "REC1", metadata: "CONFIG.TXT"}
	card, destRoot, status := runOffload(t, driver, true, map[string]string{
		"rec1.wav":   "audio one",
		"rec2.wav":   "audio two",
		"CONFIG.TXT": "Device ID : 245AAA0666BC09F5",
	})
	if status != OffloadDone {
		t.Fatalf("status = %v, want OffloadDone", status)
	}

	destDir := filepath.Join(destRoot, "exp", "REC1")
	for name, want := range map[string]string{
		"rec1.wav":   "audio one",
		"rec2.wav":   "audio two",
		"CONFIG.TXT": "Device ID : 245AAA0666BC09F5",
	} {
		got, err := os.ReadFile(filepath.Join(destDir, name))
		if err != nil {
			t.Errorf("%s did not reach the destination: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}

	// The recordings are gone from the card (that's what auto-delete is
	// for) but the metadata file, which identifies the device, is not.
	for _, name := range []string{"rec1.wav", "rec2.wav"} {
		if _, err := os.Stat(filepath.Join(card, name)); !os.IsNotExist(err) {
			t.Errorf("%s was not deleted from the card under autoDelete", name)
		}
	}
	if _, err := os.Stat(filepath.Join(card, "CONFIG.TXT")); err != nil {
		t.Errorf("metadata file was deleted from the card: %v", err)
	}
}

// TestOffloadDriverWithoutMetadata covers the other side of the optional
// interface: a driver that declares no metadata offloads exactly as before.
func TestOffloadDriverWithoutMetadata(t *testing.T) {
	driver := offloadDriver{id: "REC1"}
	card, destRoot, status := runOffload(t, driver, true, map[string]string{"rec1.wav": "audio one"})
	if status != OffloadDone {
		t.Fatalf("status = %v, want OffloadDone", status)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "exp", "REC1", "rec1.wav")); err != nil {
		t.Errorf("recording did not reach the destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(card, "rec1.wav")); !os.IsNotExist(err) {
		t.Error("recording was not deleted from the card under autoDelete")
	}
}

// TestMetadataFilesOptional checks the helper both drivers go through, so
// callers never have to type-assert MetadataFileLister themselves.
func TestMetadataFilesOptional(t *testing.T) {
	card := t.TempDir()
	if err := os.WriteFile(filepath.Join(card, "CONFIG.TXT"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := Volume{MountPoint: card}

	got, err := MetadataFiles(offloadDriver{id: "REC1", metadata: "CONFIG.TXT"}, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DestRelPath != "CONFIG.TXT" {
		t.Errorf("MetadataFiles = %v, want one CONFIG.TXT entry", got)
	}

	got, err = MetadataFiles(fakeDriver{name: "no-metadata"}, v)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("MetadataFiles for a driver without the interface = %v, want nil", got)
	}
}
