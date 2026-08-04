package ui

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// actionRow lays out a screen footer's or dialog's buttons under one rule,
// applied everywhere in the app: the way *out* of the flow (Back, Cancel,
// Exit) sits leftmost, the button that *advances or completes* the flow sits
// rightmost, and anything in between (alternative scan modes, a Retry, a
// utility like Copy Error) goes in the middle. So the rightmost button is
// always "the thing this screen is for", and the user never has to re-read a
// row to find out which side commits.
func actionRow(retreat fyne.CanvasObject, rest ...fyne.CanvasObject) *fyne.Container {
	objs := make([]fyne.CanvasObject, 0, len(rest)+1)
	if retreat != nil {
		objs = append(objs, retreat)
	}
	objs = append(objs, rest...)
	return container.NewHBox(objs...)
}

// showDangerConfirm shows a Yes/No confirmation whose confirm button is red
// (DangerImportance), for an action that interrupts a transfer in flight or
// destroys data. Use showCautionConfirm instead for one that merely
// discards unsaved work or skips a safeguard - see the importance ladder in
// theme.go.
func showDangerConfirm(title, message, confirmText, dismissText string, callback func(bool), parent fyne.Window) (*widget.Label, *widget.Button) {
	return showConfirmAction(title, message, confirmText, dismissText, widget.DangerImportance, callback, parent)
}

// showCautionConfirm is showDangerConfirm in amber (WarningImportance): the
// action is a one-way door worth pausing at, but nothing is deleted and no
// transfer is interrupted - e.g. leaving a review screen without applying
// the corrections typed into it.
func showCautionConfirm(title, message, confirmText, dismissText string, callback func(bool), parent fyne.Window) (*widget.Label, *widget.Button) {
	return showConfirmAction(title, message, confirmText, dismissText, widget.WarningImportance, callback, parent)
}

// showConfirmAction shows a Yes/No confirmation with plain text buttons (no
// check/cancel glyphs), the confirm button styled at the caller's
// importance. Wraps dialog.NewCustomWithoutButtons + SetButtons since
// dialog.NewCustomConfirm always adds theme icons to its buttons. Returns
// the message label and confirm button so callers that need to keep the
// dialog current (e.g. a syncing count that changes while it's open, and
// the confirm button's styling along with it) can update them live.
func showConfirmAction(title, message, confirmText, dismissText string, imp widget.Importance, callback func(bool), parent fyne.Window) (*widget.Label, *widget.Button) {
	msgLabel := widget.NewLabel(message)
	msgLabel.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustomWithoutButtons(title, msgLabel, parent)

	dismissBtn := widget.NewButton(dismissText, func() {
		d.Hide()
		callback(false)
	})
	confirmBtn := widget.NewButton(confirmText, func() {
		d.Hide()
		callback(true)
	})
	confirmBtn.Importance = imp
	d.SetButtons([]fyne.CanvasObject{dismissBtn, confirmBtn})
	d.Show()
	return msgLabel, confirmBtn
}

// requireNonEmpty shows an info dialog and returns false when value is
// blank (after trimming) - shared by the "Name required" / "Folder
// required" guards in the remote-setup wizard and edit-location screens.
// Takes a plain string rather than a *widget.Entry since not every check is
// entry-backed (a chosen local folder path is a bare string, not text
// typed into an Entry).
func requireNonEmpty(win fyne.Window, value, title, msg string) bool {
	if strings.TrimSpace(value) == "" {
		dialog.ShowInformation(title, msg, win)
		return false
	}
	return true
}

// pluralWord returns just the singular or plural form of a noun for a count,
// with no number attached — for callers that format the count themselves
// (e.g. through commaInt). pluralForm may be empty for the regular case,
// where the singular plus "s" is used; pass it explicitly for irregulars
// ("file copy" / "file copies").
func pluralWord(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	if pluralForm == "" {
		return singular + "s"
	}
	return pluralForm
}

