package ui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

func showLocations(s *state) {
	list := widget.NewList(
		func() int { return len(s.cfg.Locations) },
		func() fyne.CanvasObject {
			nameLabel := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			pathLabel := widget.NewLabel("")
			removeBtn := widget.NewButton("Remove", nil)
			removeBtn.Importance = widget.DangerImportance
			btnBox := container.NewHBox(widget.NewButton("Show Experiments", nil), widget.NewButton("Edit", nil), widget.NewButton("Export", nil), removeBtn)
			nameRow := container.NewBorder(nil, nil, nil, btnBox, nameLabel)
			return container.NewVBox(nameRow, pathLabel)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			loc := s.cfg.Locations[id]
			vbox := obj.(*fyne.Container)
			nameRow := vbox.Objects[0].(*fyne.Container)
			pathLabel := vbox.Objects[1].(*widget.Label)
			nameLabel := nameRow.Objects[0].(*widget.Label)
			nameLabel.SetText(loc.Name)
			pathLabel.SetText(fmt.Sprintf("%s: %s", loc.Kind, describeLocation(loc)))

			btnBox := nameRow.Objects[1].(*fyne.Container)
			showExpBtn := btnBox.Objects[0].(*widget.Button)
			showExpBtn.OnTapped = func() { showLocationExperiments(s, loc) }

			editBtn := btnBox.Objects[1].(*widget.Button)
			editBtn.OnTapped = func() { showEditLocation(s, id) }

			exportBtn := btnBox.Objects[2].(*widget.Button)
			exportBtn.Hidden = loc.Kind != syncengine.LocationRemote
			exportBtn.OnTapped = func() { exportLocation(s, loc) }

			removeBtn := btnBox.Objects[3].(*widget.Button)
			removeBtn.OnTapped = func() { removeLocation(s, id, loc) }
		},
	)

	addBtn := widget.NewButton("+ Add Location", func() { showAddLocation(s) })
	importBtn := widget.NewButton("Import Location...", func() { importLocation(s) })
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(widget.NewLabelWithStyle("Locations", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), container.NewHBox(addBtn, importBtn), widget.NewSeparator()),
		backBtn,
		nil, nil,
		list,
	)
	s.setContent(container.NewPadded(content))
}

// unlistLocation drops loc at index id from ExpSync's config, leaving any
// underlying rclone remote untouched. Shared by both removal paths so the
// list-editing logic lives in one place.
func unlistLocation(s *state, id int) {
	s.cfg.Locations = append(append([]syncengine.Location{}, s.cfg.Locations[:id]...), s.cfg.Locations[id+1:]...)
	s.saveConfig()
	showLocations(s)
}

// removeLocation asks how far to remove loc. For a local location there's
// nothing but the list entry to drop, so it's a plain confirm. For a remote
// location it offers two outcomes: "Unlist" (forget it here but keep the
// rclone remote and its saved sign-in) or "Delete config" (also delete the
// rclone remote's credentials via syncengine.DeleteRemote). Deleting the
// remote only removes the stored credentials - it never touches files on the
// remote itself.
func removeLocation(s *state, id int, loc syncengine.Location) {
	if loc.Kind != syncengine.LocationRemote {
		dialog.ShowConfirm("Remove location", "Remove \""+loc.Name+"\" from ExpSync?", func(ok bool) {
			if ok {
				unlistLocation(s, id)
			}
		}, s.win)
		return
	}

	msg := widget.NewLabel("Remove \"" + loc.Name + "\" from ExpSync?\n\n" +
		"• Unlist: forget it here, but keep the rclone remote \"" + loc.RemoteName + "\" and its sign-in.\n" +
		"• Delete config: also delete the rclone remote's saved credentials.\n\n" +
		"Neither option deletes any files on the remote itself.")
	msg.Wrapping = fyne.TextWrapWord

	var d dialog.Dialog
	unlistBtn := widget.NewButton("Unlist", func() {
		d.Hide()
		unlistLocation(s, id)
	})
	deleteBtn := widget.NewButton("Delete config", func() {
		d.Hide()
		syncengine.DeleteRemote(loc.RemoteName)
		unlistLocation(s, id)
	})
	deleteBtn.Importance = widget.DangerImportance

	d = dialog.NewCustom("Remove location", "Cancel",
		container.NewVBox(msg, container.NewHBox(unlistBtn, deleteBtn)), s.win)
	d.Show()
}

