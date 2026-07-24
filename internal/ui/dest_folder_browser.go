package ui

import (
	"context"
	"fmt"
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
//
// It backs all five in-app browse call sites: recorder-sync's destination
// picker, Sync Experiments (one-way), Manage Locations' Browse dialog, Pull
// Files, and Manage Files' From/To picker. Most browse the union of one or
// more Locations via the streaming Union* listers; Manage Files instead sets
// a custom lister (see the lister field) so it lists exactly one Location via
// syncengine.ListChildren with its own not-found tolerance and error routing.
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
	// candidate path is right), Pull Files (selectFiles below), and Manage
	// Files.
	showFiles bool

	// selectFiles controls whether a shown file is tappable to select it
	// as the browse target, alongside the usual folder scope - e.g. Pull
	// Files letting a researcher grab exactly one recording instead of a
	// whole folder. Only meaningful when showFiles is also set. Off for
	// Manage Locations' Browse dialog, where files are shown for context
	// only - "Set as Location" only ever adopts a directory.
	selectFiles bool

	// showFileSize appends a "(size)" suffix to each file row - on for
	// Manage Files (where the size helps confirm the right file/folder to
	// move or delete), off elsewhere.
	showFileSize bool

	// selectedFile is the tapped file's name within the current relPath,
	// or "" if none is selected. Cleared by any navigation (reload) since
	// a selection only makes sense within the folder it was made in.
	selectedFile string

	// lister, when non-nil, replaces the default multi-Location union
	// listing (reload's Union* calls) with a fully custom one. Manage Files
	// sets it to list a single reference Location via
	// syncengine.ListChildren, tolerating a not-found/is-a-file path (naming
	// a new destination, or selecting a bare file) as an empty listing while
	// still routing a hard error (e.g. an expired remote token) to reconnect
	// - behavior the union path deliberately doesn't have (it silently skips
	// a Location it can't list). It is handed the reload's scan generation
	// (guard UI updates with listingDone/listingFailed, which drop stale
	// generations) and the relPath to list, and owns its own goroutine.
	lister func(gen int, relPath string)

	// breadcrumbPrefix precedes RelPath() in the breadcrumb. Empty (the
	// default) keeps the legacy "/"+rel form; Manage Files sets
	// "experiments/" so the breadcrumb reads from that schema root.
	breadcrumbPrefix string

	// breadcrumbNote is an optional trailing note on the breadcrumb (e.g.
	// Manage Files' " (new folder)" when a "To" path doesn't exist yet), set
	// by a custom lister and cleared on every navigation.
	breadcrumbNote string

	// addFolderStatus is the status-line message shown while the add-folder
	// row is open. Defaults to the recorder/experiment-sync wording; Manage
	// Files overrides it (its folders are created on Apply, not a sync).
	addFolderStatus string

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
	b := &destFolderBrowser{win: win, allowCreate: allowCreate, addFolderStatus: "Folder will be created on first sync."}

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

// NavigateTo re-anchors the browser at relPath and re-lists it, as if the
// user had browsed there - used when an external field (Manage Files' typed
// From/To entry, or a target switch) is the source of truth for where to
// browse. Trailing/leading slashes are tolerated.
func (b *destFolderBrowser) NavigateTo(relPath string) {
	b.relPath = strings.Trim(strings.TrimSpace(relPath), "/")
	b.closeAddFolder()
	b.reload()
	b.notifyPathChanged()
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

// SetBrowseLocations updates the Location set without reloading or firing
// OnPathChanged, for a caller (Manage Files) that lists through a custom
// lister and drives reload itself. It only refreshes the add-folder row's
// enabled state, which depends on there being at least one Location.
func (b *destFolderBrowser) SetBrowseLocations(locs []syncengine.Location) {
	b.locs = locs
	b.list.Refresh()
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
	b.statusLbl.SetText(b.addFolderStatus)
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

// fileLabel is the display text for a file row - the name, plus a "(size)"
// suffix when showFileSize is on.
func (b *destFolderBrowser) fileLabel(e syncengine.Entry) string {
	if b.showFileSize {
		return fmt.Sprintf("%s  (%s)", e.Name, humanBytes(e.Size))
	}
	return e.Name
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
			label := b.fileLabel(e)
			if b.selectedFile == name {
				btn.Importance = widget.HighImportance
				btn.SetText("✅ " + label)
			} else {
				btn.Importance = widget.MediumImportance
				btn.SetText("\U0001F4C4 " + label)
			}
			btn.OnTapped = func() { b.selectFile(name) }
		} else {
			btn.Importance = widget.MediumImportance
			btn.SetText("\U0001F4C4 " + b.fileLabel(e))
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
	rel := b.RelPath()
	if b.breadcrumbPrefix != "" {
		b.breadcrumb.SetText(b.breadcrumbPrefix + rel + b.breadcrumbNote)
		return
	}
	if rel != "" {
		b.breadcrumb.SetText("/" + rel)
	} else {
		b.breadcrumb.SetText("/")
	}
}

// setBreadcrumbNote sets the trailing breadcrumb note (see breadcrumbNote)
// and repaints the breadcrumb. Called by a custom lister once it knows a
// "To" path names a folder that doesn't exist yet.
func (b *destFolderBrowser) setBreadcrumbNote(note string) {
	b.breadcrumbNote = note
	b.updateBreadcrumbText()
}

// setBreadcrumbOverride replaces the breadcrumb text outright (e.g. Manage
// Files' "Select a Location above first." when nothing is selected to
// browse), bypassing the usual prefix+path formatting.
func (b *destFolderBrowser) setBreadcrumbOverride(text string) {
	b.breadcrumb.SetText(text)
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

// listingDone publishes a custom lister's result for scan generation gen,
// dropping it (and returning false) if a newer reload has superseded it. A
// nil entries slice is a valid empty listing. Hides the loading bar. Must be
// called on the UI goroutine.
func (b *destFolderBrowser) listingDone(gen int, entries []syncengine.Entry) bool {
	if gen != b.scanGen {
		return false
	}
	b.entries = entries
	b.list.Refresh()
	b.loading.Hide()
	return true
}

// listingFailed marks a custom lister's scan as finished-with-error for
// generation gen (hides the loading bar), returning false if it's stale so
// the caller can skip surfacing an error for a superseded listing.
func (b *destFolderBrowser) listingFailed(gen int) bool {
	if gen != b.scanGen {
		return false
	}
	b.loading.Hide()
	return true
}

func (b *destFolderBrowser) reload() {
	b.selectedFile = ""
	b.breadcrumbNote = ""
	b.updateBreadcrumbText()
	b.updateBackBtn()
	b.scanGen++
	gen := b.scanGen

	// A custom lister (Manage Files) owns listing entirely from here.
	if b.lister != nil {
		b.entries = nil
		b.list.Refresh()
		b.statusLbl.SetText("")
		b.loading.Show()
		b.lister(gen, b.relPath)
		return
	}

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
