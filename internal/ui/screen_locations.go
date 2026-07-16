package ui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/appconfig"
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// byPriorityThenName sorts by explicit Priority ascending; locations that
// share a Priority (in particular a legacy config where Priority was never
// set — all zero — before this ranking feature existed) fall back to
// alphabetical order by Name rather than whatever order they happened to be
// in, so a freshly upgraded config starts from a predictable ranking
// instead of an arbitrary one.
func byPriorityThenName(locs []syncengine.Location) func(i, j int) bool {
	return func(i, j int) bool {
		if locs[i].Priority != locs[j].Priority {
			return locs[i].Priority < locs[j].Priority
		}
		return strings.ToLower(locs[i].Name) < strings.ToLower(locs[j].Name)
	}
}

// normalizePriorities re-sorts cfg.Locations so all Local locations are
// grouped first (by byPriorityThenName), followed by all Remote locations
// (same rule, independently numbered), with each group's Priority
// renumbered 1..n. Slice order is what BuildNWayTransferPlan actually walks
// for its tie-break between two same-kind sources (see PreferLocalSource,
// which only ever prefers local over remote, never reorders two same-kind
// candidates against each other) — keeping slice order in sync with
// Priority here is what makes the ranking take effect, for both groups
// independently. Idempotent, and safe to call before every render: it's
// also what upgrades a legacy config where Priority was never set (all
// zero) into a stable, predictable (alphabetical) default order.
func normalizePriorities(cfg *appconfig.Config) {
	cfg.Locations = normalizedLocationOrder(cfg.Locations)
}

// normalizedLocationOrder is the pure reordering logic behind
// normalizePriorities, split out so it's testable without an appconfig.Config
// or any UI/disk side effects.
func normalizedLocationOrder(locs []syncengine.Location) []syncengine.Location {
	locals := make([]syncengine.Location, 0, len(locs))
	remotes := make([]syncengine.Location, 0, len(locs))
	for _, loc := range locs {
		if loc.Kind == syncengine.LocationLocal {
			locals = append(locals, loc)
		} else {
			remotes = append(remotes, loc)
		}
	}
	sort.SliceStable(locals, byPriorityThenName(locals))
	sort.SliceStable(remotes, byPriorityThenName(remotes))
	for i := range locals {
		locals[i].Priority = i + 1
	}
	for i := range remotes {
		remotes[i].Priority = i + 1
	}
	return append(locals, remotes...)
}

// moveToPosition moves the location currently at cfg index cfgIdx so it
// lands at finalPos (0-indexed) among locations of the same Kind, then
// renumbers that group's Priority 1..n to match and persists. Used directly
// by the priority dropdown (finalPos is exactly the position the user
// picked).
func moveToPosition(s *state, cfgIdx int, finalPos int) {
	s.cfg.Locations = locationsMovedToPosition(s.cfg.Locations, cfgIdx, finalPos)
	s.saveConfig()
	showLocations(s)
}

// locationsMovedToPosition is the pure reordering logic behind
// moveToPosition, split out so it's testable without a *state (which pulls
// in Fyne widgets and disk writes via saveConfig/showLocations).
func locationsMovedToPosition(locs []syncengine.Location, cfgIdx int, finalPos int) []syncengine.Location {
	loc := locs[cfgIdx]
	group := make([]syncengine.Location, 0, len(locs))
	others := make([]syncengine.Location, 0, len(locs))
	for i, l := range locs {
		if i == cfgIdx {
			continue
		}
		if l.Kind == loc.Kind {
			group = append(group, l)
		} else {
			others = append(others, l)
		}
	}
	if finalPos < 0 {
		finalPos = 0
	}
	if finalPos > len(group) {
		finalPos = len(group)
	}
	group = append(group[:finalPos], append([]syncengine.Location{loc}, group[finalPos:]...)...)
	for i := range group {
		group[i].Priority = i + 1
	}
	if loc.Kind == syncengine.LocationLocal {
		return append(group, others...)
	}
	return append(others, group...)
}

