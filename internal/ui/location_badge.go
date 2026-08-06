package ui

import (
	"image/color"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// locationBadgeGoldColor is a Results location's badge fill, distinct from
// an Audio location's theme-primary blue (see badgeFillColor) so the two
// roles read apart at a glance in a mixed picker (newRoleToggleGroup).
var locationBadgeGoldColor = color.NRGBA{R: 0xC9, G: 0x9A, B: 0x1C, A: 0xFF}

// badgeFillColor picks a locationBadge's circle color by the Location Role
// it represents: theme-primary blue for Audio, a fixed gold for Results.
// Resolved live (not cached) so the blue half stays correct across a
// light/dark theme switch.
func badgeFillColor(role syncengine.LocationRole) color.Color {
	if role == syncengine.RoleResults {
		return locationBadgeGoldColor
	}
	return theme.Color(theme.ColorNamePrimary)
}

// roleLabel is a bold canvas.Text colored by badgeFillColor - a widget
// (rather than a bare canvas.Text dropped in a container) so Fyne's
// theme-change propagation calls its Refresh and it stays correct if the
// app switches light/dark while running, same as locationBadge. Used on
// Manage Locations so a Location's row reads Audio/Results at a glance
// alongside its badges everywhere else (see badgeFillColor).
type roleLabel struct {
	widget.BaseWidget
	text string
	role syncengine.LocationRole
}

func newRoleLabel(text string, role syncengine.LocationRole) *roleLabel {
	l := &roleLabel{text: text, role: role}
	l.ExtendBaseWidget(l)
	return l
}

func (l *roleLabel) CreateRenderer() fyne.WidgetRenderer {
	t := canvas.NewText(l.text, badgeFillColor(l.role))
	t.TextStyle.Bold = true
	return &roleLabelRenderer{l: l, text: t}
}

type roleLabelRenderer struct {
	l    *roleLabel
	text *canvas.Text
}

func (r *roleLabelRenderer) Layout(size fyne.Size)        { r.text.Resize(size) }
func (r *roleLabelRenderer) MinSize() fyne.Size           { return r.text.MinSize() }
func (r *roleLabelRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.text} }
func (r *roleLabelRenderer) Destroy()                     {}

func (r *roleLabelRenderer) Refresh() {
	r.text.Text = r.l.text
	r.text.Color = badgeFillColor(r.l.role)
	r.text.Refresh()
}

// locationBadge is a small filled circle with a centered number - the
// visual index a Location keeps in a picker while selected (see
// toggleGroup.renumber), assigned dynamically by selection order rather
// than a fixed position, so a researcher only has to learn "3 = SharePoint"
// for as long as that selection lasts. Follows the same fixed-size
// custom-widget pattern as dragHandle in location_drag_row.go.
type locationBadge struct {
	widget.BaseWidget
	number int
	role   syncengine.LocationRole
}

func newLocationBadge(number int, role syncengine.LocationRole) *locationBadge {
	b := &locationBadge{number: number, role: role}
	b.ExtendBaseWidget(b)
	return b
}

const locationBadgeSize = float32(16)

// SetNumber updates the badge's displayed number (0 = about to be hidden by
// the caller) and refreshes it.
func (b *locationBadge) SetNumber(number int) {
	if b.number == number {
		return
	}
	b.number = number
	b.Refresh()
}

func (b *locationBadge) CreateRenderer() fyne.WidgetRenderer {
	circle := canvas.NewCircle(badgeFillColor(b.role))
	text := canvas.NewText(strconv.Itoa(b.number), color.White)
	text.Alignment = fyne.TextAlignCenter
	text.TextSize = 10
	return &locationBadgeRenderer{b: b, circle: circle, text: text}
}

type locationBadgeRenderer struct {
	b      *locationBadge
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
	r.circle.FillColor = badgeFillColor(r.b.role)
	r.text.Text = strconv.Itoa(r.b.number)
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
// badges) contain that entry: one small circle per Location, colored by
// that Location's Role like its badge (see badgeFillColor) - filled if
// present there, just an outline in that color if not.
type presenceIndicator struct {
	widget.BaseWidget
	present []bool
	roles   []syncengine.LocationRole
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
// present and roles are index-matched to the browser's current Locations
// (see destFolderBrowser.locs). A nil/empty present (nothing to compare
// against, or this row predates a scan finishing) renders nothing.
func (p *presenceIndicator) Update(present []bool, roles []syncengine.LocationRole) {
	p.present = present
	p.roles = roles
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
	for i, dot := range r.dots {
		var role syncengine.LocationRole
		if i < len(r.p.roles) {
			role = r.p.roles[i]
		}
		roleColor := badgeFillColor(role)
		dot.StrokeColor = roleColor
		if r.p.present[i] {
			dot.FillColor = roleColor
		} else {
			dot.FillColor = color.Transparent
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
