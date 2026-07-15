package ui

import (
	"context"
	"path"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// destFolderBrowser is a live, read-mostly folder browser over the union of
// a set of destination Locations that are assumed to share one directory
// layout (recorder-sync's chosen destinations/uploads). It replaces
// free-typed experiment-name + subpath entry: the researcher navigates
// folders that already exist on any of the locations instead of having to
// remember naming conventions from memory, and the current browse depth
// *is* the chosen path - there's no separate "confirm this folder" step.
// A "+ Add Folder" row, always last in the same scrollable list as the
// real subfolders, lets them type a folder that doesn't exist yet and
// descend into it; nothing is created on disk here - rclone copy creates
// any missing destination directories itself once the sync actually runs.
type destFolderBrowser struct {
	// OnPathChanged fires whenever RelPath() changes - browsing up/down,
	// or a keystroke in the add-folder row. There's no separate
	// "confirm" step: once the add-folder row is in edit mode, whatever's
	// typed in it is already the chosen destination.
	OnPathChanged func(relPath string)

	win fyne.Window

	// allowCreate controls whether the trailing "+ Add Folder" row is
	// offered - off for a plain "browse existing folders" use (e.g. Manage
	// Locations' Browse button), on for recorder-sync's destination picker
	// where typing a not-yet-existing folder name is the point.
	allowCreate bool

	locs    []syncengine.Location
	relPath string
	scanGen int

	backBtn    *widget.Button
	breadcrumb *widget.Label
	statusLbl  *widget.Label
	loading    *loadingBar
	list       *widget.List
	names      []string

	// addingFolder is whether the trailing list row is in text-entry
	// mode. addFolderText mirrors that entry's content - kept on the
	// browser rather than read back off the (pooled, possibly
	// recreated-on-scroll) list item widget. needsFocus is consumed the
	// next time that row is rendered, so opening the row grabs focus
	// exactly once rather than on every list refresh.
	addingFolder  bool
	addFolderText string
	needsFocus    bool

	root fyne.CanvasObject
}

func newDestFolderBrowser(win fyne.Window, allowCreate bool) *destFolderBrowser {
	b := &destFolderBrowser{win: win, allowCreate: allowCreate}

	b.breadcrumb = widget.NewLabel("")
	b.statusLbl = widget.NewLabel("")
	b.statusLbl.Wrapping = fyne.TextWrapWord
	b.loading = newLoadingBar()

	b.list = widget.NewList(
		func() int {
			if b.allowCreate {
				return len(b.names) + 1 // +1 for the trailing "+ Add Folder" row
			}
			return len(b.names)
		},
		func() fyne.CanvasObject {
			entry := widget.NewEntry()
			entry.Hide()
			return container.NewStack(widget.NewButton("", nil), entry)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) { b.updateRow(id, obj) },
	)

	b.backBtn = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() { b.ascend() })

	b.root = container.NewBorder(
		container.NewHBox(b.backBtn, b.breadcrumb),
		container.NewVBox(b.loading.CanvasObject(), b.statusLbl),
		nil, nil,
		b.list,
	)

	b.updateBreadcrumbText()
	b.updateBackBtn()
	return b
}

func (b *destFolderBrowser) CanvasObject() fyne.CanvasObject { return b.root }

// RelPath is the currently chosen destination: the browsed-to folder, plus
// whatever's typed into the add-folder row if it's in edit mode (see
// OnPathChanged - there's no separate commit step for that text).
func (b *destFolderBrowser) RelPath() string {
	if b.addingFolder {
		if name := strings.TrimSpace(b.addFolderText); name != "" {
			return joinRel(b.relPath, name)
		}
	}
	return b.relPath
}

// SetLocations replaces the set of locations being browsed (e.g. the
// destination/upload selection changed) and resets back to the root -
// a path chosen against the old selection may not make sense against the
// new one.
func (b *destFolderBrowser) SetLocations(locs []syncengine.Location) {
	b.locs = locs
	b.relPath = ""
	b.closeAddFolder()
	b.reload()
	b.notifyPathChanged()
}

func (b *destFolderBrowser) descend(name string) {
	b.relPath = joinRel(b.relPath, name)
	b.closeAddFolder()
	b.reload()
	b.notifyPathChanged()
}

