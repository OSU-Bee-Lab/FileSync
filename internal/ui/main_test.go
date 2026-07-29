package ui

import (
	"context"
	"os"
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// TestMain warms rclone's lazily-initialized global config before any test can
// reach it from two goroutines at once - see the fuller note on
// internal/syncengine's TestMain. The audio preview tests start and stop
// playback in quick succession, which can have two streams opening at the same
// time, and the first of those to touch rclone would otherwise be racing.
//
// The warm-up goes through syncengine's own API rather than rclone's, keeping
// this package free of direct rclone imports.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "filesync-config-warmup")
	if err == nil {
		loc := syncengine.LocalFolderLocation("warmup", dir)
		_, _ = syncengine.ListChildren(context.Background(), loc, "")
		os.RemoveAll(dir)
	}
	os.Exit(m.Run())
}
