package recorder

import (
	"os"
	"syscall"
	"time"
)

// BestCreationTime returns the earliest of mtime/creation-time, ported
// from offload.py's best_creation_time: Windows' file creation time is a
// real, meaningful timestamp (unlike Linux's ctime).
func BestCreationTime(info os.FileInfo) time.Time {
	mtime := info.ModTime()
	wd, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return mtime
	}
	creation := time.Unix(0, wd.CreationTime.Nanoseconds())
	if creation.Before(mtime) {
		return creation
	}
	return mtime
}
