package syncengine

import (
	"context"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/filter"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/fs/sync"
	"github.com/rclone/rclone/lib/random"
)

// RetriesUnlimited is the CopyRetries value meaning "never give up on a
// transient error" — the default. It exists for the overnight-upload case:
// if the network drops for an hour at 2am, the right behaviour is to keep
// picking back up until the copy gets through, not to error out and leave
// the user to discover a half-finished sync in the morning.
//
// Unlimited doesn't mean "never fail": copyDirWithRetry still stops on
// anything rclone marks fatal or non-retriable, and an error that is
// retriable but doesn't look like a network problem gets only
// unlimitedNonTransientAttempts tries. The backoff caps at
// copyRetryMaxInterval so a waiting copy idles rather than spins, and
// cancelling the job always breaks out immediately.
const RetriesUnlimited = 0

// DefaultCopyRetries is the retry count a fresh config starts with.
const DefaultCopyRetries = RetriesUnlimited

// copyRetryInitialInterval is the pause before the first retry;
// copyRetryMaxInterval caps the exponential backoff applied to each
// subsequent one. The cap keeps a long outage cheap (a wake-up every few
// minutes) while still resuming promptly once the network returns — an
// attempt that manages to transfer anything resets the backoff.
const (
	copyRetryInitialInterval = 5 * time.Second
	copyRetryMaxInterval     = 5 * time.Minute
)

// copyRetries is the number of times a whole-directory copy is attempted
// before a transient error (e.g. a dropped HTTP/2 connection) is reported to
// the UI as a failure, or RetriesUnlimited to keep retrying. This is the
// outer retry loop the rclone CLI runs (its --retries flag), which FileSync
// doesn't get for free since it calls sync.CopyDir directly instead of going
// through rclone's cmd.Run.
var copyRetries = DefaultCopyRetries

// SetCopyRetries sets how many attempts a copy gets before a transient error
// becomes a failure. n <= 0 means unlimited (RetriesUnlimited). Like
// SetTransfers/SetCheckers this is a process-global user preference, applied
// at startup and whenever Settings changes it; a copy already running reads
// it per-attempt, so a change takes effect on the next retry.
func SetCopyRetries(n int) {
	if n < 0 {
		n = RetriesUnlimited
	}
	copyRetries = n
}

// extraRetriablePhrases lists error text that means "the network dropped this
// request mid-flight" but that rclone's own fserrors.ShouldRetry does not
// recognise. rclone matches a hand-maintained list of phrases
// (fserrors.retriableErrorStrings) plus errors implementing Timeout()/
// Temporary(); several routine cloud-transfer failures satisfy neither, most
// notably Go's HTTP/2 transport tearing down a whole connection:
//
//	Put "https://...sharepoint.com/...": http2: client connection force
//	closed via ClientConn.Close
//
// That is a plain errors.New wrapped in a *url.Error, so ShouldRetry returns
// false and a single dropped connection ends the whole copy — even though
// re-running it is exactly what fixes it. Everything listed here is safe to
// retry: CopyDir never deletes, and rclone only publishes a destination file
// once it has been written in full (upload sessions for cloud backends, a
// .partial name plus rename for local ones), so a retry just re-sends what
// didn't land.
var extraRetriablePhrases = []string{
	"http2:",                   // e.g. "client connection force closed via ClientConn.Close"
	"connection reset by peer", // TCP reset mid-transfer
	"broken pipe",
	"unexpected EOF",
	"i/o timeout",
	"TLS handshake timeout",
	"Client.Timeout exceeded", // net/http's own request-timeout wording
	"context deadline exceeded",
	"server misbehaving", // transient DNS failure (e.g. laptop resuming from sleep)
}

