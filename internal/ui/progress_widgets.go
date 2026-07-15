package ui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// This file holds reusable render primitives shared by the scan/sync screen
// (progress_screen.go) and, for sectionHeader, screen_recorders.go's local
// sync panel.

type progressItemLayout struct {
	percent float64
}

func (l *progressItemLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) >= 3 {
		return objects[2].MinSize()
	}
	return fyne.NewSize(100, 32)
}

func (l *progressItemLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) < 3 {
		return
	}
	bg := objects[0]
	fill := objects[1]
	content := objects[2]

	bg.Resize(size)
	bg.Move(fyne.NewPos(0, 0))

	fillWidth := float32(float64(size.Width) * l.percent)
	if fillWidth < 0 {
		fillWidth = 0
	}
	if fillWidth > size.Width {
		fillWidth = size.Width
	}
	fill.Resize(fyne.NewSize(fillWidth, size.Height))
	fill.Move(fyne.NewPos(0, 0))

	content.Resize(size)
	content.Move(fyne.NewPos(0, 0))

	// Optional fade overlay (objects[3]) covers the whole row.
	if len(objects) >= 4 {
		objects[3].Resize(size)
		objects[3].Move(fyne.NewPos(0, 0))
	}

	// Selection outline (objects[4]) sits on top of everything so the fill
	// and fade never occlude it.
	if len(objects) >= 5 {
		objects[4].Resize(size)
		objects[4].Move(fyne.NewPos(0, 0))
	}
}

func createBackingBarItem() fyne.CanvasObject {
	bg := canvas.NewRectangle(color.White)
	fill := canvas.NewRectangle(color.Transparent)

	nameLabel := widget.NewLabel("")
	nameLabel.Truncation = fyne.TextTruncateEllipsis

	summaryLabel := widget.NewLabel("")
	summaryLabel.Alignment = fyne.TextAlignTrailing

	// errBtn is hidden by default; updateBackingBarItem shows it when there
	// is an error and wires OnTapped to open the error detail modal.
	errBtn := widget.NewButtonWithIcon("", theme.ErrorIcon(), nil)
	errBtn.Importance = widget.DangerImportance
	errBtn.Hide()

	trailing := container.NewHBox(errBtn, summaryLabel)
	content := container.NewBorder(nil, nil, nil, trailing, nameLabel)
	paddedContent := container.NewPadded(content)

	// fade sits on top of everything; setItemFade tints it to fade a row out.
	fade := canvas.NewRectangle(color.Transparent)

	// selectionBorder is drawn last so the selection outline is never hidden
	// behind the progress fill or fade overlay.
	selectionBorder := canvas.NewRectangle(color.Transparent)
	selectionBorder.StrokeWidth = 0

	itemLayout := &progressItemLayout{percent: 0.0}
	item := container.New(itemLayout, bg, fill, paddedContent, fade, selectionBorder)
	return item
}

