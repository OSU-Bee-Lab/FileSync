package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// lightenedTheme wraps Fyne's default theme and lightens the primary (blue,
// widget.HighImportance) and error (red, widget.DangerImportance) colors a
// few shades, so action/destructive buttons read a little softer than the
// stock theme's saturated blue/red.
type lightenedTheme struct {
	fyne.Theme
}

func newLightenedTheme() fyne.Theme {
	return lightenedTheme{Theme: theme.DefaultTheme()}
}

func (t lightenedTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	c := t.Theme.Color(name, variant)
	switch name {
	case theme.ColorNamePrimary, theme.ColorNameError:
		return lighten(c, 0.18)
	}
	return c
}

// lighten blends c toward white by amount (0-1).
func lighten(c color.Color, amount float32) color.Color {
	r, g, b, a := c.RGBA()
	blend := func(v uint32) uint8 {
		f := float32(v>>8) + (255-float32(v>>8))*amount
		if f > 255 {
			f = 255
		}
		return uint8(f)
	}
	return color.NRGBA{R: blend(r), G: blend(g), B: blend(b), A: uint8(a >> 8)}
}
