package syncengine

import (
	"context"
	"log"
	"os"
	"sync/atomic"

	"github.com/rclone/rclone/fs"
)

// debugEnabled gates both this package's own console logging and rclone's
// internal log level. It's a package-level atomic rather than something
// threaded through every call because it's a global user preference (the
// Settings screen's Debug mode checkbox), not a per-operation option.
var debugEnabled atomic.Bool

// debugLogger writes FileSync's own debug lines to stdout, separately from
// rclone's own logger (which writes to stderr - see fs/log's default
// handler), so the two interleave without one overwriting the other's
// prefix/format.
var debugLogger = log.New(os.Stdout, "[filesync debug] ", log.LstdFlags)

// SetDebugLogging turns on/off verbose console output for scan/copy
// progress and rclone's own internal logging (equivalent to rclone's -vv).
// Safe to call anytime, including while a scan or copy is in flight - the
// next log line/progress emit will reflect the new setting.
func SetDebugLogging(enabled bool) {
	debugEnabled.Store(enabled)
	level := fs.LogLevelNotice
	if enabled {
		level = fs.LogLevelDebug
	}
	fs.GetConfig(context.Background()).LogLevel = level
}

// debugf prints a line via debugLogger when debug mode is on; a no-op
// otherwise, so callers don't need to guard every call site themselves.
func debugf(format string, args ...any) {
	if debugEnabled.Load() {
		debugLogger.Printf(format, args...)
	}
}