func (b *destFolderBrowser) ascend() {
	b.relPath = path.Dir(b.relPath)
	if b.relPath == "." {
		b.relPath = ""
	}
	b.closeAddFolder()
	b.reload()
	b.notifyPathChanged()
}

func (b *destFolderBrowser) showAddFolder() {
	if !b.allowCreate || len(b.locs) == 0 {
		return
	}
	b.addingFolder = true
	b.addFolderText = ""
	b.needsFocus = true
	b.statusLbl.SetText("Folder will be created on first sync.")
	b.list.Refresh()
}

func (b *destFolderBrowser) closeAddFolder() {
	b.addingFolder = false
	b.addFolderText = ""
	b.statusLbl.SetText("")
}

// commitNewFolder folds the typed name into relPath and re-opens browsing
// under it (still without touching disk - rclone copy creates it once a
// sync actually runs). Not required to make the typed name "count": Enter
// just lets the user keep drilling deeper under a not-yet-existing folder.
func (b *destFolderBrowser) commitNewFolder() {
	name := strings.TrimSpace(b.addFolderText)
	if name == "" {
		b.closeAddFolder()
		b.list.Refresh()
		b.notifyPathChanged()
		return
	}
	b.descend(name)
}

func (b *destFolderBrowser) updateRow(id widget.ListItemID, obj fyne.CanvasObject) {
	stack := obj.(*fyne.Container)
	btn := stack.Objects[0].(*widget.Button)
	entry := stack.Objects[1].(*widget.Entry)

	if id < len(b.names) {
		name := b.names[id]
		entry.Hide()
		btn.Show()
		btn.SetText("\U0001F4C1 " + name)
		btn.OnTapped = func() { b.descend(name) }
		return
	}

	// Trailing "+ Add Folder" row.
	if b.addingFolder {
		btn.Hide()
		entry.Show()
		entry.SetText(b.addFolderText)
		entry.OnChanged = func(s string) {
			b.addFolderText = s
			b.notifyPathChanged()
		}
		entry.OnSubmitted = func(string) { b.commitNewFolder() }
		if b.needsFocus {
			b.needsFocus = false
			if b.win != nil {
				b.win.Canvas().Focus(entry)
			}
		}
		return
	}
	entry.Hide()
	btn.Show()
	btn.SetText("+ Add Folder")
	btn.OnTapped = func() { b.showAddFolder() }
	if len(b.locs) == 0 {
		btn.Disable()
	} else {
		btn.Enable()
	}
}

func (b *destFolderBrowser) notifyPathChanged() {
	b.updateBreadcrumbText()
	if b.OnPathChanged != nil {
		b.OnPathChanged(b.RelPath())
	}
}

// updateBreadcrumbText shows the live effective path (RelPath(), which
// includes an in-progress add-folder keystroke), so the breadcrumb tracks
// what "Syncing to:" reports rather than lagging a keystroke behind it.
func (b *destFolderBrowser) updateBreadcrumbText() {
	if rel := b.RelPath(); rel != "" {
		b.breadcrumb.SetText("/" + rel)
	} else {
		b.breadcrumb.SetText("/")
	}
}

// updateBackBtn enables/disables navigating up based on the committed
// relPath - not RelPath()'s live add-folder preview, which isn't a real
// place to browse into.
func (b *destFolderBrowser) updateBackBtn() {
	b.backBtn.Disable()
	if b.relPath != "" {
		b.backBtn.Enable()
	}
}

func (b *destFolderBrowser) reload() {
	b.updateBreadcrumbText()
	b.updateBackBtn()
	b.scanGen++
	gen := b.scanGen
	locs := b.locs
	relPath := b.relPath

	if len(locs) == 0 {
		b.names = nil
		b.list.Refresh()
		b.statusLbl.SetText("")
		b.loading.Hide()
		return
	}

	b.names = nil
	b.list.Refresh()
	b.statusLbl.SetText("")
	b.loading.Show()
	go func() {
		ctx := context.Background()
		syncengine.UnionChildDirNamesStream(ctx, locs, relPath, func(names []string) {
			fyne.Do(func() {
				if gen != b.scanGen {
					return
				}
				b.names = names
				b.list.Refresh()
			})
		})
		fyne.Do(func() {
			if gen != b.scanGen {
				return
			}
			b.loading.Hide()
		})
	}()
}
