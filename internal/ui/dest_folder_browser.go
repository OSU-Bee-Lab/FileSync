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

	// showFiles controls whether files (not just subfolders) appear in the
	// listing. Off by default (see the package comment on showPullFiles);
	// on for Manage Locations' Browse dialog (context to confirm the
	// candidate path is right) and for Pull Files (selectFiles below).
	showFiles bool

	// selectFiles controls whether a shown file is tappable to select it
	// as the browse target, alongside the usual folder scope - e.g. Pull
	// Files letting a researcher grab exactly one recording instead of a
	// whole folder. Only meaningful when showFiles is also set. Off for
	// Manage Locations' Browse dialog, where files are shown for context
	// only - "Set as Location" only ever adopts a directory.
	selectFiles bool

	// selectedFile is the tapped file's name within the current relPath,
	// or "" if none is selected. Cleared by any navigation (reload) since
	// a selection only makes sense within the folder it was made in.
	selectedFile string

	locs    []syncengine.Location
	relPath string
	scanGen int

	backBtn    *widget.Button
	breadcrumb *widget.Label
	statusLbl  *widget.Label
	loading    *loadingBar
	list       *widget.List
	entries    []syncengine.Entry

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
				return len(b.entries) + 1 // +1 for the trailing "+ Add Folder" row
			}
			return len(b.entries)
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
// OnPathChanged - there's no separate commit step for that text), or a
// tapped file's path if one is selected (see selectFile).
func (b *destFolderBrowser) RelPath() string {
	if b.addingFolder {
		if name := strings.TrimSpace(b.addFolderText); name != "" {
			return joinRel(b.relPath, name)
		}
	}
	if b.selectedFile != "" {
		return joinRel(b.relPath, b.selectedFile)
	}
	return b.relPath
}

// IsFileSelected reports whether RelPath() names a selected file (from
// selectFiles mode) rather than the browsed-to folder itself.
func (b *destFolderBrowser) IsFileSelected() bool {
	return b.selectedFile != ""
}

// selectFile is a file row's tap handler when selectFiles is on: it toggles
// name as the selection within the current folder (tapping the same file
// again deselects it, falling back to the folder scope). Files have no
// children, so unlike descend this never re-browses or reloads.
func (b *destFolderBrowser) selectFile(name string) {
	b.closeAddFolder()
	if b.selectedFile == name {
		b.selectedFile = ""
	} else {
		b.selectedFile = name
	}
	b.list.Refresh()
	b.notifyPathChanged()
}

// SetLocations replaces the set of locations being browsed (e.g. the
// destination/upload selection changed) without disturbing the currently
// chosen path: if none of the (possibly new) locations have that folder
// yet, that's fine - it's created on sync - and if they do, the reload
// below re-scans and shows it.
func (b *destFolderBrowser) SetLocations(locs []syncengine.Location) {
	b.locs = locs
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
	b.selectedFile = ""
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

	if id < len(b.entries) {
		e := b.entries[id]
		entry.Hide()
		btn.Show()
		if e.IsDir {
			btn.Importance = widget.MediumImportance
			btn.SetText("\U0001F4C1 " + e.Name)
			btn.OnTapped = func() { b.descend(e.Name) }
		} else if b.selectFiles {
			name := e.Name
			if b.selectedFile == name {
				btn.Importance = widget.HighImportance
				btn.SetText("✅ " + name)
			} else {
				btn.Importance = widget.MediumImportance
				btn.SetText("\U0001F4C4 " + name)
			}
			btn.OnTapped = func() { b.selectFile(name) }
		} else {
			btn.Importance = widget.MediumImportance
			btn.SetText("\U0001F4C4 " + e.Name)
			btn.OnTapped = nil
		}
		btn.Enable()
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
	b.selectedFile = ""
	b.updateBreadcrumbText()
	b.updateBackBtn()
	b.scanGen++
	gen := b.scanGen
	locs := b.locs
	relPath := b.relPath

	if len(locs) == 0 {
		b.entries = nil
		b.list.Refresh()
		b.statusLbl.SetText("")
		b.loading.Hide()
		return
	}

	b.entries = nil
	b.list.Refresh()
	b.statusLbl.SetText("")
	b.loading.Show()
	go func() {
		ctx := context.Background()
		onUpdate := func(entries []syncengine.Entry) {
			fyne.Do(func() {
				if gen != b.scanGen {
					return
				}
				b.entries = entries
				b.list.Refresh()
			})
		}
		if b.showFiles {
			syncengine.UnionChildEntriesStream(ctx, locs, relPath, onUpdate)
		} else {
			syncengine.UnionChildDirNamesStream(ctx, locs, relPath, func(names []string) {
				entries := make([]syncengine.Entry, len(names))
				for i, n := range names {
					entries[i] = syncengine.Entry{Name: n, IsDir: true}
				}
				onUpdate(entries)
			})
		}
		fyne.Do(func() {
			if gen != b.scanGen {
				return
			}
			b.loading.Hide()
		})
	}()
}
