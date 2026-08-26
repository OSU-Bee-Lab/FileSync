package applog

import (
	"os"
	"syscall"
)

// redirectStderr points file descriptor 2 at f, so writes that bypass the
// os package - the Go runtime's panic output above all - land in the log
// too. os.Stderr keeps working unchanged: it wraps fd 2, which is now this
// file.
func redirectStderr(f *os.File) error {
	return syscall.Dup2(int(f.Fd()), int(os.Stderr.Fd()))
}
