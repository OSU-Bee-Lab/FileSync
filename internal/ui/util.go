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
	selected bool
	onTapped func()

	bg   *canvas.Rectangle
	text *canvas.Text
}

func newToggleChip(label string, onTapped func()) *toggleChip {
	c := &toggleChip{label: label, onTapped: onTapped}
	c.ExtendBaseWidget(c)
	return c
}

func (c *toggleChip) CreateRenderer() fyne.WidgetRenderer {
	c.bg = canvas.NewRectangle(color.Transparent)
	c.bg.StrokeWidth = 1.5
	c.text = canvas.NewText(c.label, theme.Color(theme.ColorNameForeground))
	c.text.Alignment = fyne.TextAlignCenter
	c.refresh()
	pad := container.NewPadded(container.NewCenter(c.text))
	return widget.NewSimpleRenderer(container.NewStack(c.bg, pad))
}

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
// options, initially selected per selected.
func newToggleGroup(options []string, selected []string) *toggleGroup {
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
		chip := newToggleChip(name, func() { g.toggle(name) })
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
