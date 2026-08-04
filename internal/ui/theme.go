package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Button importance ladder
//
// One meaning per color, app-wide. Pick by what the button *does*, never by
// where it sits in the row (that's actionRow's job, see util.go):
//
//   - HighImportance (blue): the affirmative action the screen exists to
//     perform - Sync, Scan, Save, Apply, Start, Continue. At most one per row.
//   - MediumImportance (default): navigation and alternatives - Back, Done,
//     Full Scan next to Quick Scan, Cancel of something not yet running.
//   - WarningImportance (amber): proceeds, but discards work or skips a
//     safeguard - leaving a review without applying corrections, syncing past
//     unresolved conflicts, ending a session with uploads still pending.
//     Nothing is deleted and nothing in flight is interrupted.
//   - DangerImportance (red): interrupts a transfer that is actually running,
//     or deletes/overwrites data. Red is the app's "this destroys something"
//     signal and must not be spent on merely-final actions - two buttons that
//     end up in the same place should never be styled blue and red.
//   - LowImportance: incidental affordances that shouldn't compete - Details…,
//     media transport controls.
//
// Row/list styling (e.g. the folder browser's selected-path buttons) uses
// High/Medium purely as a selected/unselected shade and is outside this
// ladder.
//
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