// reconnectHintRe extracts the remote name from rclone's
// `please run "rclone config reconnect teams:"` hint, so a bad-token error
// can still be routed to the reconnect window even when the caller didn't
// pass the offending Location.
var reconnectHintRe = regexp.MustCompile(`config reconnect (\S+?):`)

// isAuthError reports whether err looks like an rclone bad/expired-token
// error that a re-authorization would fix. rclone surfaces these in a few
// shapes across backends and code paths:
//   - "couldn't fetch token: invalid_grant: maybe token expired?"
//   - "empty token found - please run \"rclone config reconnect teams:\""
//
// In every case rclone's remedy is the same OAuth reconnect, and it always
// mentions either "fetch token", an empty token, or the `config reconnect`
// hint - so matching on those is reliable across backends.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "couldn't fetch token") ||
		strings.Contains(msg, "empty token found") ||
		strings.Contains(msg, "config reconnect")
}

// showLocationError is the single entrypoint for surfacing errors from
// operations that touch a remote. If err looks like a bad OAuth token
// (isAuthError), it always routes to the reconnect window rather than a
// plain error dialog: it prefers a remote Location from locs, and otherwise
// falls back to the remote name rclone names in its `config reconnect` hint.
// Non-auth errors fall through to dialog.ShowError.
func showLocationError(s *state, err error, locs ...syncengine.Location) {
	if isAuthError(err) {
		for _, loc := range locs {
			if loc.Kind != syncengine.LocationRemote {
				continue
			}
			showReconnectWindow(s, err, loc.RemoteName, loc.Name)
			return
		}
		if m := reconnectHintRe.FindStringSubmatch(err.Error()); m != nil {
			showReconnectWindow(s, err, m[1], m[1])
			return
		}
	}
	dialog.ShowError(err, s.win)
}

// showReconnectWindow is the common bad-token entrypoint: it explains the
// sign-in has expired and offers a "Reconnect" button that runs the OAuth
// flow for remoteName. displayName is the friendly name shown to the user
// (a Location's name when we have it, else the raw remote name).
func showReconnectWindow(s *state, err error, remoteName, displayName string) {
	dialog.NewCustomConfirm("Sign-in expired", "Reconnect", "Cancel",
		widget.NewLabel("\""+displayName+"\" needs you to sign in again:\n\n"+err.Error()),
		func(ok bool) {
			if ok {
				reconnectRemote(s, remoteName, displayName)
			}
		}, s.win).Show()
}

// reconnectRemote re-runs the OAuth sign-in for remoteName without touching
// any of its other settings - the fix for rclone errors like
// "invalid_grant: maybe token expired?" that ask the user to run
// `rclone config reconnect`. Passing no fields to UpdateRemote leaves
// existing config alone; driveConfigSteps still drives the backend's OAuth
// confirms to their default ("yes, refresh token"), so this forces the
// browser sign-in flow the same way editing-and-saving an OAuth remote does.
func reconnectRemote(s *state, remoteName, displayName string) {
	runRemoteOAuthUpdate(s, "Reconnecting...", "Reconnecting "+displayName+"...", remoteName, map[string]string{}, func(err error) {
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			dialog.ShowError(fmt.Errorf("couldn't reconnect: %w", err), s.win)
			return
		}
		dialog.ShowInformation("Reconnected", "\""+displayName+"\" is signed in again.", s.win)
	})
}

// runRemoteOAuthUpdate drives syncengine.UpdateRemote for remoteName,
// showing the shared sign-in dialog (see runRemoteOAuth), reused by the
// Reconnect action above and the "Save & Re-authorize" edit flow
// (screen_remote_edit.go) so the three flows don't drift apart.
func runRemoteOAuthUpdate(s *state, dialogTitle, progressText, remoteName string, fields map[string]string, onDone func(err error)) {
	runRemoteOAuth(s, dialogTitle, progressText, func(ctx context.Context, onAuthURL func(url string)) error {
		return syncengine.UpdateRemote(ctx, remoteName, fields, onAuthURL)
	}, onDone)
}