func updateBackingBarItem(obj fyne.CanvasObject, labelText, summaryText string, progress float64, itemErr error, hasError bool, isFolder bool, isSelected bool, win fyne.Window) {
	containerObj := obj.(*fyne.Container)
	bg := containerObj.Objects[0].(*canvas.Rectangle)
	fill := containerObj.Objects[1].(*canvas.Rectangle)

	paddedContent := containerObj.Objects[2].(*fyne.Container)
	borderContainer := paddedContent.Objects[0].(*fyne.Container)
	nameLabel := borderContainer.Objects[0].(*widget.Label)
	trailing := borderContainer.Objects[1].(*fyne.Container)
	errBtn := trailing.Objects[0].(*widget.Button)
	summaryLabel := trailing.Objects[1].(*widget.Label)

	nameLabel.SetText(labelText)
	summaryLabel.SetText(summaryText)
	if itemErr != nil {
		errText := itemErr.Error()
		errBtn.OnTapped = func() { showErrorModal(win, errText) }
		errBtn.Show()
	} else {
		errBtn.OnTapped = nil
		errBtn.Hide()
	}

	layout := containerObj.Layout.(*progressItemLayout)
	layout.percent = progress

	var bgColor, fillColor color.Color

	if itemErr != nil || (hasError && !isFolder) {
		bgColor = color.NRGBA{R: 254, G: 226, B: 226, A: 255}
		fillColor = color.NRGBA{R: 252, G: 165, B: 165, A: 255}
	} else if hasError {
		bgColor = color.NRGBA{R: 254, G: 243, B: 199, A: 255}
		fillColor = color.NRGBA{R: 253, G: 186, B: 116, A: 255}
	} else {
		bgColor = color.White
		if progress >= 1.0 {
			fillColor = color.NRGBA{R: 147, G: 197, B: 253, A: 255}
			bgColor = fillColor
		} else {
			fillColor = color.NRGBA{R: 219, G: 234, B: 254, A: 255}
		}
	}

	bg.FillColor = bgColor
	fill.FillColor = fillColor

	if len(containerObj.Objects) >= 5 {
		selectionBorder := containerObj.Objects[4].(*canvas.Rectangle)
		if isSelected {
			selectionBorder.StrokeColor = color.NRGBA{R: 59, G: 130, B: 246, A: 255}
			selectionBorder.StrokeWidth = 2
		} else {
			selectionBorder.StrokeWidth = 0
		}
		selectionBorder.Refresh()
	}

	setItemFade(obj, 0)

	bg.Refresh()
	fill.Refresh()
	containerObj.Refresh()
}

// updateDividerItem restyles a backing-bar item as a section divider (a muted
// grey banner with centered text and no progress fill or summary).
func updateDividerItem(obj fyne.CanvasObject, text string) {
	containerObj := obj.(*fyne.Container)
	bg := containerObj.Objects[0].(*canvas.Rectangle)
	fill := containerObj.Objects[1].(*canvas.Rectangle)

	paddedContent := containerObj.Objects[2].(*fyne.Container)
	borderContainer := paddedContent.Objects[0].(*fyne.Container)
	nameLabel := borderContainer.Objects[0].(*widget.Label)
	trailing := borderContainer.Objects[1].(*fyne.Container)
	errBtn := trailing.Objects[0].(*widget.Button)
	summaryLabel := trailing.Objects[1].(*widget.Label)

	nameLabel.SetText(text)
	summaryLabel.SetText("")
	errBtn.OnTapped = nil
	errBtn.Hide()

	layout := containerObj.Layout.(*progressItemLayout)
	layout.percent = 0

	bg.FillColor = color.NRGBA{R: 229, G: 231, B: 235, A: 255}
	bg.StrokeWidth = 0
	fill.FillColor = color.Transparent

	setItemFade(obj, 0)

	bg.Refresh()
	fill.Refresh()
	containerObj.Refresh()
}

// setItemFade tints a backing-bar item's top overlay (objects[3]) toward the
// pane background. fade is 0 (fully visible) to 1 (invisible).
func setItemFade(obj fyne.CanvasObject, fade float64) {
	containerObj := obj.(*fyne.Container)
	if len(containerObj.Objects) < 4 {
		return
	}
	overlay := containerObj.Objects[3].(*canvas.Rectangle)
	if fade <= 0 {
		overlay.FillColor = color.Transparent
	} else {
		if fade > 1 {
			fade = 1
		}
		overlay.FillColor = color.NRGBA{R: 240, G: 242, B: 245, A: uint8(fade * 255)}
	}
	overlay.Refresh()
}

// tintItemBg overrides a backing-bar item's background colour. Used to give
// already-synced file rows a light grey wash so they read as distinct from
// to-sync rows. Call after updateBackingBarItem (which sets the base colour).
func tintItemBg(obj fyne.CanvasObject, c color.Color) {
	containerObj := obj.(*fyne.Container)
	bg := containerObj.Objects[0].(*canvas.Rectangle)
	bg.FillColor = c
	bg.Refresh()
}