// shouldRetryCopy reports whether an error looks like a transient network
// failure - the question copyDirWithRetry asks before letting an unlimited
// retry budget keep going indefinitely (whether to retry *at all* is decided
// from the job's accounting stats, as rclone's own CLI does).
// It defers to rclone's own judgement first and then widens it with
// extraRetriablePhrases. String matching is unlovely, but it's the same
// mechanism rclone itself uses for this class of error (its own comment on
// retriableErrorStrings calls it "incredibly ugly") — the underlying errors
// are unexported stdlib values with no type to assert on.
func shouldRetryCopy(err error) bool {
	if err == nil {
		return false
	}
	if fserrors.ShouldRetry(err) {
		return true
	}
	msg := err.Error()
	for _, phrase := range extraRetriablePhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// JobStatus is the lifecycle state of a running copy.
type JobStatus int

const (
	JobRunning JobStatus = iota
	JobDone
	JobError
	JobCanceled
)

// FileProgress tracks progress of a single file.
type FileProgress struct {
	BytesDone int64
	Done      bool
	Err       error
}

// ProgressSnapshot is one point-in-time update on a running Job.
type ProgressSnapshot struct {
	BytesDone, BytesTotal int64
	FilesDone, FilesTotal int
	CurrentFile           string
	Status                JobStatus
	Err                   error
	Speed                 float64

	// Files is a lossy, windowed view of per-file progress: it's built from
	// rclone's stats.Transferred() (only the last ~100 completed transfers
	// are retained) plus stats.RemoteStats' "transferring" list, which
	// collapses up to ci.Transfers concurrent in-flight files down to a
	// single CurrentFile name. Consumers must treat a file's completion as
	// sticky once seen and must not assume Files reflects every file in the
	// job - older completions can fall out of the window, and concurrent
	// in-flight files beyond the first aren't individually named here.
	Files map[string]FileProgress

	// Retrying is true while the job is paused between attempts after a
	// transient error (see copyDirWithRetry). The job is still JobRunning
	// at this point - it isn't a failure until every retry is exhausted -
	// so callers should show this as a "will resolve itself" notice
	// rather than an error state. RetryMax is RetriesUnlimited when the
	// copy has no attempt limit, so a UI showing "attempt N of M" must
	// handle that case (see retryBudgetLabel).
	Retrying               bool
	RetryAttempt, RetryMax int
	RetryErr               error
}

// Job is a running (or finished) copy started by StartSyncExperiments/StartPullFiles.
// cancel derives startCopyPreserving's working context from the ctx its
// caller passed in; a caller cancels a running Job by canceling that same
// ctx it originally passed to Start*, not through a method on Job (rclone's
// fs/sync and fs/operations check ctx.Err() between file operations, so this
// stops the copy promptly, leaving whatever already completed in place —
// never a partial file, since rclone copies to a temp name and renames on
// completion).
type Job struct {
	cancel context.CancelFunc
}

// StartSyncExperiments copies one whole experiment from src to dst (Location <->
// Location, mirrored under each side's own experiments/ root). expected
// should be the ScanResult the user already confirmed, used to seed the
// progress bar's totals — the same relPath/filter that produced it is what
// actually runs here, so the set of files acted on can't drift from what
// was shown.
func StartSyncExperiments(ctx context.Context, src, dst Location, experimentName string, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	return startCopyPreserving(ctx, src.rcloneSpec(), dst.rcloneSpec(), experimentName, experimentName, expected)
}

// StartPullFiles copies an arbitrary sub-path (or, when srcFiles is
// non-empty, one or more individually selected files) from src into
// destFolder. When fullIdent is true, the chosen scope's structure is
// preserved under destFolder (e.g. pulling "Luke - Zucchini/2026-06-23" into
// "/Downloads/foo" lands at "/Downloads/foo/Luke - Zucchini/2026-06-23/...");
// when false, the copy is flattened to just the path beneath the scope
// (lands directly under "/Downloads/foo/..."). destFolder is a raw local
// path, never a saved Location. srcFiles must match what was passed to the
// preceding ScanPullFilesWithProgress call - see its doc comment for why a
// file selection needs the scope resolved to CommonDir(srcFiles) instead.
func StartPullFiles(ctx context.Context, src Location, srcRelPath string, srcFiles []string, destFolder string, fullIdent bool, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	scopeRelPath := srcRelPath
	if len(srcFiles) > 0 {
		scopeRelPath = CommonDir(srcFiles)
	}
	dstRelPath := ""
	if fullIdent {
		dstRelPath = scopeRelPath
	}
	return startCopyPreserving(ctx, src.rcloneSpec(), destFolder, scopeRelPath, dstRelPath, expected)
}

// filesFromFilter builds an rclone filter that restricts the copy to
// exactly the files the scan identified as needing transfer. This
// avoids the redundant source+destination traversal that would otherwise
// repeat the work the scan already did. Returns nil when no files
// need copying (the caller should still proceed — CopyDir is a no-op
// when nothing matches).
func filesFromFilter(expected ScanResult) *filter.Filter {
	if expected.CopyCount == 0 {
		return nil
	}
	f, _ := filter.NewFilter(nil) // no opts → can't error
	for _, entry := range expected.Entries {
		if entry.Action == ActionCopy {
			_ = f.AddFile(entry.RelPath) // path-only, can't error
		}
	}
	return f
}

// unlimitedNonTransientAttempts bounds the one case an unlimited retry
// budget would otherwise spin on forever: an error rclone is willing to
// retry but that doesn't look like a network problem (shouldRetryCopy says
// no). Those are usually something only the user can fix - a file the remote
// keeps rejecting, a full disk - so they get a few attempts and then a real
// error message, rather than a progress bar that never finishes and never
// explains itself. Dropped connections stay unlimited.
const unlimitedNonTransientAttempts = 3

// speedWindow is the span the displayed transfer speed averages over. Any
// change in the real rate (including a drop to 0 once bytes stop moving) is
// fully reflected after this long, regardless of how big the change was.
const speedWindow = 3 * time.Second

// speedAverager reports transfer speed as the average rate across the last
// speedWindow, computed from the cumulative byte counter rather than from
// per-tick deltas. Averaging a fixed span keeps the number readable without
// the settling time depending on the size of the change - an exponential
// moving average takes longer to come down from 100MB/s than from 1MB/s and
// reads as arbitrary flicker.
type speedAverager struct {
	samples []speedSample
}

type speedSample struct {
	at    time.Time
	bytes int64
}

// observe records the cumulative byte count at a point in time and returns
// the current speed in bytes/sec. The oldest sample still inside the window
// is the baseline; samples that fall out are dropped, except that the last
// one out is kept until a newer sample is itself old enough to replace it,
// so the average always spans the full window rather than collapsing to the
// gap between the two newest ticks.
func (s *speedAverager) observe(at time.Time, bytes int64) float64 {
	s.samples = append(s.samples, speedSample{at: at, bytes: bytes})
	cutoff := at.Add(-speedWindow)
	for len(s.samples) > 1 && !s.samples[1].at.After(cutoff) {
		s.samples = s.samples[1:]
	}
	oldest := s.samples[0]
	elapsed := at.Sub(oldest.at).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(bytes-oldest.bytes) / elapsed
}

// copyDirWithRetry runs sync.CopyDir, retrying until it succeeds, hits an
// error retrying can't fix, exhausts the configured attempt budget, or the
// job is cancelled. Files already copied in a failed attempt are left in
// place (CopyDir never deletes), so a retry just picks up whatever the
// destination is still missing - it isn't redone from scratch, though a
// retried attempt does re-list source and destination.
//
// The retry decision mirrors rclone's own CLI loop (cmd.Run): it asks the
// job's accounting stats, which is where every per-file error has already
// been classified as fatal / no-retry / retriable, rather than re-judging
// the single error CopyDir happens to return. That matters because CopyDir
// returns a summary ("failed to copy: N errors") whose text says nothing
// about the underlying failures. shouldRetryCopy is then used only to
// decide whether an unlimited budget really means unlimited, and a
// server-supplied Retry-After (rclone's usual answer to being throttled) is
// honoured over our own backoff.
//
// That backoff is exponential, capped at copyRetryMaxInterval, and resets as
// soon as an attempt transfers anything: a long outage settles into a cheap
// every-few-minutes poll, but the moment the network is back the copy is
// running at full tilt again.
//
// onRetry, if non-nil, is called just before each retry sleep so a caller
// can surface a "retrying" indicator in the UI without the error being
// treated as a hard failure - the copy is still in flight, not stalled. max
// is passed through as RetriesUnlimited when there's no attempt limit.
func copyDirWithRetry(ctx context.Context, fdst, fsrc fs.Fs, onRetry func(attempt, max int, err error)) error {
	max := copyRetries
	stats := accounting.Stats(ctx)
	wait := copyRetryInitialInterval
	var err error
	for attempt := 1; max <= RetriesUnlimited || attempt <= max; attempt++ {
		bytesBefore := stats.GetBytes()

		err = fs.CountError(ctx, sync.CopyDir(ctx, fdst, fsrc, false))
		// A per-file failure doesn't always come back as CopyDir's return
		// value, so take the stats' last error when it doesn't - the same
		// substitution cmd.Run makes.
		if err == nil {
			err = stats.GetLastError()
		}
		if ctx.Err() != nil {
			return err
		}
		if !stats.Errored() {
			return err // normally nil; non-nil only for an error rclone chose not to count
		}
		switch {
		case stats.HadFatalError():
			debugf("copy %s -> %s: fatal error, not retrying: %v", fsrc.Root(), fdst.Root(), err)
			return err
		case !stats.HadRetryError():
			debugf("copy %s -> %s: no retriable errors, not retrying: %v", fsrc.Root(), fdst.Root(), err)
			return err
		case max > RetriesUnlimited && attempt == max:
			return err
		case max <= RetriesUnlimited && attempt >= unlimitedNonTransientAttempts &&
			!shouldRetryCopy(err) && !shouldRetryCopy(stats.GetLastError()):
			debugf("copy %s -> %s: %d attempts, error doesn't look transient, giving up: %v", fsrc.Root(), fdst.Root(), attempt, err)
			return err
		}

		if stats.GetBytes() > bytesBefore {
			wait = copyRetryInitialInterval // made progress: don't punish the next attempt
		}
		if retryAfter := stats.RetryAfter(); !retryAfter.IsZero() {
			if d := time.Until(retryAfter); d > wait {
				wait = d // the remote told us how long to back off; believe it
			}
		}
		debugf("copy %s -> %s: attempt %d/%s failed with %d errors, retrying in %s: %v", fsrc.Root(), fdst.Root(), attempt, retryBudgetLabel(max), stats.GetErrors(), wait, err)
		if onRetry != nil {
			onRetry(attempt, max, err)
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return err
		}
		if wait *= 2; wait > copyRetryMaxInterval {
			wait = copyRetryMaxInterval
		}
		// Errors are per-attempt: without this reset the stats stay in the
		// errored state from a failure the next attempt may well fix, and
		// every later attempt would look like it failed too.
		stats.ResetErrors()
		if onRetry != nil {
			onRetry(attempt, max, nil) // resumed: clear the "retrying" indicator
		}
	}
	return err
}

// retryBudgetLabel renders an attempt budget for debug logging, spelling out
// the unlimited case rather than printing "0".
func retryBudgetLabel(max int) string {
	if max <= RetriesUnlimited {
		return "unlimited"
	}
	return strconv.Itoa(max)
}

// startCopyPreserving is the one place sync.CopyDir is called in this
// codebase. It must never be swapped for sync.Sync, which deletes
// destination-only files - see TestCopyPreserving_NeverDeletesDestinationOnlyFiles.
//
// When expected contains files to copy (CopyCount > 0), the scan's
// file list is used to build a files-from filter so rclone copies only
// those files without re-scanning source or destination. This turns the
// scan→copy round-trip from O(2×listing) into O(listing + copies).
func startCopyPreserving(parent context.Context, srcRoot, dstRoot, srcRelPath, dstRelPath string, expected ScanResult) (*Job, <-chan ProgressSnapshot) {
	ctx, cancel := context.WithCancel(parent)
	progress := make(chan ProgressSnapshot, 1)

	// filesFromFilter returns nil exactly when the scan the user confirmed
	// found nothing to copy. Return a Done snapshot immediately in that case:
	// running CopyDir "to confirm the no-op" would re-list both trees and fall
	// back to rclone's native size+modtime equality — hashing whole audio
	// files over a cloud remote on any modtime drift, the exact cost the
	// scan's size+prefix check exists to avoid.
	cachedFilter := filesFromFilter(expected)
	if cachedFilter == nil {
		progress <- ProgressSnapshot{Status: JobDone}
		close(progress)
		return &Job{cancel: cancel}, progress
	}

	ctx, ci := fs.AddConfig(ctx)

	// Copy only the files the scan identified, via a precise files-from list
	// with destination listing disabled — the user's FilterSettings were
	// already applied during the scan, so no filter or full traversal is
	// needed here.
	ctx = filter.ReplaceConfig(ctx, cachedFilter)
	ci.NoTraverse = true

	groupName := "filesync-job-" + random.String(8)
	ctx = accounting.WithStatsGroup(ctx, groupName)

	job := &Job{cancel: cancel}

	go func() {
		defer close(progress)

		fsrc, err := cache.Get(ctx, joinSpec(srcRoot, srcRelPath))
		if err != nil {
			progress <- ProgressSnapshot{Status: JobError, Err: err, FilesTotal: expected.CopyCount, BytesTotal: expected.TotalBytes}
			return
		}
		fdst, err := cache.Get(ctx, joinSpec(dstRoot, dstRelPath))
		if err != nil {
			progress <- ProgressSnapshot{Status: JobError, Err: err, FilesTotal: expected.CopyCount, BytesTotal: expected.TotalBytes}
			return
		}

		debugf("copy %s -> %s: starting, %d files queued", fsrc.Root(), fdst.Root(), expected.CopyCount)
		done := make(chan error, 1)

		var retryMu stdsync.Mutex
		var retrying bool
		var retryAttempt, retryMax int
		var retryErr error
		onRetry := func(attempt, max int, err error) {
			retryMu.Lock()
			retrying, retryAttempt, retryMax, retryErr = err != nil, attempt, max, err
			retryMu.Unlock()
		}

		go func() { done <- copyDirWithRetry(ctx, fdst, fsrc, onRetry) }()

		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		stats := accounting.Stats(ctx)

		var speed speedAverager

		emit := func(status JobStatus, err error) {
			currentBytes := stats.GetBytes()
			currentSpeed := speed.observe(time.Now(), currentBytes)

			filesMap := make(map[string]FileProgress)
			for _, t := range stats.Transferred() {
				filesMap[t.Name] = FileProgress{
					BytesDone: t.Bytes,
					// An errored transfer must never report Done=true - a
					// caller tallying filesDone would otherwise count a
					// failed file as if it succeeded. The error is still
					// carried via Err below (see hasError downstream).
					// Note t.Error==nil must gate the whole expression: rclone
					// sets CompletedAt unconditionally in Transfer.Done (even
					// on failure), so "!t.CompletedAt.IsZero()" alone is true
					// for errored transfers too.
					Done: t.Error == nil && (!t.CompletedAt.IsZero() || t.Bytes == t.Size),
					Err:  t.Error,
				}
			}

			// Transferred() above only reports completed transfers, so a
			// file actively being copied would otherwise sit at 0 bytes
			// until it finishes (the bar looked like it jumped 0%→100%).
			// RemoteStats' "transferring" list carries live in-progress
			// byte counts for files currently being copied.
			var current string
			if remoteStats, rcErr := stats.RemoteStats(false); rcErr == nil {
				if transferring, ok := remoteStats["transferring"].([]rc.Params); ok {
					for _, t := range transferring {
						name, _ := t["name"].(string)
						bytesDone, _ := t["bytes"].(int64)
						if name == "" {
							continue
						}
						filesMap[name] = FileProgress{BytesDone: bytesDone}
						current = name
					}
				}
			}

			if current != "" {
				debugf("copy %s -> %s: transferring %s (%d/%d bytes)", fsrc.Root(), fdst.Root(), current, currentBytes, expected.TotalBytes)
			}

			retryMu.Lock()
			snapRetrying, snapAttempt, snapMax, snapRetryErr := retrying, retryAttempt, retryMax, retryErr
			retryMu.Unlock()

			// copyDirWithRetry re-runs sync.CopyDir on the same stats group
			// after a transient error, so a file that partially transferred
			// before the drop gets fully re-sent on retry; stats.GetBytes()
			// is cumulative and never subtracts the abandoned partial, so it
			// can climb past expected.TotalBytes. Clamp only the reported
			// value here - currentSpeed above legitimately uses the raw,
			// unclamped currentBytes deltas.
			reportedBytesDone := currentBytes
			if reportedBytesDone > expected.TotalBytes {
				reportedBytesDone = expected.TotalBytes
			}

			progress <- ProgressSnapshot{
				BytesDone:    reportedBytesDone,
				BytesTotal:   expected.TotalBytes,
				FilesDone:    int(stats.GetTransfers()),
				FilesTotal:   expected.CopyCount,
				CurrentFile:  current,
				Status:       status,
				Err:          err,
				Speed:        currentSpeed,
				Files:        filesMap,
				Retrying:     snapRetrying,
				RetryAttempt: snapAttempt,
				RetryMax:     snapMax,
				RetryErr:     snapRetryErr,
			}
		}

		for {
			select {
			case err := <-done:
				status := JobDone
				if err != nil {
					status = JobError
					if ctx.Err() != nil {
						status = JobCanceled
					}
				}
				debugf("copy %s -> %s: finished, status=%v err=%v", fsrc.Root(), fdst.Root(), status, err)
				emit(status, err)
				return
			case <-ticker.C:
				emit(JobRunning, nil)
			}
		}
	}()

	return job, progress
}