// runRemoteOAuth is the shared engine behind every browser-sign-in flow -
// adding a remote (CreateRemote), editing/re-authorizing one (UpdateRemote),
// and reconnecting an expired one. run performs the actual syncengine call and
// is handed an onAuthURL callback to surface the sign-in URL. All three flows
// therefore get the same dialog. The browser is never opened automatically:
// once the sign-in URL is known, "Open in Browser" and "Copy Link" buttons
// appear (Copy Link lets the user open it in the browser profile / incognito
// window holding the account they want, rather than being stuck on whichever
// account their default browser is signed into). Cancel really cancels ctx.
// onDone runs on the Fyne UI goroutine; a user-initiated cancel reports
// context.Canceled so callers can tell it apart from a real failure.
func runRemoteOAuth(s *state, dialogTitle, progressText string, run func(ctx context.Context, onAuthURL func(url string)) error, onDone func(err error)) {
	progressLabel := widget.NewLabel(progressText)
	progressLabel.Wrapping = fyne.TextWrapWord

	var authURL string
	openBtn := widget.NewButton("Open in Browser", func() {
		if u, err := url.Parse(authURL); err == nil {
			fyne.CurrentApp().OpenURL(u)
		}
	})
	openBtn.Importance = widget.HighImportance
	copyBtn := widget.NewButton("Copy Link", func() {
		if authURL != "" {
			s.win.Clipboard().SetContent(authURL)
		}
	})
	// The sign-in URL isn't known until run() reaches the OAuth step, so the
	// browser buttons start hidden and appear once onAuthURL fires.
	buttonRow := container.NewHBox(openBtn, copyBtn)
	buttonRow.Hide()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

	progressDialog := dialog.NewCustom(dialogTitle, "Cancel", container.NewVBox(progressLabel, buttonRow), s.win)
	progressDialog.SetOnClosed(cancel)
	progressDialog.Show()
	// NewCustom sizes to content min width, which is narrow for a short label -
	// widen it so the sign-in instructions and buttons aren't cramped.
	progressDialog.Resize(fyne.NewSize(460, 200))

	go func() {
		defer cancel()
		err := run(ctx, func(rawURL string) {
			fyne.Do(func() {
				authURL = rawURL
				buttonRow.Show()
				progressDialog.Resize(fyne.NewSize(460, 200))
				progressLabel.SetText("Click Open in Browser to sign in.\n\n" +
					"To sign in as a different account than the one your browser " +
					"is already logged into, use Copy Link and open it in a " +
					"private/incognito window.")
			})
		})
		fyne.Do(func() {
			progressDialog.Hide()
			if err != nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			onDone(err)
		})
	}()
}

// showLocationExperiments lists the experiment directories found at the
// root of loc, so users can check what's there without starting a Sync or
// Download flow.
func showLocationExperiments(s *state, loc syncengine.Location) {
	progressDialog := dialog.NewCustom("Loading...", "Please wait", widget.NewLabel("Listing experiments in "+loc.Name+"..."), s.win)
	progressDialog.Show()

	go func() {
		exps, err := syncengine.ListExperiments(context.Background(), loc)
		fyne.Do(func() {
			progressDialog.Hide()
			if err != nil {
				showLocationError(s, err, loc)
				return
			}
			names := make([]string, len(exps))
			for i, e := range exps {
				names[i] = e.Name
			}
			body := "No experiments found."
			if len(names) > 0 {
				body = strings.Join(names, "\n")
			}
			list := widget.NewLabel(body)
			list.Wrapping = fyne.TextWrapWord
			scroll := container.NewVScroll(list)
			scroll.SetMinSize(fyne.NewSize(360, 300))
			dialog.ShowCustom(fmt.Sprintf("Experiments in %s (%d)", loc.Name, len(names)), "Close", scroll, s.win)
		})
	}()
}

func describeLocation(loc syncengine.Location) string {
	if loc.Kind == syncengine.LocationLocal {
		return loc.RootPath
	}
	return loc.RemoteName + ":" + loc.RootPath
}
