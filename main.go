// Command filesync is a cross-platform GUI for schema-scoped rclone
// syncing and pulling of bioacoustics experiment data.
package main

import (
	"fmt"
	"os"

	"github.com/OSU-Bee-Lab/filesync/internal/applog"
	_ "github.com/OSU-Bee-Lab/filesync/internal/rcbackends"
	_ "github.com/OSU-Bee-Lab/filesync/internal/recorder/drivers"
	"github.com/OSU-Bee-Lab/filesync/internal/ui"
	"github.com/rclone/rclone/fs/config/configfile"
)

func main() {
	// First, before anything can crash or log: this app runs from a
	// desktop icon with nowhere for stderr to go, so a panic (which the Go
	// runtime writes there and nowhere else) would otherwise leave nothing
	// behind to diagnose. Failing to set it up is never a reason not to
	// start - the message just goes to the stderr we couldn't capture.
	if _, err := applog.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "filesync: crash logging unavailable: %v\n", err)
	}

	// Without this, rclone's config package falls back to an in-memory-only
	// stub whose Load/Save are no-ops, so remotes created via the wizard
	// would vanish the moment the app is relaunched.
	configfile.Install()
	ui.Run()
}