// plural formats a count with its noun, picking the singular or plural form
// so user-facing text never has to hedge with "(s)" — "1 conflict", not
// "1 conflict(s)".
func plural(n int, singular, pluralForm string) string {
	return fmt.Sprintf("%d %s", n, pluralWord(n, singular, pluralForm))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// locationNames returns the names of every Location, for populating a
// from/to picker. Local and cloud locations are offered the same way;
// whether one is actually reachable is checked live at sync time (see
// missingLocalLocations), not baked into which locations are offered.
func locationNames(locs []syncengine.Location) []string {
	out := make([]string, len(locs))
	for i, l := range locs {
		out[i] = l.Name
	}
	return out
}

// locationNamesByKind is locationNames filtered to one Kind, for pickers
// that split local from cloud destinations (e.g. Sync Recorders).
func locationNamesByKind(locs []syncengine.Location, kind syncengine.LocationKind) []string {
	var out []string
	for _, l := range locs {
		if l.Kind == kind {
			out = append(out, l.Name)
		}
	}
	return out
}

// roleLabels are the Add/Edit Location Role select's options, index-matched
// to syncengine.RoleAudio/RoleResults.
var roleLabels = []string{"Audio", "Results"}

func roleFromLabel(label string) syncengine.LocationRole {
	if label == roleLabels[1] {
		return syncengine.RoleResults
	}
	return syncengine.RoleAudio
}

func labelFromRole(role syncengine.LocationRole) string {
	if role == syncengine.RoleResults {
		return roleLabels[1]
	}
	return roleLabels[0]
}

// filterByRole returns every Location whose Role matches role, for pickers
// that must never mix Audio and Results locations in one N-way run (e.g.
// Sync Experiments).
func filterByRole(locs []syncengine.Location, role syncengine.LocationRole) []syncengine.Location {
	var out []syncengine.Location
	for _, l := range locs {
		if l.Role == role {
			out = append(out, l)
		}
	}
	return out
}

// locationNamesByKindAndRole is locationNamesByKind additionally filtered to
// one Role, for pickers that need both splits at once (e.g. Sync Recorders,
// which offers only RoleAudio locations).
func locationNamesByKindAndRole(locs []syncengine.Location, kind syncengine.LocationKind, role syncengine.LocationRole) []string {
	var out []string
	for _, l := range locs {
		if l.Kind == kind && l.Role == role {
			out = append(out, l.Name)
		}
	}
	return out
}

// locationsFromNamesAny resolves a set of selected Names back into
// Locations regardless of Kind, for pickers that don't separate local from
// cloud (e.g. Sync Experiments' "To").
func locationsFromNamesAny(locs []syncengine.Location, names []string) []syncengine.Location {
	var out []syncengine.Location
	for _, name := range names {
		if loc := findLocation(locs, name); loc != nil {
			out = append(out, *loc)
		}
	}
	return out
}

func findLocation(locs []syncengine.Location, name string) *syncengine.Location {
	for i := range locs {
		if locs[i].Name == name {
			return &locs[i]
		}
	}
	return nil
}

func findLocationByID(locs []syncengine.Location, id string) *syncengine.Location {
	for i := range locs {
		if locs[i].ID == id {
			return &locs[i]
		}
	}
	return nil
}

// containsLocation reports whether loc appears in locs, by ID.
func containsLocation(locs []syncengine.Location, loc syncengine.Location) bool {
	for _, l := range locs {
		if l.ID == loc.ID {
			return true
		}
	}
	return false
}

// selectedFromIDs converts a set of persisted Location IDs into the
// matching Location Names, for pre-populating a toggleGroup's selection
// from RecorderSettings.
func selectedFromIDs(locs []syncengine.Location, ids []string) []string {
	var out []string
	for _, id := range ids {
		if loc := findLocationByID(locs, id); loc != nil {
			out = append(out, loc.Name)
		}
	}
	return out
}

// locationsFromNames resolves a CheckGroup's selected Names back into
// Locations of the given kind.
func locationsFromNames(locs []syncengine.Location, names []string, kind syncengine.LocationKind) []syncengine.Location {
	var out []syncengine.Location
	for _, name := range names {
		if loc := findLocation(locs, name); loc != nil && loc.Kind == kind {
			out = append(out, *loc)
		}
	}
	return out
}

func idsFromLocations(locs []syncengine.Location) []string {
	ids := make([]string, len(locs))
	for i, l := range locs {
		ids[i] = l.ID
	}
	return ids
}

// locationNumbers maps every Location's Name to its 1-based position within
// all - always the full, canonical s.cfg.Locations, never a filtered
// subset, so a Location's badge number is identical everywhere it's shown
// even though different pickers offer different subsets (e.g. local-only vs
// remote-only). Purely a live display index, not persisted: reordering
// Locations on the Locations screen renumbers them.
func locationNumbers(all []syncengine.Location) map[string]int {
	out := make(map[string]int, len(all))
	for i, l := range all {
		out[l.Name] = i + 1
	}
	return out
}

// joinRel joins a browsing breadcrumb path with a child name, both always
// forward-slash separated (an rclone-relative path, not an OS path).
func joinRel(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}

// toggleChipSelectedFill/Stroke are the "selected" look for a toggleChip: a
// blue perimeter with a light blue fill, distinct from the theme's normal
// button styling so a multi-select group of chips reads clearly at a
// glance.
var (
	toggleChipSelectedStroke = color.NRGBA{R: 0x1C, G: 0x6D, B: 0xD0, A: 0xFF}
	toggleChipSelectedFill   = color.NRGBA{R: 0xD8, G: 0xE9, B: 0xFC, A: 0xFF}
)

// toggleChip is a tappable, selectable pill used to build a multi-select
// button group (see newToggleGroup) - the button-based replacement for a
// widget.CheckGroup. Selected chips get a blue perimeter and light blue
// fill; unselected chips look like a plain outlined button.
type toggleChip struct {
	widget.BaseWidget
	label    string
	number   int // 0 = no badge
	selected bool
	onTapped func()

	bg   *canvas.Rectangle
	text *canvas.Text
}

func newToggleChip(label string, number int, onTapped func()) *toggleChip {
	c := &toggleChip{label: label, number: number, onTapped: onTapped}
	c.ExtendBaseWidget(c)
	return c
}

// chipBadgeOverhang is how far a toggleChip's numbered badge pokes outside
// the chip's own top-right corner, notification-bubble style - half the
// badge's own size, so the badge is centered exactly on the corner rather
// than tucked inside it overlapping the label.
const chipBadgeOverhang = locationBadgeSize / 2

func (c *toggleChip) CreateRenderer() fyne.WidgetRenderer {
	c.bg = canvas.NewRectangle(color.Transparent)
	c.bg.StrokeWidth = 1.5
	c.text = canvas.NewText(c.label, theme.Color(theme.ColorNameForeground))
	c.text.Alignment = fyne.TextAlignCenter
	c.refresh()
	r := &toggleChipRenderer{c: c}
	if c.number != 0 {
		r.badge = newLocationBadge(c.number)
	}
	return r
}

// toggleChipRenderer lays the chip's background+label out inset from the
// widget's own top-right corner by chipBadgeOverhang (only when there's a
// badge), then centers the badge on that corner - so the badge floats half
// outside the chip's border like a notification count, never overlapping
// the label. A plain custom renderer rather than nested stack/border
// containers, since "half outside the box" isn't expressible with Fyne's
// stock layouts.
type toggleChipRenderer struct {
	c     *toggleChip
	badge *locationBadge
}

func (r *toggleChipRenderer) marginTop() float32 {
	if r.badge == nil {
		return 0
	}
	return chipBadgeOverhang
}

func (r *toggleChipRenderer) marginRight() float32 {
	if r.badge == nil {
		return 0
	}
	return chipBadgeOverhang
}

func (r *toggleChipRenderer) Layout(size fyne.Size) {
	mt, mr := r.marginTop(), r.marginRight()
	bgSize := fyne.NewSize(size.Width-mr, size.Height-mt)
	r.c.bg.Move(fyne.NewPos(0, mt))
	r.c.bg.Resize(bgSize)

	textMin := r.c.text.MinSize()
	r.c.text.Resize(textMin)
	r.c.text.Move(fyne.NewPos((bgSize.Width-textMin.Width)/2, mt+(bgSize.Height-textMin.Height)/2))

	if r.badge != nil {
		r.badge.Resize(fyne.NewSize(locationBadgeSize, locationBadgeSize))
		r.badge.Move(fyne.NewPos(bgSize.Width-locationBadgeSize/2, mt-locationBadgeSize/2))
	}
}

func (r *toggleChipRenderer) MinSize() fyne.Size {
	pad := theme.Padding()
	textMin := r.c.text.MinSize()
	bgMin := fyne.NewSize(textMin.Width+4*pad, textMin.Height+2*pad)
	return fyne.NewSize(bgMin.Width+r.marginRight(), bgMin.Height+r.marginTop())
}

func (r *toggleChipRenderer) Refresh() {
	r.c.refresh()
	r.c.text.Text = r.c.label
	r.c.text.Color = theme.Color(theme.ColorNameForeground)
	r.c.text.Refresh()
	if r.badge != nil {
		r.badge.Refresh()
	}
	r.Layout(r.c.Size())
}

func (r *toggleChipRenderer) Objects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{r.c.bg, r.c.text}
	if r.badge != nil {
		objs = append(objs, r.badge)
	}
	return objs
}