// moveBySeam is the drag-and-drop entry point: seam is a drop-gap index
// among the n current rows in the dragged row's group (0 = above the first
// row, n = below the last), measured against the pre-move row order (the
// row being dragged is still shown in place during the drag). It converts
// that into the finalPos moveToPosition expects.
func moveBySeam(s *state, cfgIdx int, from int, seam int) {
	moveToPosition(s, cfgIdx, seamToFinalPos(from, seam))
}

// seamToFinalPos converts a drop-gap seam index (measured against the
// pre-move row order, including the row being dragged) into the 0-indexed
// final position locationsMovedToPosition expects (measured after that row
// is removed from the group). Split out from moveBySeam so it's directly
// testable.
func seamToFinalPos(from, seam int) int {
	finalPos := seam
	if seam > from {
		finalPos--
	}
	return finalPos
}

func locationButtonRow(s *state, id int, loc syncengine.Location) *fyne.Container {
	removeBtn := widget.NewButton("Remove", func() { removeLocation(s, id, loc) })
	removeBtn.Importance = widget.DangerImportance
	browseBtn := widget.NewButton("Browse", func() { browseLocation(s, id, loc) })
	editBtn := widget.NewButton("Edit", func() { showEditLocation(s, id) })
	btns := []fyne.CanvasObject{browseBtn, editBtn}
	if loc.Kind == syncengine.LocationRemote {
		exportBtn := widget.NewButton("Export", func() { exportLocation(s, loc) })
		btns = append(btns, exportBtn)
	}
	btns = append(btns, removeBtn)
	return container.NewHBox(btns...)
}

func locationRowContent(s *state, id int, loc syncengine.Location, priorityLabels []string, priorityIdx int) fyne.CanvasObject {
	nameLabel := widget.NewLabelWithStyle(loc.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	pathLabel := widget.NewLabel(fmt.Sprintf("%s: %s", loc.Kind, describeLocation(loc)))
	trailing := locationButtonRow(s, id, loc)
	// NewSelect + SetSelectedIndex, in that order, would fire OnChanged
	// immediately (the previous "" selected value always differs from the
	// initial index) and re-enter showLocations while it's still building
	// this same render — an infinite loop. Set the initial index with no
	// callback attached, then attach OnChanged only after.
	prioritySelect := widget.NewSelect(priorityLabels, nil)
	prioritySelect.SetSelectedIndex(priorityIdx)
	prioritySelect.OnChanged = func(sel string) {
		n, err := strconv.Atoi(sel)
		if err != nil {
			return
		}
		moveToPosition(s, id, n-1)
	}
	trailing = container.NewHBox(widget.NewLabel("Priority"), prioritySelect, trailing)
	nameRow := container.NewBorder(nil, nil, nil, trailing, nameLabel)
	return container.NewVBox(nameRow, pathLabel)
}

// addRankedSection appends a titled group of rows to body for every
// location in idx (all the same Kind), each with a priority dropdown and a
// drag handle so the user can rank them — 1 beats 2 beats 3, etc. — for
// N-way sync source selection. Locals are always preferred over remotes
// regardless of either group's ranking (see PreferLocalSource); ranking
// only breaks ties within a group — among locals when several hold a file
// no remote needs, or among remotes when no local has the file at all.
func addRankedSection(s *state, body *fyne.Container, title string, idx []int) {
	if len(idx) == 0 {
		return
	}
	body.Add(widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))

	priorityLabels := make([]string, len(idx))
	for i := range priorityLabels {
		priorityLabels[i] = strconv.Itoa(i + 1)
	}

	// One seam rectangle above each row plus one below the last row;
	// seam[i] is the drop-gap "above row i" (seam[len(idx)] is below the
	// last row). Highlighted blue while a row is dragged over it,
	// transparent otherwise.
	seams := make([]*canvas.Rectangle, len(idx)+1)
	for i := range seams {
		r := canvas.NewRectangle(theme.Color(theme.ColorNameBackground))
		r.SetMinSize(fyne.NewSize(0, 3))
		seams[i] = r
	}
	clearSeams := func() {
		for _, r := range seams {
			r.FillColor = theme.Color(theme.ColorNameBackground)
			r.Refresh()
		}
	}

	body.Add(seams[0])
	for pos, cfgIdx := range idx {
		loc := s.cfg.Locations[cfgIdx]
		row := locationRowContent(s, cfgIdx, loc, priorityLabels, pos)

		from := pos
		cid := cfgIdx
		startY := float32(0)
		clampTarget := func(t int) int {
			if t < 0 {
				return 0
			}
			if t > len(idx) {
				return len(idx)
			}
			return t
		}

		var rowWithHandle *fyne.Container
		handle := newDragHandle(
			func(dy float32) {
				startY += dy
				rowHeight := rowWithHandle.MinSize().Height
				if rowHeight <= 0 {
					rowHeight = 1
				}
				target := clampTarget(int(float32(from) + startY/rowHeight + 0.5))
				clearSeams()
				seams[target].FillColor = theme.Color(theme.ColorNamePrimary)
				seams[target].Refresh()
			},
			func() {
				rowHeight := rowWithHandle.MinSize().Height
				if rowHeight <= 0 {
					rowHeight = 1
				}
				target := clampTarget(int(float32(from) + startY/rowHeight + 0.5))
				startY = 0
				clearSeams()
				if target != from {
					moveBySeam(s, cid, from, target)
				}
			},
		)
		rowWithHandle = container.NewBorder(nil, nil, handle, nil, row)

		body.Add(rowWithHandle)
		body.Add(seams[pos+1])
	}
}

