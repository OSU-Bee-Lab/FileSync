package ui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

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

func plural(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}

// locationNames returns the names of every enabled Location, for
// populating a from/to picker. Disabled locations are left out so a
// temporarily-suspended location (see Location.Enabled) can't be picked
// as a live sync endpoint.
func locationNames(locs []syncengine.Location) []string {
	var out []string
	for _, l := range locs {
		if l.Enabled {
			out = append(out, l.Name)
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

// locationNamesByKind returns the names of every Location of the given
// kind, e.g. for populating a destination picker that only makes sense for
// local folders or only for cloud remotes. Disabled locations are included
// - whether a destination is actually usable is checked at sync time, not
// baked into which locations are offered.
func locationNamesByKind(locs []syncengine.Location, kind syncengine.LocationKind) []string {
	var out []string
	for _, l := range locs {
		if l.Kind == kind {
			out = append(out, l.Name)
		}
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
