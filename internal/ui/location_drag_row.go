package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// dragHandle is a small "knurled dots" grip (2x3 dots, like a drag handle
// on a physical slider) that's the only draggable part of a ranked location
// row — dragging the row itself anywhere else does nothing, only this
// handle reorders it. It reports raw vertical drag movement to the caller,
// which tracks cumulative movement and which seam between rows the drag is
// currently over.
type dragHandle struct {
	widget.BaseWidget
	onDragged func(dy float32)
	onDragEnd func()
}

func newDragHandle(onDragged func(dy float32), onDragEnd func()) *dragHandle {
	h := &dragHandle{onDragged: onDragged, onDragEnd: onDragEnd}
	h.ExtendBaseWidget(h)
	return h
}

func (h *dragHandle) Dragged(ev *fyne.DragEvent) {
	if h.onDragged != nil {
		h.onDragged(ev.Dragged.DY)
	}
}

func (h *dragHandle) DragEnd() {
	if h.onDragEnd != nil {
		h.onDragEnd()
	}
}

func (h *dragHandle) CreateRenderer() fyne.WidgetRenderer {
	dots := make([]fyne.CanvasObject, 6)
	col := theme.Color(theme.ColorNameForeground)
	for i := range dots {
		dots[i] = canvas.NewCircle(col)
	}
	return &dragHandleRenderer{dots: dots}
}

// dragHandleRenderer lays six small circles out in a fixed 2x3 grid
// regardless of the space Fyne offers the widget, so the grip stays a
// compact, minimal dot cluster rather than stretching to fill its row.
type dragHandleRenderer struct {
	dots []fyne.CanvasObject
}

const (
	dragHandleDotSize = float32(3)
	dragHandleGap     = float32(6)
	dragHandleWidth   = dragHandleDotSize + dragHandleGap
	dragHandleHeight  = dragHandleDotSize + 2*dragHandleGap
)

func (r *dragHandleRenderer) Layout(size fyne.Size) {
	startX := (size.Width - dragHandleWidth) / 2
	startY := (size.Height - dragHandleHeight) / 2
	i := 0
	for row := 0; row < 3; row++ {
		for col := 0; col < 2; col++ {
			dot := r.dots[i]
			dot.Resize(fyne.NewSize(dragHandleDotSize, dragHandleDotSize))
			dot.Move(fyne.NewPos(startX+float32(col)*dragHandleGap, startY+float32(row)*dragHandleGap))
			i++
		}
	}
}

func (r *dragHandleRenderer) MinSize() fyne.Size {
	const pad = float32(6)
	return fyne.NewSize(dragHandleWidth+pad, dragHandleHeight+pad)
}

func (r *dragHandleRenderer) Refresh() {
	col := theme.Color(theme.ColorNameForeground)
	for _, o := range r.dots {
		o.(*canvas.Circle).FillColor = col
		o.Refresh()
	}
}

func (r *dragHandleRenderer) Objects() []fyne.CanvasObject { return r.dots }

func (r *dragHandleRenderer) Destroy() {}
