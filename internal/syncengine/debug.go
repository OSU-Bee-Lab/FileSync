package syncengine

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync/atomic"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/fshttp"
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

// SetHTTP2Enabled chooses between HTTP/2 and HTTP/1.1 for every remote
// (rclone's --disable-http2, inverted). FileSync defaults to HTTP/1.1.
//
// HTTP/2 multiplexes every request onto a single TCP connection, which pays
// off for workloads made of many small requests. This app's is the opposite:
// dozens of ~1 GB audio files, each a long-lived upload, where the
// per-request overhead HTTP/2 saves is negligible. What isn't negligible is
// the shared fate - when that one connection is torn down (Go's HTTP/2
// transport and SharePoint/OneDrive do this to each other routinely, giving
// "http2: client connection force closed via ClientConn.Close"), every
// in-flight transfer dies at once, and because rclone cancels an interrupted
// upload session rather than resuming it, each of those files restarts from
// byte zero. On HTTP/1.1 the transfers sit on separate connections, so a
// drop costs one file instead of Transfers-many.
//
// The rclone community has repeatedly proposed making --disable-http2 the
// default for exactly this reason; FileSync just does it.
//
// Only transports created after this call are affected: a remote already
// used this session keeps the connection it has until FileSync restarts.
func SetHTTP2Enabled(enabled bool) {
	fs.GetConfig(context.Background()).DisableHTTP2 = !enabled
	// The transport is built once, on first use, from whatever the config
	// said at that moment; without this a change made in Settings wouldn't
	// apply to any remote opened later in the same session either.
	fshttp.ResetTransport()
}
