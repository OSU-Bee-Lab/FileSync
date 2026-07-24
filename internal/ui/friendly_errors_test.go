package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// The real thing, as it appeared in the sync screen's error banner.
const http2Raw = `Put "https://osu.sharepoint.com/_api/v2.0/drive/items/01ABCDEF/uploadSession?guid=%277f2%27&path=~tmp&overwrite=True&tempauth=eyJ0eXAiOiJKV1QiLCJhbGciOiJub25lIn0": http2: client connection force closed via ClientConn.Close`

func TestRedactURLs_DropsCredentialBearingQuery(t *testing.T) {
	got := redactURLs(http2Raw)
	if strings.Contains(got, "tempauth") {
		t.Errorf("redactURLs left a tempauth token in the text users screenshot:\n%s", got)
	}
	if !strings.Contains(got, "osu.sharepoint.com") {
		t.Errorf("redactURLs dropped the host, which is the one useful part:\n%s", got)
	}
	if !strings.Contains(got, "http2: client connection force closed") {
		t.Errorf("redactURLs mangled the error itself:\n%s", got)
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantHead string
	}{
		{"dropped connection", errors.New(http2Raw), "The connection to the remote dropped."},
		{"expired token", errors.New(`empty token found - please run "rclone config reconnect teams:"`), "The sign-in for this remote has expired."},
		{"out of space", errors.New("quotaLimitReached: not enough storage"), "The destination is out of space."},
		{"throttled", errors.New("HTTP error 429 (429 Too Many Requests)"), "The remote is rate-limiting FileSync."},
		{"unrecognised", errors.New("something nobody has seen before"), "The sync couldn't finish."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyError(tt.err)
			if got.Headline != tt.wantHead {
				t.Errorf("headline = %q, want %q", got.Headline, tt.wantHead)
			}
			// Whatever the classification, the original must stay reachable
			// under Details - that's what makes it safe to hide it.
			if got.Detail == "" {
				t.Error("Detail is empty; the raw error was lost")
			}
			// And the raw text must never leak into the banner itself.
			if strings.Contains(got.String(), "http2") || strings.Contains(got.String(), "https://") {
				t.Errorf("raw error text leaked into the banner: %q", got.String())
			}
		})
	}
}

func TestClassifyError_NilIsZero(t *testing.T) {
	if got := classifyError(nil); got != (friendlyErr{}) {
		t.Errorf("classifyError(nil) = %+v, want zero value", got)
	}
}

func TestRetryNoticeText(t *testing.T) {
	unlimited := retryNoticeText(7, syncengine.RetriesUnlimited)
	if strings.Contains(unlimited, "/0") || strings.Contains(unlimited, " 0") {
		t.Errorf("unlimited retries rendered a bogus budget: %q", unlimited)
	}
	if !strings.Contains(unlimited, "7") {
		t.Errorf("unlimited notice should still report the attempt count: %q", unlimited)
	}
	if got := retryNoticeText(2, 5); !strings.Contains(got, "2/5") {
		t.Errorf("finite notice = %q, want it to show 2/5", got)
	}
}
