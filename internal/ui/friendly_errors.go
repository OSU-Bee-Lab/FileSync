package ui

import (
	"fmt"
	"regexp"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// friendlyErr is a raw rclone error translated for display: what went wrong,
// what happens next, and the original text kept aside for troubleshooting.
//
// rclone's errors are written for someone reading a terminal - they carry the
// HTTP verb, the full request URL and the underlying Go transport error, e.g.
//
//	Put "https://osu.sharepoint.com/_api/v2.0/drive/items/01ABC.../uploadSession?guid=...&tempauth=eyJ0eX...":
//	http2: client connection force closed via ClientConn.Close
//
// None of which tells a researcher whether their data is safe. Headline and
// Advice answer that; Detail keeps the original for when it's actually needed.
type friendlyErr struct {
	// Headline is a single plain sentence naming what failed.
	Headline string
	// Advice says what the app already did or what the user should do. May
	// be empty when the headline is self-contained.
	Advice string
	// Detail is the original error text, redacted (see redactURLs). Always
	// non-empty, so "Details" never opens onto nothing.
	Detail string
}

// String renders the user-facing part of a friendlyErr (never Detail).
func (f friendlyErr) String() string {
	if f.Advice == "" {
		return f.Headline
	}
	return f.Headline + " " + f.Advice
}

// urlRe matches an http(s) URL embedded in an error message.
var urlRe = regexp.MustCompile(`https?://[^\s"'<>]+`)

// redactURLs strips request URLs down to their host.
//
// This is both a readability and a safety measure. The URLs rclone quotes in
// upload errors are single-use API endpoints - a SharePoint upload-session
// URL is ~400 characters of opaque GUIDs, and its query string carries a
// `tempauth=` bearer token. That token is a live credential for the duration
// of the session, and an error banner is a place users screenshot and paste
// into emails and issue trackers. The host is the only part with any
// diagnostic value ("it was talking to SharePoint"), so that's all we keep.
func redactURLs(msg string) string {
	return urlRe.ReplaceAllStringFunc(msg, func(u string) string {
		rest := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		host, _, _ := strings.Cut(rest, "/")
		if host == "" {
			return "<url>"
		}
		return host + "/…"
	})
}

// errorPattern maps a set of phrases appearing in rclone's error text to the
// explanation shown instead. Matching on text is unavoidable here: rclone
// flattens backend-specific error types into strings well before they reach
// us, which is the same reason rclone's own fserrors matches on phrases.
type errorPattern struct {
	phrases  []string
	headline string
	advice   string
}

// errorPatterns is checked in order, first match wins, so put the specific
// cases above the general ones. Anything unmatched falls through to
// classifyError's generic case, which still shows the redacted original.
var errorPatterns = []errorPattern{
	{
		phrases:  []string{"quotaLimitReached", "storageLimitReached", "insufficient space", "no space left on device", "not enough space"},
		headline: "The destination is out of space.",
		advice:   "Free up space (or pick another destination) and sync again — nothing was lost here.",
	},
	{
		phrases:  []string{"Too Many Requests", "rateLimit", "activityLimitReached", "userThrottled"},
		headline: "The remote is rate-limiting FileSync.",
		advice:   "This usually clears on its own; syncing again later, or lowering Transfers and Checkers in Settings, avoids it.",
	},
	{
		phrases:  []string{"accessDenied", "403 Forbidden", "permission denied", "unauthorized", "Access denied"},
		headline: "The remote refused access to that location.",
		advice:   "Check that this account still has write access to the destination folder.",
	},
	{
		phrases:  []string{"nameAlreadyExists", "invalidRequest", "pathTooLong", "invalid characters", "name is too long", "file name too long"},
		headline: "The remote rejected a file's name or path.",
		advice:   "SharePoint and OneDrive limit path length and disallow some characters; the file needs renaming or moving somewhere shallower.",
	},
	{
		phrases:  []string{"directory not found", "object not found", "itemNotFound", "no such file or directory", "404 Not Found"},
		headline: "A file or folder went missing mid-sync.",
		advice:   "It was probably moved or deleted while the sync was running. Scanning again will pick up the current state.",
	},
	{
		phrases:  []string{"http2:", "connection reset by peer", "broken pipe", "unexpected EOF", "i/o timeout", "TLS handshake timeout", "Client.Timeout exceeded", "context deadline exceeded", "server misbehaving", "no such host", "network is unreachable", "connection refused"},
		headline: "The connection to the remote dropped.",
		advice:   "Nothing was damaged — files transfer completely or not at all. Syncing again picks up exactly what's still missing.",
	},
}

// classifyError turns an rclone error into a friendlyErr. It never returns
// the raw error as the headline: an unrecognised error still gets a plain
// framing plus the reassurance that the data is intact, with the original
// text (URLs redacted) available under Details.
func classifyError(err error) friendlyErr {
	if err == nil {
		return friendlyErr{}
	}
	detail := redactURLs(err.Error())

	if isAuthError(err) {
		return friendlyErr{
			Headline: "The sign-in for this remote has expired.",
			Advice:   "Reconnect the location, then sync again.",
			Detail:   detail,
		}
	}
	for _, p := range errorPatterns {
		for _, phrase := range p.phrases {
			if strings.Contains(detail, phrase) {
				return friendlyErr{Headline: p.headline, Advice: p.advice, Detail: detail}
			}
		}
	}
	return friendlyErr{
		Headline: "The sync couldn't finish.",
		Advice:   "No files were deleted or half-written — syncing again will retry whatever didn't make it.",
		Detail:   detail,
	}
}

// retryNoticeText captions the amber "still working on it" label shown while
// a copy is between attempts. With an unlimited retry budget (the default)
// there's no "of N" to count towards, so it just reports how many attempts
// have gone by - enough to tell a brief hiccup from an outage that's been
// running all night.
func retryNoticeText(attempt, max int) string {
	if max <= syncengine.RetriesUnlimited {
		return fmt.Sprintf("⚠ Connection trouble — still retrying (attempt %d)…", attempt)
	}
	return fmt.Sprintf("⚠ Connection trouble — retrying (%d/%d)…", attempt, max)
}

// showErrorDetails opens the raw (redacted) error text in a scrollable,
// selectable box, for pasting into a bug report. Read-only rather than a
// label so the text can be selected and copied.
func showErrorDetails(title, detail string, win fyne.Window) {
	box := widget.NewMultiLineEntry()
	box.SetText(detail)
	box.Wrapping = fyne.TextWrapWord

	scroll := container.NewVScroll(box)
	scroll.SetMinSize(fyne.NewSize(460, 180))

	d := dialog.NewCustom(title, "Close", scroll, win)
	d.Show()
}
