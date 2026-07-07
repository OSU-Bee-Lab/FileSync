// Command expsync is a cross-platform GUI for schema-scoped rclone
// syncing and pulling of bioacoustics experiment data.
package main

import (
	_ "github.com/OSU-Bee-Lab/expsync/internal/rcbackends"
	"github.com/OSU-Bee-Lab/expsync/internal/ui"
	"github.com/rclone/rclone/fs/config/configfile"
)

func main() {
	// Without this, rclone's config package falls back to an in-memory-only
	// stub whose Load/Save are no-ops, so remotes created via the wizard
	// would vanish the moment the app is relaunched.
	configfile.Install()
	ui.Run()
}
