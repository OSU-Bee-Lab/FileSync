package ui

import (
	"fmt"

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
			return container.NewBorder(nil, nil, nil,
				container.NewHBox(widget.NewButton("Export...", nil), widget.NewButton("Remove", nil)),
				widget.NewLabel(""))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			loc := s.cfg.Locations[id]
			border := obj.(*fyne.Container)
			label := border.Objects[0].(*widget.Label)
			label.SetText(fmt.Sprintf("%s  (%s: %s)", loc.Name, loc.Kind, describeLocation(loc)))

			btnBox := border.Objects[1].(*fyne.Container)
			exportBtn := btnBox.Objects[0].(*widget.Button)
			exportBtn.Hidden = loc.Kind != syncengine.LocationRemote
			exportBtn.OnTapped = func() { exportLocation(s, loc) }

			removeBtn := btnBox.Objects[1].(*widget.Button)
			removeBtn.OnTapped = func() {
				dialog.ShowConfirm("Remove location", "Remove \""+loc.Name+"\" from ExpSync? This only forgets it locally - no files are touched.", func(ok bool) {
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

func describeLocation(loc syncengine.Location) string {
	if loc.Kind == syncengine.LocationLocal {
		return loc.RootPath
	}
	return loc.RemoteName + ":" + loc.RootPath
}
