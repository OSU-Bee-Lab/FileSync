package syncengine

import (
	"context"
	"errors"

	"net/url"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/fserrors"
)

// The error rclone surfaces when Go's HTTP/2 transport tears down a
// connection mid-upload. Reproduced in the shape it actually arrives in (a
// *url.Error wrapping an unexported stdlib error) rather than as a flat
// string, since that shape is exactly why fserrors.ShouldRetry misses it.
func http2CloseErr() error {
	return &url.Error{
		Op:  "Put",
		URL: "https://osu.sharepoint.com/_api/v2.0/drive/items/01ABC/uploadSession?guid=x&tempauth=eyJ0eX",
		Err: errors.New("http2: client connection force closed via ClientConn.Close"),
	}
}

func TestShouldRetryCopy_CoversErrorsRcloneMisses(t *testing.T) {
	err := http2CloseErr()
	if fserrors.ShouldRetry(err) {
		t.Skip("rclone now classifies this itself - extraRetriablePhrases may be able to drop the http2 entry")
	}
	if !shouldRetryCopy(err) {
		t.Fatalf("shouldRetryCopy(%v) = false, want true: a dropped HTTP/2 connection must not fail the whole copy", err)
	}
}

// timeoutErr satisfies the Timeout() interface fserrors.Cause looks for,
// without any of the text extraRetriablePhrases matches on - so it only
// passes shouldRetryCopy if rclone's own judgement is being consulted.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "the operation timed out" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestShouldRetryCopy_DefersToRclone(t *testing.T) {
	if !fserrors.ShouldRetry(timeoutErr{}) {
		t.Fatal("test premise broken: rclone no longer treats a Timeout() error as retriable")
	}
	if !shouldRetryCopy(timeoutErr{}) {
		t.Fatal("shouldRetryCopy dropped an error rclone considers retriable")
	}
}

func TestShouldRetryCopy_PermanentErrorsAreNotRetried(t *testing.T) {
	// Retrying these forever would spin instead of telling the user what to
	// fix - the whole reason unlimited retries are safe as a default.
	permanent := []error{
		nil,
		errors.New("couldn't fetch token: invalid_grant: maybe token expired?"),
		errors.New("quotaLimitReached: the account is out of storage"),
		errors.New("accessDenied: you do not have permission to perform this action"),
		errors.New("invalidRequest: the specified name is not valid"),
	}
	for _, err := range permanent {
		if shouldRetryCopy(err) {
			t.Errorf("shouldRetryCopy(%v) = true, want false", err)
		}
	}
}

func TestSetCopyRetries(t *testing.T) {
	t.Cleanup(func() { SetCopyRetries(DefaultCopyRetries) })

	SetCopyRetries(5)
	if copyRetries != 5 {
		t.Errorf("copyRetries = %d, want 5", copyRetries)
	}
	// Both 0 and a nonsense negative mean "no limit" rather than "give up
	// immediately", which would silently disable retrying altogether.
	for _, n := range []int{0, -3} {
		SetCopyRetries(n)
		if copyRetries != RetriesUnlimited {
			t.Errorf("SetCopyRetries(%d): copyRetries = %d, want RetriesUnlimited", n, copyRetries)
		}
	}
}

func TestSetHTTP2Enabled(t *testing.T) {
	t.Cleanup(func() { SetHTTP2Enabled(false) })

	// The config flag is rclone's --disable-http2, i.e. the inverse of ours;
	// getting the polarity backwards would silently do the opposite of what
	// Settings says.
	SetHTTP2Enabled(false)
	if !fs.GetConfig(context.Background()).DisableHTTP2 {
		t.Error("SetHTTP2Enabled(false) left HTTP/2 enabled")
	}
	SetHTTP2Enabled(true)
	if fs.GetConfig(context.Background()).DisableHTTP2 {
		t.Error("SetHTTP2Enabled(true) left HTTP/2 disabled")
	}
}

// A steady transfer must report a steady, non-zero speed for as long as it
// keeps moving bytes. The previous ramp-based smoother re-anchored its ramp
// on every tick, so the reported speed froze at whatever value it last held
// and never tracked the transfer again.
func TestSpeedAverager_TracksOngoingTransfer(t *testing.T) {
	var s speedAverager
	start := time.Now()
	const perTick = 1 << 20 // 1 MiB every 250ms == 4 MiB/s

	var speed float64
	for i := 0; i <= 40; i++ {
		speed = s.observe(start.Add(time.Duration(i)*250*time.Millisecond), int64(i)*perTick)
	}
	if want := float64(4 << 20); speed < want*0.99 || speed > want*1.01 {
		t.Fatalf("speed = %v, want ~%v", speed, want)
	}
}

// Once bytes stop moving the speed must reach exactly zero within the
// window, since the UI shows "---" only for a zero speed.
func TestSpeedAverager_StallReachesZero(t *testing.T) {
	var s speedAverager
	start := time.Now()
	for i := 0; i <= 20; i++ {
		s.observe(start.Add(time.Duration(i)*250*time.Millisecond), int64(i)<<20)
	}
	stallStart := start.Add(20 * 250 * time.Millisecond)

	var speed float64
	for i := 1; i <= 20; i++ {
		speed = s.observe(stallStart.Add(time.Duration(i)*250*time.Millisecond), 20<<20)
		if elapsed := time.Duration(i) * 250 * time.Millisecond; elapsed < speedWindow && speed <= 0 {
			t.Fatalf("speed dropped to %v after only %s of stall", speed, elapsed)
		}
	}
	if speed != 0 {
		t.Fatalf("speed = %v after a long stall, want exactly 0", speed)
	}
}
