package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
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
	case theme.ColorNameInputBorder:
		// Stock InputBorder (e.g. an unchecked checkbox's outline) is nearly
		// invisible against its own background on a poor-contrast monitor -
		// mix it toward the foreground color for a border that actually reads
		// as a border in both themes.
		fg := t.Theme.Color(theme.ColorNameForeground, variant)
		return mix(c, fg, 0.6)
	}
	return c
}

// mix blends c1 toward c2 by amount (0 = c1, 1 = c2), keeping c1's alpha.
func mix(c1, c2 color.Color, amount float32) color.Color {
	r1, g1, b1, a1 := c1.RGBA()
	r2, g2, b2, _ := c2.RGBA()
	blend := func(v1, v2 uint32) uint8 {
		return uint8((float32(v1>>8))*(1-amount) + (float32(v2>>8))*amount)
	}
	return color.NRGBA{R: blend(r1, r2), G: blend(g1, g2), B: blend(b1, b2), A: uint8(a1 >> 8)}
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

// confirmTint wraps a confirm-field Entry so its text can show whether
// what's typed is right (see Manage Files' delete confirmation): the
// primary blue once it's correct, the normal color while it isn't, and red
// only once a submit (Preview) has flagged it - never while still typing.
// Blue rather than green for correct, so the two don't hinge on red/green
// perception, and to match the app's blue affirmative buttons.
type confirmTint struct {
	over    *container.ThemeOverride
	state   confirmTintState
	flagged bool
}

type confirmTintState int

const (
	confirmTintNormal confirmTintState = iota
	confirmTintCorrect
	confirmTintWrong
)

func newConfirmTint(e *widget.Entry) *confirmTint {
	return &confirmTint{over: container.NewThemeOverride(e, appTheme())}
}

// update recolors the field for its current correctness: blue when
// correct (which also clears any flag), red when wrong and flagged by a
// submit, otherwise normal.
func (c *confirmTint) update(correct bool) {
	if correct {
		c.flagged = false
	}
	switch {
	case correct:
		c.apply(confirmTintCorrect)
	case c.flagged:
		c.apply(confirmTintWrong)
	default:
		c.apply(confirmTintNormal)
	}
}

// apply switches the text color, refreshing only on an actual change.
func (c *confirmTint) apply(state confirmTintState) {
	if state == c.state {
		return
	}
	c.state = state
	switch state {
	case confirmTintCorrect:
		c.over.Theme = textColorTheme{Theme: appTheme(), color: theme.ColorNamePrimary}
	case confirmTintWrong:
		// Placeholder too, so a flagged field left empty still shows red.
		c.over.Theme = textColorTheme{Theme: appTheme(), color: theme.ColorNameError, placeholder: true}
	default:
		c.over.Theme = appTheme()
	}
	c.over.Refresh()
}

// appTheme is the running app's theme (lightenedTheme), or Fyne's default
// where there's no app (tests).
func appTheme() fyne.Theme {
	if a := fyne.CurrentApp(); a != nil && a.Settings().Theme() != nil {
		return a.Settings().Theme()
	}
	return theme.DefaultTheme()
}

// textColorTheme draws foreground text (and, with placeholder, an entry's
// placeholder text) in another of the theme's own colors (e.g. primary or
// error), leaving everything else as is.
type textColorTheme struct {
	fyne.Theme
	color       fyne.ThemeColorName
	placeholder bool
}

func (t textColorTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNameForeground || (t.placeholder && name == theme.ColorNamePlaceHolder) {
		name = t.color
	}
	return t.Theme.Color(name, variant)
}