// showLocations renders Manage Locations, split into a Local section and a
// Remote section, each independently ranked (priority dropdown + drag
// handle). Locals always win as an N-way sync source over remotes
// regardless of rank; ranking only breaks ties within a group.
func showLocations(s *state) {
	normalizePriorities(&s.cfg)

	var localIdx, remoteIdx []int
	for i, loc := range s.cfg.Locations {
		if loc.Kind == syncengine.LocationLocal {
			localIdx = append(localIdx, i)
		} else {
			remoteIdx = append(remoteIdx, i)
		}
	}

	body := container.NewVBox()
	addRankedSection(s, body, "Local", localIdx)
	addRankedSection(s, body, "Remote", remoteIdx)

	scroll := container.NewVScroll(body)

	addBtn := widget.NewButton("+ Add Location", func() { showAddLocation(s) })
	importBtn := widget.NewButton("Import Location...", func() { importLocation(s) })
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(widget.NewLabelWithStyle("Locations", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), container.NewHBox(addBtn, importBtn), widget.NewSeparator()),
		backBtn,
		nil, nil,
		scroll,
	)
	s.setContent(container.NewPadded(content))
}

// unlistLocation drops loc at index id from FileSync's config, leaving any
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
		dialog.ShowConfirm("Remove location", "Remove \""+loc.Name+"\" from FileSync?", func(ok bool) {
			if ok {
				unlistLocation(s, id)
			}
		}, s.win)
		return
	}

	msg := widget.NewLabel("Remove \"" + loc.Name + "\" from FileSync?\n\n" +
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
	cancelBtn := widget.NewButton("Cancel", func() { d.Hide() })

	d = dialog.NewCustomWithoutButtons("Remove location",
		container.NewVBox(msg, container.NewCenter(container.NewHBox(unlistBtn, deleteBtn, cancelBtn))), s.win)
	d.Resize(fyne.NewSize(420, 0))
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
	bt, _, _ := syncengine.RemoteConfig(remoteName)
	runRemoteOAuthUpdate(s, bt, "Reconnecting...", "Reconnecting "+displayName+"...", remoteName, map[string]string{}, func(err error) {
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
func runRemoteOAuthUpdate(s *state, bt syncengine.BackendType, dialogTitle, progressText, remoteName string, fields map[string]string, onDone func(err error)) {
	runRemoteOAuth(s, bt, dialogTitle, progressText, func(ctx context.Context, onAuthURL func(url string)) error {
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
func runRemoteOAuth(s *state, bt syncengine.BackendType, dialogTitle, progressText string, run func(ctx context.Context, onAuthURL func(url string)) error, onDone func(err error)) {
	progressLabel := widget.NewLabel(progressText)
	progressLabel.Wrapping = fyne.TextWrapWord

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

	var progressDialog dialog.Dialog
	cancelBtn := widget.NewButton("Cancel", func() { progressDialog.Hide() })

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
	// browser buttons start hidden and appear once onAuthURL fires; Cancel
	// stays put in the same row throughout.
	buttonRow := container.NewHBox(openBtn, copyBtn, cancelBtn)
	openBtn.Hide()
	copyBtn.Hide()

	progressDialog = dialog.NewCustomWithoutButtons(dialogTitle, container.NewVBox(progressLabel, buttonRow), s.win)
	progressDialog.SetOnClosed(cancel)
	progressDialog.Show()
	// NewCustomWithoutButtons sizes to content min width, which is narrow for
	// a short label - widen it so the sign-in instructions and buttons aren't
	// cramped.
	progressDialog.Resize(fyne.NewSize(460, 200))

	go func() {
		defer cancel()
		err := run(ctx, func(rawURL string) {
			fyne.Do(func() {
				authURL = rawURL
				openBtn.Show()
				copyBtn.Show()
				progressDialog.Resize(fyne.NewSize(460, 200))
				text := "Click Open in Browser to sign in."
				if bt == syncengine.BackendOneDrive {
					// OneDrive/SharePoint sign-in reuses whichever Microsoft
					// account the browser is already logged into and won't
					// re-prompt for a different one - Google Drive/Dropbox
					// don't have this quirk, they prompt for account choice
					// every time.
					text += "\n\nTo sign in as a different account than the one your browser " +
						"is already logged into, use Copy Link and open it in a " +
						"private/incognito window."
				}
				progressLabel.SetText(text)
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

// browseLocation shows an in-app folder browser over loc (reusing
// destFolderBrowser, including its "+ Add Folder" row - typing a
// not-yet-existing name just lets browsing continue into it; nothing is
// created on disk until an actual sync copies files there). "Set as
// Location" lets the user navigate to the folder they actually mean and
// adopt it as loc's RootPath, e.g. after finding experiments have moved
// into a subfolder.
//
// The browser is anchored above loc.RootPath - the remote's own root for a
// remote location, or "/" for a local one - and starts already drilled down
// to loc.RootPath, so Back can climb above the current location (e.g. up to
// a sibling folder, the remote root, or the local drive root) rather than
// treating RootPath as a floor.
func browseLocation(s *state, id int, loc syncengine.Location) {
	browser := newDestFolderBrowser(s.win, true)
	rootLoc := loc
	if loc.Kind == syncengine.LocationRemote {
		rootLoc.RootPath = ""
	} else {
		rootLoc.RootPath = "/"
	}
	browser.locs = []syncengine.Location{rootLoc}
	browser.relPath = strings.Trim(loc.RootPath, "/")
	browser.reload()

	var d dialog.Dialog
	setBtn := widget.NewButton("Set as Location", func() {
		rel := browser.RelPath()
		if loc.Kind == syncengine.LocationLocal {
			rel = "/" + rel
		}
		s.cfg.Locations[id].RootPath = rel
		s.saveConfig()
		d.Hide()
		showLocations(s)
	})
	setBtn.Importance = widget.HighImportance
	closeBtn := widget.NewButton("Close", func() { d.Hide() })

	body := container.NewBorder(nil, container.NewCenter(container.NewHBox(setBtn, closeBtn)), nil, nil, browser.CanvasObject())
	d = dialog.NewCustomWithoutButtons("Browse "+loc.Name, body, s.win)
	d.Resize(fyne.NewSize(480, 500))
	d.Show()
}

func describeLocation(loc syncengine.Location) string {
	if loc.Kind == syncengine.LocationLocal {
		return loc.RootPath
	}
	return loc.RemoteName + ":" + loc.RootPath
}
