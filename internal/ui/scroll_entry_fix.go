package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// scrollForwarder is an invisible overlay that forwards mouse-wheel scroll
// events to target. Fyne's widget.Entry always keeps its own internal
// scroll region - even a single-line entry with nothing to scroll - which
// otherwise silently swallows every wheel event under the cursor before it
// reaches an enclosing Scroll, freezing scrolling wherever the mouse
// crosses a text box. Stacking one of these on top of an Entry (see
// fixEntryScrolling) intercepts the event first and redirects it instead.
type scrollForwarder struct {
	*canvas.Rectangle
	target *container.Scroll
}

func newScrollForwarder(target *container.Scroll) *scrollForwarder {
	return &scrollForwarder{Rectangle: canvas.NewRectangle(color.Transparent), target: target}
}

func (f *scrollForwarder) Scrolled(ev *fyne.ScrollEvent) {
	f.target.Scrolled(ev)
}

// fixEntryScrolling walks obj's canvas-object tree (as built for target's
// content) and replaces every *widget.Entry it finds with a stack of that
// entry plus a scrollForwarder, so mouse-wheel scrolling works everywhere
// inside target, including with the cursor over a text box. Safe to call
// repeatedly on the same subtree only if that subtree was freshly (re)built
// since the last call - calling it twice on already-wrapped entries nests
// an extra harmless layer rather than erroring, but callers whose content
// rebuilds (e.g. on a dropdown change) should fix just the rebuilt part.
func fixEntryScrolling(obj fyne.CanvasObject, target *container.Scroll) {
	switch v := obj.(type) {
	case *fyne.Container:
		for i, child := range v.Objects {
			if entry, ok := child.(*widget.Entry); ok {
				v.Objects[i] = container.NewStack(entry, newScrollForwarder(target))
				continue
			}
			fixEntryScrolling(child, target)
		}
	case *widget.Form:
		for _, item := range v.Items {
			if entry, ok := item.Widget.(*widget.Entry); ok {
				item.Widget = container.NewStack(entry, newScrollForwarder(target))
				continue
			}
			fixEntryScrolling(item.Widget, target)
		}
		v.Refresh()
	case *widget.Accordion:
		for _, item := range v.Items {
			if entry, ok := item.Detail.(*widget.Entry); ok {
				item.Detail = container.NewStack(entry, newScrollForwarder(target))
				continue
			}
			fixEntryScrolling(item.Detail, target)
		}
		v.Refresh()
	}
}
