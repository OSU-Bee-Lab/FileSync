package appconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

func TestLoadReturnsDefaultOnFirstRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Locations) != 0 {
		t.Fatalf("expected no locations on first run, got %v", cfg.Locations)
	}
	if len(cfg.DefaultFilter.IncludePatterns) != 0 {
		t.Fatalf("expected no default filter, got %v", cfg.DefaultFilter.IncludePatterns)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	cfg := Default()
	cfg.Locations = append(cfg.Locations, syncengine.Location{
		ID:       "abc123",
		Name:     "Lab Server",
		Kind:     syncengine.LocationLocal,
		RootPath: "/Volumes/BeeLabServer",
	}, syncengine.Location{
		ID:         "def456",
		Name:       "OSU SharePoint",
		Kind:       syncengine.LocationRemote,
		RemoteName: "sharepoint-osu",
		RootPath:   "Bee Lab Docs",
	})

	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Locations) != 2 {
		t.Fatalf("got %d locations, want 2", len(got.Locations))
	}
	if got.Locations[1].Kind != syncengine.LocationRemote || got.Locations[1].RemoteName != "sharepoint-osu" {
		t.Fatalf("remote location didn't round-trip: %+v", got.Locations[1])
	}
	if got.Locations[0].Kind != syncengine.LocationLocal {
		t.Fatalf("local location didn't round-trip: %+v", got.Locations[0])
	}
}

// TestLoadBackfillsTimestampTolerance guards the retime/offload false-flag
// bug: a config saved before timestampToleranceMinutes existed omits the key,
// which unmarshals to 0. Zero tolerance flags every recorder whose first-file
// time-of-day doesn't exactly tie another's, so Load must restore the default
// rather than pass 0 through.
func TestLoadBackfillsTimestampTolerance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A config predating the field: recorderSettings present, tolerance key absent.
	if err := os.WriteFile(path, []byte(`{"recorderSettings":{"autoDeleteAfterVerify":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecorderSettings.TimestampToleranceMinutes != DefaultTimestampToleranceMinutes {
		t.Fatalf("expected tolerance backfilled to %d, got %d",
			DefaultTimestampToleranceMinutes, cfg.RecorderSettings.TimestampToleranceMinutes)
	}
}

// TestExistingConfigsGetHTTP2Disabled is the mirror image of the backfill
// above: here the absent key must be left alone. HTTP/2 is off by default,
// including for anyone upgrading - they're precisely the users who have been
// losing transfers to dropped connections - so a config predating the option
// must load with HTTP2Enabled false, same as a fresh one.
func TestExistingConfigsGetHTTP2Disabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"checkers":8,"transfers":4}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP2Enabled {
		t.Error("a config predating the option loaded with HTTP/2 enabled")
	}
	if Default().HTTP2Enabled {
		t.Error("a fresh config defaults to HTTP/2 enabled")
	}
}

// TestLoadBackfillsJunkExcludeRules guards a config saved before the
// junk-filter feature existed: excludeRules is absent (unmarshals to nil),
// and Load must seed it with the default junk rules exactly once, recording
// that via JunkFilterDefaultsApplied so it never re-adds them after a user
// deliberately empties the list.
func TestLoadBackfillsJunkExcludeRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"checkers":8,"transfers":4}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DefaultFilter.ExcludeRules) != len(syncengine.DefaultJunkExcludeRules()) {
		t.Fatalf("expected %d default exclude rules backfilled, got %d",
			len(syncengine.DefaultJunkExcludeRules()), len(cfg.DefaultFilter.ExcludeRules))
	}
	if !cfg.JunkFilterDefaultsApplied {
		t.Fatal("expected JunkFilterDefaultsApplied to be set after backfill")
	}

	// A user who deliberately empties the list (and saves) must not have it
	// silently repopulated on the next load.
	cfg.DefaultFilter.ExcludeRules = nil
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.DefaultFilter.ExcludeRules) != 0 {
		t.Fatalf("expected deliberately emptied exclude rules to stay empty, got %v", reloaded.DefaultFilter.ExcludeRules)
	}
}

func TestPathIsUnderFileSyncSubdir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "config.json" {
		t.Fatalf("Path() = %q, want basename config.json", path)
	}
	if filepath.Base(filepath.Dir(path)) != "FileSync" {
		t.Fatalf("Path() = %q, want parent dir FileSync", path)
	}
}
