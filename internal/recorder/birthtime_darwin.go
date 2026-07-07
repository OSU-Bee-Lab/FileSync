package recorder

import (
	"os"
	"syscall"
	"time"
)

// bestCreationTime returns the earliest of mtime/birthtime, ported from
// offload.py's best_creation_time: macOS exposes a real st_birthtime.
func bestCreationTime(info os.FileInfo) time.Time {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.ModTime()
	}
	mtime := time.Unix(st.Mtimespec.Sec, st.Mtimespec.Nsec)
	birth := time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	if birth.Before(mtime) {
		return birth
	}
	return mtime
}
