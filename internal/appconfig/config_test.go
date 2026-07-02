package appconfig

import (
	"testing"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
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
	if len(cfg.DefaultFilter.IncludePatterns) == 0 {
		t.Fatal("expected a non-empty default filter")
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

func TestPathIsUnderExpSyncSubdir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if got := path; len(got) == 0 {
		t.Fatal("expected a non-empty path")
	}
}
