package syncengine

import (
	"context"
	"os"
	"testing"
)

// TestMain warms rclone's lazily-initialized global config on a single
// goroutine before any test can reach it.
//
// rclone loads that config the first time anything calls fs.NewFs, and the
// load is not safe against concurrent first callers - `go test -race` reports
// a data race inside rclone's own config.LoadedData when two goroutines get
// there together. Several tests here do exactly that, since scans list
// locations in parallel, so whether the race fires depends on which test runs
// first in a given invocation (running one scan test on its own is enough).
//
// The app doesn't hit this because main.go installs the config file before
// starting the UI. Tests deliberately don't install it - that would read the
// developer's own rclone config - so they warm it with a throwaway listing
// against an empty directory instead.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "filesync-config-warmup")
	if err == nil {
		_, _ = ListChildren(context.Background(), localLoc(dir), "")
		os.RemoveAll(dir)
	}
	os.Exit(m.Run())
}