func (r *toggleChipRenderer) Destroy() {}

func (c *toggleChip) refresh() {
	if c.selected {
		c.bg.StrokeColor = toggleChipSelectedStroke
		c.bg.FillColor = toggleChipSelectedFill
	} else {
		c.bg.StrokeColor = theme.Color(theme.ColorNameDisabled)
		c.bg.FillColor = color.Transparent
	}
	c.bg.Refresh()
}

func (c *toggleChip) SetSelected(selected bool) {
	c.selected = selected
	if c.bg != nil {
		c.refresh()
	}
}

func (c *toggleChip) Tapped(*fyne.PointEvent) {
	if c.onTapped != nil {
		c.onTapped()
	}
}

func (c *toggleChip) MinSize() fyne.Size {
	c.ExtendBaseWidget(c)
	return c.BaseWidget.MinSize()
}

// toggleGroup is a multi-select group of toggleChips, a button-styled
// stand-in for widget.CheckGroup.
type toggleGroup struct {
	box       *fyne.Container
	options   []string
	chips     map[string]*toggleChip
	selected  map[string]bool
	OnChanged func([]string)
}

// newToggleGroup builds a toggleGroup offering one chip per name in
// options, initially selected per selected. numbers gives each chip its
// badge number by name (see locationNumbers) - a name missing from numbers,
// or mapped to 0, gets no badge.
func newToggleGroup(options []string, selected []string, numbers map[string]int) *toggleGroup {
	g := &toggleGroup{
		box:      container.NewHBox(),
		options:  options,
		chips:    map[string]*toggleChip{},
		selected: map[string]bool{},
	}
	for _, name := range selected {
		g.selected[name] = true
	}
	for _, name := range options {
		name := name
		chip := newToggleChip(name, numbers[name], func() { g.toggle(name) })
		chip.SetSelected(g.selected[name])
		g.chips[name] = chip
		g.box.Add(chip)
	}
	return g
}

// SetSelected replaces the current selection wholesale, e.g. after the
// caller drops names that turned out to be inaccessible.
func (g *toggleGroup) SetSelected(selected []string) {
	g.selected = map[string]bool{}
	for _, name := range selected {
		g.selected[name] = true
	}
	for name, chip := range g.chips {
		chip.SetSelected(g.selected[name])
	}
}

func (g *toggleGroup) toggle(name string) {
	g.selected[name] = !g.selected[name]
	g.chips[name].SetSelected(g.selected[name])
	if g.OnChanged != nil {
		g.OnChanged(g.Selected())
	}
}

// Selected returns the currently-selected chip names, in options order.
func (g *toggleGroup) Selected() []string {
	var out []string
	for _, name := range g.options {
		if g.selected[name] {
			out = append(out, name)
		}
	}
	return out
}

func (g *toggleGroup) CanvasObject() fyne.CanvasObject { return g.box }
