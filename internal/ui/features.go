package ui

import "os"

// devMode gates functionality that is incomplete or still being stabilized
// and should stay hidden from release builds. Set the env var to any
// non-empty value to reveal dev-only features at runtime without rebuilding:
//
//	FILESYNC_DEV=1 go run .
func devMode() bool {
	return os.Getenv("FILESYNC_DEV") != ""
}
