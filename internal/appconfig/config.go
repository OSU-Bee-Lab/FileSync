// Package appconfig persists ExpSync's own settings - the list of
// configured Locations and a couple of defaults. It is deliberately
// separate from rclone's own config file (which holds remote
// credentials/secrets and is left at rclone's default path, untouched -
// see internal/syncengine/remote.go) so that a collaborator's list of
// storage locations, which legitimately differs per machine, never
// comingles with credential material.
package appconfig

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

const currentVersion = 5

// RecorderSettings persists the Sync Recorders feature's defaults and
// its tag-file ID-assignment state (batch/counter scheme — see
// internal/recorder/identity.go), so recorder IDs stay stable across runs.
type RecorderSettings struct {
	DestinationLocationIDs []string       `json:"destinationLocationIds,omitempty"`
	UploadLocationIDs      []string       `json:"uploadLocationIds,omitempty"`
	AutoDeleteAfterVerify  bool           `json:"autoDeleteAfterVerify"`
	TagBatch               int            `json:"tagBatch"`
	TagCounters            map[string]int `json:"tagCounters,omitempty"`
}

// Config is ExpSync's entire persisted app state.
type Config struct {
	Version          int                       `json:"version"`
	Locations        []syncengine.Location     `json:"locations"`
	DefaultFilter    syncengine.FilterSettings `json:"defaultFilter"`
	PreserveModTime  bool                      `json:"preserveModTime"`
	RecorderSettings RecorderSettings          `json:"recorderSettings"`
}

// Default returns the config used the first time ExpSync runs on a
// machine, before any Locations have been added.
func Default() Config {
	return Config{
		Version:          currentVersion,
		DefaultFilter:    syncengine.DefaultFilterSettings(),
		PreserveModTime:  true,
		RecorderSettings: RecorderSettings{TagBatch: 1},
	}
}

// Path returns the OS-appropriate location for ExpSync's config file
// (e.g. ~/.config/ExpSync/config.json on Linux, ~/Library/Application
// Support/ExpSync/config.json on macOS, %AppData%\ExpSync\config.json on
// Windows), via os.UserConfigDir so no path is ever hardcoded.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ExpSync", "config.json"), nil
}

// Load reads the config file, returning Default() if it doesn't exist yet
// (first run).
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Version < 2 && len(cfg.DefaultFilter.IncludePatterns) == 1 && cfg.DefaultFilter.IncludePatterns[0] == "*.mp3" {
		cfg.DefaultFilter = syncengine.DefaultFilterSettings()
	}
	if cfg.Version < 3 && cfg.RecorderSettings.TagBatch == 0 {
		cfg.RecorderSettings.TagBatch = 1
	}
	if cfg.Version < 4 {
		// Enabled is new in v4; configs written before it exist have every
		// location unmarshal to Enabled: false, which would silently
		// disable them all. Back-fill true so upgrading never disables an
		// existing location.
		for i := range cfg.Locations {
			cfg.Locations[i].Enabled = true
		}
	}
	if cfg.Version < 5 && len(cfg.RecorderSettings.DestinationLocationIDs) == 0 {
		// DestinationLocationID (singular) became DestinationLocationIDs
		// (plural, multi-destination) in v5; without this, a config written
		// by v4 would silently lose its chosen destination on upgrade.
		var legacy struct {
			RecorderSettings struct {
				DestinationLocationID string `json:"destinationLocationId"`
			} `json:"recorderSettings"`
		}
		if err := json.Unmarshal(data, &legacy); err == nil && legacy.RecorderSettings.DestinationLocationID != "" {
			cfg.RecorderSettings.DestinationLocationIDs = []string{legacy.RecorderSettings.DestinationLocationID}
		}
	}
	if cfg.Version < currentVersion {
		cfg.Version = currentVersion
	}
	return cfg, nil
}

// Save writes the config file, creating its parent directory if needed.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