func createColumn(title string, content fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 240, G: 242, B: 245, A: 255})
	bg.StrokeWidth = 1
	bg.StrokeColor = color.NRGBA{R: 218, G: 220, B: 224, A: 255}
	bg.CornerRadius = 8

	titleLabel := widget.NewLabelWithStyle(title, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	headerBg := canvas.NewRectangle(color.NRGBA{R: 209, G: 213, B: 219, A: 255})
	header := container.NewStack(headerBg, container.NewPadded(titleLabel))

	colContent := container.NewBorder(
		container.NewVBox(header, widget.NewSeparator()),
		nil, nil, nil,
		content,
	)

	return container.NewStack(bg, colContent)
}

// sectionHeader is a heavy-weight banner that labels a Files sub-panel
// ("Current Sync" / "Already synced"). It sits above its panel's list, so it
// stays fixed while the list scrolls. Also used by screen_recorders.go's
// local-sync panel.
func sectionHeader(title string) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 240, G: 242, B: 245, A: 255})
	label := widget.NewLabelWithStyle(title, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	return container.NewStack(bg, container.NewPadded(label))
}

func metricPanel(label string, value *widget.Label, bg color.Color) fyne.CanvasObject {
	rect := canvas.NewRectangle(bg)
	rect.CornerRadius = 8
	caption := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return container.NewStack(rect, container.NewPadded(container.NewVBox(caption, value)))
}

// thinBarLayout renders its single child (an infinite progress bar) as a
// slim horizontal strip of barHeight, vertically centered within whatever
// height the parent actually allocates - letting a loadingBar report a much
// shorter MinSize than the bar's own (much taller) default one.
type thinBarLayout struct{ barHeight float32 }

func (l *thinBarLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, l.barHeight)
}

func (l *thinBarLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) == 0 {
		return
	}
	y := (size.Height - l.barHeight) / 2
	objects[0].Resize(fyne.NewSize(size.Width, l.barHeight))
	objects[0].Move(fyne.NewPos(0, y))
}

// loadingBar is a slim infinite progress bar (rather than a full-height
// widget.ProgressBarInfinite) used by any folder/experiment browser while a
// scan is in flight.
type loadingBar struct {
	root fyne.CanvasObject
	bar  *widget.ProgressBarInfinite
}

func newLoadingBar() *loadingBar {
	bar := widget.NewProgressBarInfinite()
	thinHeight := bar.MinSize().Height / 5

	barWrap := container.New(&thinBarLayout{barHeight: thinHeight}, bar)
	barWrap.Hide()

	return &loadingBar{root: barWrap, bar: bar}
}

func (lb *loadingBar) CanvasObject() fyne.CanvasObject { return lb.root }

func (lb *loadingBar) Show() {
	lb.root.Show()
	lb.bar.Start()
}

func (lb *loadingBar) Hide() {
	lb.bar.Stop()
	lb.root.Hide()
}

func humanSpeed(bytesPerSec float64) string {
	const unit = 1024
	if bytesPerSec < unit {
		return fmt.Sprintf("%.1f B", bytesPerSec)
	}
	div, exp := float64(unit), 0
	for m := bytesPerSec / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", bytesPerSec/div, "KMGTPE"[exp])
}

// showErrorModal opens a scrollable dialog containing the full error text and
// a Copy button. It is triggered by the error-icon button on a list row.
func showErrorModal(win fyne.Window, errText string) {
	errLabel := widget.NewLabel(errText)
	errLabel.Wrapping = fyne.TextWrapWord

	scroll := container.NewScroll(errLabel)
	scroll.SetMinSize(fyne.NewSize(420, 220))

	copyBtn := widget.NewButton("Copy", func() {
		win.Clipboard().SetContent(errText)
	})

	var d dialog.Dialog
	closeBtn := widget.NewButton("Close", func() { d.Hide() })

	content := container.NewBorder(
		nil,
		container.NewCenter(container.NewHBox(copyBtn, closeBtn)),
		nil, nil,
		scroll,
	)

	d = dialog.NewCustomWithoutButtons("Error Details", content, win)
	d.Show()
}
