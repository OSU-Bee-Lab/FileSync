package recorder

import (
	"os"
	"syscall"
	"time"
)

// BestCreationTime returns the earliest of mtime/ctime, ported from
// offload.py's best_creation_time: Linux doesn't expose a real birthtime
// via stat(), so ctime is the closest available proxy.
func BestCreationTime(info os.FileInfo) time.Time {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.ModTime()
	}
	mtime := time.Unix(st.Mtim.Sec, st.Mtim.Nsec)
	ctime := time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
	if ctime.Before(mtime) {
		return ctime
	}
	return mtime
}
