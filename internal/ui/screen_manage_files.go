package ui

import (
	"context"
	"fmt"
	"path"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// showManageFiles is the dev-gated Manage Files flow: direct rename,
// move/merge, and delete operations against experiment data on one or more
// selected Locations. See CLAUDE.md's Manage Files exception — this is a
// deliberate, user-driven carve-out from the app's otherwise copy-only
// sync engine, gated the same way as Pull Files (devMode) and requiring an
// explicit preview/collision-resolution/confirm sequence before anything
// is applied.
func showManageFiles(s *state) {
	names := locationNames(s.cfg.Locations)
	locGroup := newToggleGroup(names, nil)
	mirrorWarning := widget.NewLabel("")
	mirrorWarning.Wrapping = fyne.TextWrapWord

	updateMirrorWarning := func() {
		selected := locGroup.Selected()
		if len(selected) > 0 && len(selected) < len(s.cfg.Locations) {
			mirrorWarning.SetText("Warning: only " + fmt.Sprint(len(selected)) + " of " + fmt.Sprint(len(s.cfg.Locations)) +
				" Locations selected. Per SCHEMA.md, mirrored Locations should always change together — " +
				"applying this to only some of them will make the mirrors diverge.")
		} else {
			mirrorWarning.SetText("")
		}
	}
	// --- source path picker (browses the first selected Location) ---
	relPath := ""
	breadcrumb := widget.NewLabel("experiments/")
	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("experiments/<relative path>")

	var entries []syncengine.Entry
	list := widget.NewList(
		func() int { return len(entries) },
		func() fyne.CanvasObject { return widget.NewButton("", nil) },
		nil,
	)
	upBtn := widget.NewButton("Up", nil)

	refLoc := func() *syncengine.Location {
		sel := locGroup.Selected()
		if len(sel) == 0 {
			return nil
		}
		return findLocation(s.cfg.Locations, sel[0])
	}

	var loadChildren func()
	loadChildren = func() {
		loc := refLoc()
		if loc == nil {
			entries = nil
			list.Refresh()
			breadcrumb.SetText("Select a Location above first.")
			return
		}
		breadcrumb.SetText("Loading experiments/" + relPath + "...")
		src := *loc
		p := relPath
		go func() {
			result, err := syncengine.ListChildren(context.Background(), src, p)
			fyne.Do(func() {
				if relPath != p {
					return
				}
				if err != nil {
					breadcrumb.SetText("Error loading experiments/" + relPath)
					showLocationError(s, err, src)
					return
				}
				entries = result
				breadcrumb.SetText("experiments/" + relPath)
				upBtn.Disable()
				if relPath != "" {
					upBtn.Enable()
				}
				list.Refresh()
			})
		}()
	}

	list.UpdateItem = func(id widget.ListItemID, obj fyne.CanvasObject) {
		e := entries[id]
		btn := obj.(*widget.Button)
		label := e.Name
		if e.IsDir {
			label = "\U0001F4C1 " + label
		} else {
			label = fmt.Sprintf("%s  (%s)", label, humanBytes(e.Size))
		}
		btn.SetText(label)
		btn.OnTapped = func() {
			if e.IsDir {
				relPath = joinRel(relPath, e.Name)
				pathEntry.SetText(relPath)
				loadChildren()
			}
		}
	}
	upBtn.OnTapped = func() {
		relPath = path.Dir(relPath)
		if relPath == "." {
			relPath = ""
		}
		pathEntry.SetText(relPath)
		loadChildren()
	}
	pathEntry.OnSubmitted = func(text string) {
		relPath = strings.Trim(strings.TrimSpace(text), "/")
		loadChildren()
	}
	locGroup.OnChanged = func([]string) {
		updateMirrorWarning()
		relPath = ""
		pathEntry.SetText("")
		loadChildren()
	}

	// --- operation choice ---
	newNameEntry := widget.NewEntry()
	newNameEntry.SetPlaceHolder("new name (same folder)")
	destEntry := widget.NewEntry()
	destEntry.SetPlaceHolder("destination folder, e.g. Luke - Wooster 1")
	deleteConfirmEntry := widget.NewEntry()
	deleteConfirmEntry.SetPlaceHolder("type the exact relative path to confirm")

	renameRow := widget.NewForm(widget.NewFormItem("New name", newNameEntry))
	moveRow := widget.NewForm(widget.NewFormItem("Move/merge into", destEntry))
	deleteRow := widget.NewForm(widget.NewFormItem("Confirm path", deleteConfirmEntry))
	renameRow.Hide()
	moveRow.Hide()
	deleteRow.Hide()

	opGroup := widget.NewRadioGroup([]string{"Rename", "Move / Merge", "Delete"}, nil)
	opGroup.OnChanged = func(v string) {
		renameRow.Hide()
		moveRow.Hide()
		deleteRow.Hide()
		switch v {
		case "Rename":
			renameRow.Show()
		case "Move / Merge":
			moveRow.Show()
		case "Delete":
			deleteRow.Show()
		}
	}
	opGroup.SetSelected("Rename")

	previewBox := container.NewVBox()
	previewScroll := container.NewVScroll(previewBox)
	previewScroll.SetMinSize(fyne.NewSize(0, 220))

	type locPlan struct {
		loc       syncengine.Location
		move      *syncengine.MovePlan
		del       *syncengine.DeletePlan
		dstOf     string // for move: destination relPath used
		collision map[string]*widget.Select
	}
	var plans []*locPlan

	buildPreview := func() {
		previewBox.Objects = nil
		plans = nil

		selectedNames := locGroup.Selected()
		if len(selectedNames) == 0 {
			previewBox.Add(widget.NewLabel("Select at least one Location."))
			previewScroll.Refresh()
			return
		}
		if relPath == "" {
			previewBox.Add(widget.NewLabel("Pick a source file/folder first."))
			previewScroll.Refresh()
			return
		}

		locs := locationsFromNamesAny(s.cfg.Locations, selectedNames)
		op := opGroup.Selected

		if op == "Delete" && deleteConfirmEntry.Text != relPath {
			previewBox.Add(widget.NewLabel("Type the exact relative path (\"" + relPath + "\") into the confirm field to preview the delete."))
			previewScroll.Refresh()
			return
		}

		ctx := context.Background()
		for _, loc := range locs {
			lp := &locPlan{loc: loc, collision: map[string]*widget.Select{}}
			section := container.NewVBox(widget.NewLabelWithStyle(loc.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))

			switch op {
			case "Rename", "Move / Merge":
				dst := path.Join(path.Dir(relPath), newNameEntry.Text)
				if op == "Move / Merge" {
					dst = strings.Trim(strings.TrimSpace(destEntry.Text), "/")
				}
				if dst == "" || dst == "." {
					section.Add(widget.NewLabel("(missing new name / destination)"))
					previewBox.Add(section)
					continue
				}
				plan, err := syncengine.PlanMove(ctx, loc, relPath, dst)
				if err != nil {
					section.Add(widget.NewLabel("Error: " + err.Error()))
					previewBox.Add(section)
					continue
				}
				lp.move = &plan
				lp.dstOf = dst
				section.Add(widget.NewLabel(fmt.Sprintf("%d file(s) will move to %q", len(plan.Moves), dst)))
				if len(plan.Collisions) > 0 {
					section.Add(widget.NewLabelWithStyle("Collisions - choose how to resolve each:", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
					for _, c := range plan.Collisions {
						c := c
						sel := widget.NewSelect([]string{"Skip", "Overwrite", "Keep both"}, nil)
						sel.SetSelected("Skip")
						lp.collision[c] = sel
						section.Add(container.NewBorder(nil, nil, widget.NewLabel(c), nil, sel))
					}
				}
				for _, m := range plan.Moves {
					section.Add(widget.NewLabel("  " + m.SrcRelPath + " -> " + m.DstRelPath))
				}
			case "Delete":
				plan, err := syncengine.PlanDelete(ctx, loc, relPath)
				if err != nil {
					section.Add(widget.NewLabel("Error: " + err.Error()))
					previewBox.Add(section)
					continue
				}
				lp.del = &plan
				section.Add(widget.NewLabel(fmt.Sprintf("%d file(s) will be permanently deleted:", len(plan.Entries))))
				for _, e := range plan.Entries {
					section.Add(widget.NewLabel("  " + e.RelPath))
				}
			}
			previewBox.Add(section)
			previewBox.Add(widget.NewSeparator())
			plans = append(plans, lp)
		}
		previewScroll.Refresh()
	}

	previewBtn := widget.NewButton("Preview", buildPreview)

	applyBtn := widget.NewButton("Apply", func() {
		if len(plans) == 0 {
			dialog.ShowInformation("Nothing to apply", "Preview the operation first.", s.win)
			return
		}
		op := opGroup.Selected

		run := func() {
			var failed []string
			ctx := context.Background()
			for _, lp := range plans {
				var err error
				switch op {
				case "Rename", "Move / Merge":
					if lp.move == nil {
						continue
					}
					resolutions := make(map[string]syncengine.CollisionResolution, len(lp.collision))
					for path, sel := range lp.collision {
						switch sel.Selected {
						case "Overwrite":
							resolutions[path] = syncengine.CollisionOverwrite
						case "Keep both":
							resolutions[path] = syncengine.CollisionKeepBoth
						default:
							resolutions[path] = syncengine.CollisionSkip
						}
					}
					err = syncengine.ApplyMove(ctx, lp.loc, *lp.move, resolutions)
				case "Delete":
					err = syncengine.ApplyDelete(ctx, lp.loc, relPath)
				}
				if err != nil {
					failed = append(failed, lp.loc.Name+": "+err.Error())
				}
			}
			if len(failed) > 0 {
				dialog.ShowError(fmt.Errorf("some Locations failed - mirrors may now differ:\n%s", strings.Join(failed, "\n")), s.win)
				return
			}
			dialog.ShowInformation("Done", "The operation completed on every selected Location.", s.win)
			showManageFiles(s)
		}

		if op == "Delete" {
			showIrreversibleDeleteConfirm(s, run)
			return
		}
		run()
	})
	applyBtn.Importance = widget.DangerImportance

	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Manage Files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel("Locations to apply this operation to:"),
			locGroup.CanvasObject(),
			mirrorWarning,
			widget.NewSeparator(),
			container.NewHBox(upBtn, breadcrumb),
		),
		container.NewVBox(
			widget.NewForm(widget.NewFormItem("Path", pathEntry)),
			widget.NewSeparator(),
			opGroup,
			renameRow,
			moveRow,
			deleteRow,
			container.NewHBox(previewBtn, applyBtn, backBtn),
			widget.NewSeparator(),
			previewScroll,
		),
		nil, nil,
		list,
	)
	s.setContent(container.NewPadded(content))
	upBtn.Disable()
}
