package ui

import (
	"context"
	"errors"
	"fmt"
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
			return container.NewBorder(nil, nil, nil,
				container.NewHBox(widget.NewButton("Edit...", nil), widget.NewButton("Export...", nil), removeBtn),
				container.NewVBox(nameLabel, pathLabel))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			loc := s.cfg.Locations[id]
			border := obj.(*fyne.Container)
			labelBox := border.Objects[0].(*fyne.Container)
			nameLabel := labelBox.Objects[0].(*widget.Label)
			pathLabel := labelBox.Objects[1].(*widget.Label)
			nameLabel.SetText(loc.Name)
			pathLabel.SetText(fmt.Sprintf("%s: %s", loc.Kind, describeLocation(loc)))

			btnBox := border.Objects[1].(*fyne.Container)
			editBtn := btnBox.Objects[0].(*widget.Button)
			editBtn.OnTapped = func() { showEditLocation(s, id) }

			exportBtn := btnBox.Objects[1].(*widget.Button)
			exportBtn.Hidden = loc.Kind != syncengine.LocationRemote
			exportBtn.OnTapped = func() { exportLocation(s, loc) }

			removeBtn := btnBox.Objects[2].(*widget.Button)
			removeBtn.OnTapped = func() {
				dialog.ShowConfirm("Remove location", "Remove \""+loc.Name+"\" from ExpSync?", func(ok bool) {
					if !ok {
						return
					}
					s.cfg.Locations = append(append([]syncengine.Location{}, s.cfg.Locations[:id]...), s.cfg.Locations[id+1:]...)
					s.saveConfig()
					showLocations(s)
				}, s.win)
			}
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

// isAuthError reports whether err looks like rclone's expired-OAuth-token
// error (the "couldn't fetch token: invalid_grant: maybe token expired?"
// message that today only surfaces as a raw error dialog, telling the user
// to run `rclone config reconnect` themselves). rclone always wraps the
// underlying invalid_grant with this exact prefix (see
// lib/oauthutil/oauthutil.go), so matching on it is reliable across
// backends.
func isAuthError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "couldn't fetch token")
}

// showLocationError displays err from an operation involving locs. If it
// looks like an expired OAuth token on one of the remote locations, it
// offers a "Reconnect" button that re-runs the sign-in flow in place of a
// plain error dialog; otherwise it falls back to dialog.ShowError.
func showLocationError(s *state, err error, locs ...syncengine.Location) {
	if isAuthError(err) {
		for _, loc := range locs {
			if loc.Kind != syncengine.LocationRemote {
				continue
			}
			loc := loc
			dialog.NewCustomConfirm("Sign-in expired", "Reconnect", "Cancel",
				widget.NewLabel("\""+loc.Name+"\" needs you to sign in again:\n\n"+err.Error()),
				func(ok bool) {
					if ok {
						reconnectRemote(s, loc)
					}
				}, s.win).Show()
			return
		}
	}
	dialog.ShowError(err, s.win)
}

// reconnectRemote re-runs the OAuth sign-in for loc's remote without
// touching any of its other settings - the fix for rclone errors like
// "invalid_grant: maybe token expired?" that ask the user to run
// `rclone config reconnect`. Passing no fields to UpdateRemote leaves
// existing config alone; driveConfigSteps still drives the backend's OAuth
// confirms to their default ("yes, refresh token"), so this forces the
// browser sign-in flow the same way editing-and-saving an OAuth remote does.
func reconnectRemote(s *state, loc syncengine.Location) {
	runRemoteOAuthUpdate(s, "Reconnecting...", "Reconnecting "+loc.Name+"...", loc.RemoteName, map[string]string{}, func(err error) {
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			dialog.ShowError(fmt.Errorf("couldn't reconnect: %w", err), s.win)
			return
		}
		dialog.ShowInformation("Reconnected", "\""+loc.Name+"\" is signed in again.", s.win)
	})
}

// runRemoteOAuthUpdate drives syncengine.UpdateRemote for remoteName,
// showing a progress dialog shared by the Reconnect action above and the
// "Save & Re-authorize" edit flow (screen_remote_edit.go) - both need the
// same "browser is open, here's the link, and Cancel really cancels" UI, so
// it lives in one place rather than two copies drifting apart. The dialog's
// "Cancel" button is wired to actually cancel ctx (not just hide the
// dialog), and a "Copy Link" button appears once the sign-in URL is known so
// the user isn't stuck if the browser didn't open. onDone runs on the Fyne
// UI goroutine; a user-initiated cancel reports context.Canceled so callers
// can tell it apart from a real failure.
func runRemoteOAuthUpdate(s *state, dialogTitle, progressText, remoteName string, fields map[string]string, onDone func(err error)) {
	progressLabel := widget.NewLabel(progressText)
	progressLabel.Wrapping = fyne.TextWrapWord

	var authURL string
	copyBtn := widget.NewButton("Copy Link", func() {
		if authURL != "" {
			s.win.Clipboard().SetContent(authURL)
		}
	})
	copyBtn.Hide()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

	progressDialog := dialog.NewCustom(dialogTitle, "Cancel", container.NewVBox(progressLabel, copyBtn), s.win)
	progressDialog.SetOnClosed(cancel)
	progressDialog.Show()

	go func() {
		defer cancel()
		err := syncengine.UpdateRemote(ctx, remoteName, fields, func(url string) {
			fyne.Do(func() {
				authURL = url
				copyBtn.Show()
				progressLabel.SetText("Opening your browser to sign in...\nIf it doesn't open, visit:\n" + url)
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

func describeLocation(loc syncengine.Location) string {
	if loc.Kind == syncengine.LocationLocal {
		return loc.RootPath
	}
	return loc.RemoteName + ":" + loc.RootPath
}
