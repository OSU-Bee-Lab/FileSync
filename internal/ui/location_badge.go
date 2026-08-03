package ui

import (
	"image/color"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// locationBadge is a small filled circle with a centered number - the
// visual index a Location keeps everywhere it's shown (see
// locationNumbers), so a researcher only has to learn "3 = SharePoint"
// once. Follows the same fixed-size custom-widget pattern as dragHandle in
// location_drag_row.go.
type locationBadge struct {
	widget.BaseWidget
	number int
}

func newLocationBadge(number int) *locationBadge {
	b := &locationBadge{number: number}
	b.ExtendBaseWidget(b)
	return b
}

const locationBadgeSize = float32(16)

func (b *locationBadge) CreateRenderer() fyne.WidgetRenderer {
	circle := canvas.NewCircle(theme.Color(theme.ColorNamePrimary))
	text := canvas.NewText(strconv.Itoa(b.number), theme.Color(theme.ColorNameForegroundOnPrimary))
	text.Alignment = fyne.TextAlignCenter
	text.TextSize = 10
	return &locationBadgeRenderer{circle: circle, text: text}
}

type locationBadgeRenderer struct {
	circle *canvas.Circle
	text   *canvas.Text
}

func (r *locationBadgeRenderer) Layout(fyne.Size) {
	r.circle.Resize(fyne.NewSize(locationBadgeSize, locationBadgeSize))
	r.circle.Move(fyne.NewPos(0, 0))
	r.text.Resize(fyne.NewSize(locationBadgeSize, locationBadgeSize))
	// canvas.Text's line-height padding sits mostly below the glyph, so
	// centering on TextSize alone reads low in the circle - nudge up ~20%
	// of the badge's own size to land visually centered.
	r.text.Move(fyne.NewPos(0, (locationBadgeSize-r.text.TextSize)/2-1-0.2*locationBadgeSize))
}

func (r *locationBadgeRenderer) MinSize() fyne.Size {
	return fyne.NewSize(locationBadgeSize, locationBadgeSize)
}

func (r *locationBadgeRenderer) Refresh() {
	r.circle.FillColor = theme.Color(theme.ColorNamePrimary)
	r.text.Color = theme.Color(theme.ColorNameForegroundOnPrimary)
	r.circle.Refresh()
	r.text.Refresh()
}

func (r *locationBadgeRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.circle, r.text}
}

func (r *locationBadgeRenderer) Destroy() {}

// presenceDotSize/Gap size the small filled/outline circles presenceIndicator
// draws per Location, deliberately smaller than locationBadge's numbered
// circles since a row of them has to fit alongside a filename without
// dominating it.
const (
	presenceDotSize = float32(8)
	presenceDotGap  = float32(4)

	// presenceScrollbarMargin reserves room to the right of the dots so
	// they clear widget.List's overlaid scrollbar instead of being drawn
	// underneath it - the list gives each row its full width regardless of
	// whether a scrollbar is currently showing.
	presenceScrollbarMargin = float32(20)
)

// presenceIndicator shows, for one file/folder browser row, which of the
// browser's currently-selected Locations (in the same order as their
// badges) contain that entry: one small circle per Location - filled if
// present there, outlined if not.
type presenceIndicator struct {
	widget.BaseWidget
	present []bool
}

func newPresenceIndicator() *presenceIndicator {
	p := &presenceIndicator{}
	p.ExtendBaseWidget(p)
	return p
}

// Tapped absorbs a tap on the dots/checkmark themselves as a no-op, purely
// decorative area. Without this, a tap here isn't caught by anything (it's
// not the row's button) and falls through to widget.List's own row
// selection, which visibly "selects" the row independently of - and
// confusingly alongside - the actual name button's own selection toggle.
func (p *presenceIndicator) Tapped(*fyne.PointEvent) {}

// Update replaces the presence set this indicator shows and refreshes it.
// A nil/empty present (nothing to compare against, or this row predates a
// scan finishing) renders nothing.
func (p *presenceIndicator) Update(present []bool) {
	p.present = present
	p.Refresh()
}

func (p *presenceIndicator) CreateRenderer() fyne.WidgetRenderer {
	r := &presenceIndicatorRenderer{p: p}
	r.rebuildDots()
	return r
}

type presenceIndicatorRenderer struct {
	p    *presenceIndicator
	dots []*canvas.Circle
}

func (r *presenceIndicatorRenderer) rebuildDots() {
	r.dots = make([]*canvas.Circle, len(r.p.present))
	for i := range r.dots {
		r.dots[i] = canvas.NewCircle(color.Transparent)
		r.dots[i].StrokeWidth = 1
	}
}

func (r *presenceIndicatorRenderer) Layout(size fyne.Size) {
	x := float32(0)
	y := (size.Height - presenceDotSize) / 2
	for _, dot := range r.dots {
		dot.Resize(fyne.NewSize(presenceDotSize, presenceDotSize))
		dot.Move(fyne.NewPos(x, y))
		x += presenceDotSize + presenceDotGap
	}
}

func (r *presenceIndicatorRenderer) MinSize() fyne.Size {
	if len(r.dots) == 0 {
		return fyne.NewSize(0, 0)
	}
	width := float32(len(r.dots))*presenceDotSize + float32(len(r.dots)-1)*presenceDotGap
	return fyne.NewSize(width+presenceScrollbarMargin, presenceDotSize)
}

func (r *presenceIndicatorRenderer) Refresh() {
	if len(r.dots) != len(r.p.present) {
		r.rebuildDots()
	}
	primary := theme.Color(theme.ColorNamePrimary)
	outline := theme.Color(theme.ColorNameForeground)
	for i, dot := range r.dots {
		if r.p.present[i] {
			dot.FillColor = primary
			dot.StrokeColor = primary
		} else {
			dot.FillColor = color.Transparent
			dot.StrokeColor = outline
		}
		dot.Refresh()
	}
	r.Layout(r.MinSize())
}

func (r *presenceIndicatorRenderer) Objects() []fyne.CanvasObject {
	out := make([]fyne.CanvasObject, len(r.dots))
	for i, d := range r.dots {
		out[i] = d
	}
	return out
}

func (r *presenceIndicatorRenderer) Destroy() {}
