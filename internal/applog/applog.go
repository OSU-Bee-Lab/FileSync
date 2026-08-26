// Package applog captures everything the app writes to file descriptor 2
// into a size-capped file beside the config, so a crash leaves evidence.
//
// This app is a GUI launched from a desktop icon or an app bundle: nobody
// sees stderr. A Go panic in any goroutine kills the process and writes its
// stack there and nowhere else, so before this existed a crash in the field
// left literally nothing to diagnose from - only the user's memory of what
// they were doing. Redirecting the descriptor itself (rather than assigning
// os.Stderr) is what makes that work: the Go runtime writes panic output to
// fd 2 directly, bypassing the os package entirely.
//
// rclone's own logging and Fyne's warnings land in the same file, which is
// the point - the lines just before a crash are usually what explain it.
// With DebugMode on, rclone is verbose enough to produce megabytes an hour,
// so the file is rotated at maxLogBytes: at most two files, one being
// written and one previous, capping the whole thing at 2×maxLogBytes on
// disk no matter how long the app runs.
package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OSU-Bee-Lab/filesync/internal/appconfig"
	"github.com/OSU-Bee-Lab/filesync/internal/appversion"
)

// maxLogBytes is the size at which the active log is rotated onto the
// previous one. Two files at this size is the app's whole on-disk logging
// footprint.
const maxLogBytes = 5 << 20 // 5 MiB

// rotateCheckInterval is how often the active log's size is re-checked
// while the app runs. A crash log is only useful if the app actually
// reaches the crash, so this is deliberately cheap (one Stat) and
// infrequent; the cap is a ceiling, not a precise limit.
const rotateCheckInterval = time.Minute

// Name and prevName are the two files, in the directory appconfig.Dir
// returns.
const (
	Name     = "filesync.log"
	prevName = "filesync.log.1"
)

var (
	mu      sync.Mutex
	active  *os.File // the file fd 2 currently points at
	logPath string
)

// Path returns where the log is (or would be) written, without needing
// Start to have succeeded - so the Settings screen can show the location,
// and point at it, either way.
func Path() (string, error) {
	dir, err := appconfig.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, Name), nil
}

// Start redirects stderr into the log file, rotating first if the existing
// one is already at the cap, and returns the path it's writing to. It
// starts a goroutine that keeps rotating for the life of the process.
//
// Logging is a diagnostic, never a precondition for running: if any of this
// fails (a read-only config dir, a platform with no redirect implementation)
// Start returns the error and the app carries on with stderr untouched.
func Start() (string, error) {
	dir, err := appconfig.Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	mu.Lock()
	defer mu.Unlock()

	logPath = filepath.Join(dir, Name)
	f, err := openRotated(logPath)
	if err != nil {
		return "", err
	}
	if err := redirectStderr(f); err != nil {
		f.Close()
		return "", err
	}
	active = f

	// A header per launch: without it a rotated file is a wall of context
	// -free lines, and the version matters most in exactly the case this
	// file exists for (a crash report from a machine running a build the
	// developer no longer has).
	fmt.Fprintf(f, "\n=== FileSync %s started %s ===\n",
		appversion.Version, time.Now().Format(time.RFC3339))

	go rotateLoop()
	return logPath, nil
}

// openRotated rotates path onto prevName if it's already at the cap, then
// opens it for appending.
func openRotated(path string) (*os.File, error) {
	if info, err := os.Stat(path); err == nil && info.Size() >= maxLogBytes {
		// Rename, not delete: the previous file is kept so a crash whose
		// cause scrolled past the cap is still recoverable from it. Any
		// older previous file is what gets dropped here.
		if err := os.Rename(path, filepath.Join(filepath.Dir(path), prevName)); err != nil {
			// Not fatal - worst case the file grows past the cap this
			// session. Losing the log entirely would be the worse outcome.
			fmt.Fprintf(os.Stderr, "filesync: rotating log: %v\n", err)
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// rotateLoop caps the log while the app runs, not just at startup: a single
// sync session with debug logging on can outgrow the cap several times
// over without the app ever restarting.
func rotateLoop() {
	for range time.Tick(rotateCheckInterval) {
		mu.Lock()
		f := active
		if f == nil {
			mu.Unlock()
			continue
		}
		info, err := f.Stat()
		if err != nil || info.Size() < maxLogBytes {
			mu.Unlock()
			continue
		}
		// Rotating means pointing fd 2 at a *new* file: renaming alone
		// would leave every writer (the runtime included) appending to the
		// renamed inode, and the cap would never bite.
		next, err := openRotated(logPath)
		if err != nil {
			mu.Unlock()
			continue
		}
		if err := redirectStderr(next); err != nil {
			next.Close()
			mu.Unlock()
			continue
		}
		active = next
		f.Close()
		mu.Unlock()
	}
}
