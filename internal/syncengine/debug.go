package syncengine

import (
	"context"
	"fmt"
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

// DefaultCheckers mirrors rclone's own --checkers default (rclone doesn't
// export its default as a constant), used when the user hasn't overridden
// it in Settings.
const DefaultCheckers = 8

// SetCheckers sets rclone's --checkers value (how many file-comparison
// checks run concurrently during a scan/copy). n must be >= 1; n <= 0 is a
// caller bug and falls back to DefaultCheckers rather than passing a bogus
// value to rclone. Like SetDebugLogging, this mutates rclone's
// process-global config rather than a per-operation context, since every
// scan/copy in this app already shares one rclone process and this is a
// single user preference, not a per-call option.
func SetCheckers(n int) {
	if n <= 0 {
		n = DefaultCheckers
	}
	fs.GetConfig(context.Background()).Checkers = n
}

// SetBwLimitMiBPerSec caps rclone's transfer bandwidth in MiB/s across all
// scan/copy operations. mib <= 0 removes the limit.
func SetBwLimitMiBPerSec(mib int) {
	tt := fs.GetConfig(context.Background()).BwLimit
	if mib <= 0 {
		_ = tt.Set("off")
	} else {
		_ = tt.Set(fmt.Sprintf("%dMi", mib))
	}
	fs.GetConfig(context.Background()).BwLimit = tt
}

// DefaultTransfers mirrors rclone's own --transfers default (rclone doesn't
// export its default as a constant), used when the user hasn't overridden
// it in Settings.
const DefaultTransfers = 4

// SetTransfers sets rclone's --transfers value (how many files are copied
// concurrently within a single scan/copy job). n must be >= 1; n <= 0 is a
// caller bug and falls back to DefaultTransfers rather than passing a bogus
// value to rclone. Like SetCheckers, this mutates rclone's process-global
// config, which each job's context then clones via fs.AddConfig (see
// startCopyPreserving in job.go).
func SetTransfers(n int) {
	if n <= 0 {
		n = DefaultTransfers
	}
	fs.GetConfig(context.Background()).Transfers = n
}
