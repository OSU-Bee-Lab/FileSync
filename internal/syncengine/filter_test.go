package syncengine

import (
	"context"
	"path/filepath"
	"testing"
)

// TestDefaultFilterSettings_ExcludesJunkFiles guards the default Settings
// behavior: a fresh scan must skip macOS/Windows junk files without any
// user configuration, but still pick up real data alongside them.
func TestDefaultFilterSettings_ExcludesJunkFiles(t *testing.T) {
	srcRoot := t.TempDir()
	destFolder := filepath.Join(t.TempDir(), "out")
	seedExperiment(t, srcRoot, "Luke - Zucchini")
	writeFile(t, filepath.Join(srcRoot, "Luke - Zucchini", ".DS_Store"), "junk")
	writeFile(t, filepath.Join(srcRoot, "Luke - Zucchini", "2026-06-23", "RecorderA", "._260623_0900.mp3"), "junk")
	src := localLoc(srcRoot)
	ctx := context.Background()

	scan, err := ScanPullFilesWithProgress(ctx, src, "Luke - Zucchini", nil, destFolder, true, DefaultFilterSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range scan.Entries {
		if e.RelPath == ".DS_Store" || filepath.Base(e.RelPath) == "._260623_0900.mp3" {
			t.Fatalf("expected junk file %q to be filtered out of scan", e.RelPath)
		}
	}
	// seedExperiment writes 5 real files (metadata.csv, README.txt, 2 mp3s,
	// 1 wav); the .DS_Store and ._* sidecar must not add to this count.
	if scan.CopyCount != 5 {
		t.Fatalf("scan.CopyCount = %d, want 5 (junk files must not count)", scan.CopyCount)
	}
}

// TestExcludeRule_DisabledRuleIsNotApplied ensures a rule the user has
// unchecked in Settings is kept in config but not enforced during a scan.
func TestExcludeRule_DisabledRuleIsNotApplied(t *testing.T) {
	srcRoot := t.TempDir()
	destFolder := filepath.Join(t.TempDir(), "out")
	seedExperiment(t, srcRoot, "Luke - Zucchini")
	writeFile(t, filepath.Join(srcRoot, "Luke - Zucchini", ".DS_Store"), "junk")
	src := localLoc(srcRoot)
	ctx := context.Background()

	fset := FilterSettings{ExcludeRules: []ExcludeRule{{Pattern: ".DS_Store", Enabled: false}}}
	scan, err := ScanPullFilesWithProgress(ctx, src, "Luke - Zucchini", nil, destFolder, true, fset, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range scan.Entries {
		if e.RelPath == ".DS_Store" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected disabled exclude rule to leave .DS_Store in the scan")
	}
}
