// Package appconfig persists FileSync's own settings - the list of
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

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// RecorderSettings persists the Sync Recorders feature's defaults.
type RecorderSettings struct {
	DestinationLocationIDs []string `json:"destinationLocationIds,omitempty"`
	UploadLocationIDs      []string `json:"uploadLocationIds,omitempty"`
	AutoDeleteAfterVerify  bool     `json:"autoDeleteAfterVerify"`
	// Subpaths is an optional path prepended under each destination's root
	// (before experimentName/recorderID) - recorders are almost never
	// synced straight to a destination's root folder. Keyed by experiment
	// name, since different experiments commonly use different subpaths
	// (e.g. different deployment dates/sites).
	Subpaths map[string]string `json:"subpaths,omitempty"`
}

// Config is FileSync's entire persisted app state.
type Config struct {
	Locations        []syncengine.Location     `json:"locations"`
	DefaultFilter    syncengine.FilterSettings `json:"defaultFilter"`
	RecorderSettings RecorderSettings          `json:"recorderSettings"`
	// DebugMode enables verbose console logging of scan/copy progress and
	// rclone's own internal logging, for troubleshooting a stuck or slow
	// sync without adding a separate CLI surface.
	DebugMode bool `json:"debugMode"`
	// RecorderInactivityTimeoutMinutes is how long showRecorderSync waits,
	// with no recorder actively syncing, before prompting to end the
	// Recorder Sync session. See internal/ui.recorderInactivityTimeout.
	RecorderInactivityTimeoutMinutes int `json:"recorderInactivityTimeoutMinutes"`
	// Checkers is rclone's --checkers value: how many file-comparison
	// checks run concurrently during a scan/copy. 0 means "use rclone's
	// own default" (currently 8).
	Checkers int `json:"checkers"`
	// BwLimitMiBPerSec caps rclone's transfer bandwidth in MiB/s. 0 means
	// unlimited.
	BwLimitMiBPerSec int `json:"bwLimitMiBPerSec"`
}

// Default returns the config used the first time FileSync runs on a
// machine, before any Locations have been added.
func Default() Config {
	return Config{
		DefaultFilter: syncengine.DefaultFilterSettings(),
		RecorderSettings: RecorderSettings{
			AutoDeleteAfterVerify: true,
		},
		RecorderInactivityTimeoutMinutes: 5,
	}
}

// Path returns the OS-appropriate location for FileSync's config file
// (e.g. ~/.config/FileSync/config.json on Linux, ~/Library/Application
// Support/FileSync/config.json on macOS, %AppData%\FileSync\config.json on
// Windows), via os.UserConfigDir so no path is ever hardcoded.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "FileSync", "config.json"), nil
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
