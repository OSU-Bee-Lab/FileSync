package ui

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// tooltipDelay is how long the pointer must rest over a hoverLabel before its
// tooltip appears - long enough that normal mouse travel across a list
// doesn't flicker tooltips on every row passed over.
const tooltipDelay = 500 * time.Millisecond

// hoverLabel is a widget.Label that, after tooltipDelay of mouse-hover, shows
// a popup with its full text - but only when the label's own ellipsis
// truncation means the visible text doesn't already show everything. Used for
// the sync progress screen's Experiments/Folders/Files row names, which are
// often longer than the column width.
type hoverLabel struct {
	*widget.Label
	win   fyne.Window
	popUp *widget.PopUp
	timer *time.Timer
}

func newHoverLabel(win fyne.Window) *hoverLabel {
	l := widget.NewLabel("")
	l.Truncation = fyne.TextTruncateEllipsis
	return &hoverLabel{Label: l, win: win}
}

func (h *hoverLabel) MouseIn(e *desktop.MouseEvent) {
	h.schedule(e.AbsolutePosition)
}

func (h *hoverLabel) MouseMoved(e *desktop.MouseEvent) {
	if h.popUp == nil {
		h.schedule(e.AbsolutePosition)
	}
}

func (h *hoverLabel) MouseOut() {
	h.reset()
}

func (h *hoverLabel) schedule(pos fyne.Position) {
	h.cancelTimer()
	h.timer = time.AfterFunc(tooltipDelay, func() {
		fyne.Do(func() { h.show(pos) })
	})
}

func (h *hoverLabel) cancelTimer() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

// show displays the tooltip, but only if the label's text is actually wider
// than the space it's been given - a short name has nothing extra to reveal.
func (h *hoverLabel) show(pos fyne.Position) {
	text := h.Label.Text
	if text == "" {
		return
	}
	measured := fyne.MeasureText(text, theme.TextSize(), h.Label.TextStyle)
	if measured.Width <= h.Label.Size().Width {
		return
	}

	tipLabel := widget.NewLabel(text)
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameOverlayBackground))
	bg.StrokeColor = theme.Color(theme.ColorNameInputBorder)
	bg.StrokeWidth = 1
	box := container.NewStack(bg, container.NewPadded(tipLabel))

	h.popUp = widget.NewPopUp(box, h.win.Canvas())
	h.popUp.ShowAtPosition(pos.Add(fyne.NewPos(12, 12)))
}

// reset cancels any pending tooltip timer and hides an already-shown tooltip.
// Called on mouse-out, and also from updateBackingBarItem/updateDividerItem
// since widget.List recycles row objects across different data as the user
// scrolls - without this a tooltip scheduled/shown for one row could linger
// after the object underneath it has been repurposed for another.
func (h *hoverLabel) reset() {
	h.cancelTimer()
	if h.popUp != nil {
		h.popUp.Hide()
		h.popUp = nil
	}
}

// hoverIcon is an icon that pops up a text tooltip after tooltipDelay of
// hover. Used on conflict rows in the sync progress list to surface the
// conflict reason on demand without crowding the row with long text — the
// same detail also appears in the resolver modal. Unlike hoverLabel it always
// shows its tip (an icon has no visible text that could already reveal it).
type hoverIcon struct {
	widget.Icon
	win   fyne.Window
	tip   string
	popUp *widget.PopUp
	timer *time.Timer
}

func newHoverIcon(win fyne.Window, res fyne.Resource) *hoverIcon {
	h := &hoverIcon{win: win}
	h.ExtendBaseWidget(h)
	h.SetResource(res)
	return h
}

func (h *hoverIcon) setTip(tip string) { h.tip = tip }

func (h *hoverIcon) MouseIn(e *desktop.MouseEvent) { h.schedule(e.AbsolutePosition) }

func (h *hoverIcon) MouseMoved(e *desktop.MouseEvent) {
	if h.popUp == nil {
		h.schedule(e.AbsolutePosition)
	}
}

func (h *hoverIcon) MouseOut() { h.reset() }

func (h *hoverIcon) schedule(pos fyne.Position) {
	h.cancelTimer()
	if h.tip == "" {
		return
	}
	h.timer = time.AfterFunc(tooltipDelay, func() {
		fyne.Do(func() { h.show(pos) })
	})
}

func (h *hoverIcon) cancelTimer() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

func (h *hoverIcon) show(pos fyne.Position) {
	if h.tip == "" {
		return
	}
	tipLabel := widget.NewLabel(h.tip)
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameOverlayBackground))
	bg.StrokeColor = theme.Color(theme.ColorNameInputBorder)
	bg.StrokeWidth = 1
	box := container.NewStack(bg, container.NewPadded(tipLabel))
	h.popUp = widget.NewPopUp(box, h.win.Canvas())
	h.popUp.ShowAtPosition(pos.Add(fyne.NewPos(12, 12)))
}

// reset mirrors hoverLabel.reset for the same list-recycling reason.
func (h *hoverIcon) reset() {
	h.cancelTimer()
	if h.popUp != nil {
		h.popUp.Hide()
		h.popUp = nil
	}
}
