// Command expsync is a cross-platform GUI for schema-scoped rclone
// backup/sync and download of bioacoustics experiment data.
package main

import (
	_ "github.com/OSU-Bee-Lab/expsync/internal/rcbackends"
	"github.com/OSU-Bee-Lab/expsync/internal/ui"
)

func main() {
	ui.Run()
}
