package ui

import (
	"image/color"
	"math"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	// audioSpinnerCycle is one full revolution. A second or so is what every
	// platform's spinner does, and at spinnerDots steps that works out to a
	// step every ~125ms - brisk enough to read as motion, slow enough not to
	// blur into a flicker.
	audioSpinnerCycle = 1100 * time.Millisecond

	// spinnerDots is how many dots make up the ring.
	spinnerDots = 8

	// spinnerTrailDim is the alpha of the dimmest dot (the one furthest behind
	// the head) as a fraction of full strength. Keeping it above zero leaves
	// the whole ring faintly present, so what moves is the bright head rather
	// than dots appearing out of nowhere.
	spinnerTrailDim = 0.15

	// spinnerDotScale sizes each dot relative to the ring's radius.
	spinnerDotScale = 0.28
)

// audioSpinner is the ordinary ring-of-dots spinner: a bright head travelling
// around a faint ring, one revolution per audioSpinnerCycle.
//
// It steps rather than sweeping - the head jumps from dot to dot, as the
// platform spinners it imitates do - so it repaints spinnerDots times per
// revolution instead of once per frame.
//
// Fyne's own widget.Activity is not this: it pulses three concentric circles,
// and neither its timing nor its curve can be reached from outside the widget.
type audioSpinner struct {
	widget.BaseWidget

	running bool
}

func newAudioSpinner() *audioSpinner {
	s := &audioSpinner{}
	s.ExtendBaseWidget(s)
	return s
}

func (s *audioSpinner) MinSize() fyne.Size {
	s.ExtendBaseWidget(s)
	return s.BaseWidget.MinSize()
}

// Start begins animating. Safe to call when already running.
func (s *audioSpinner) Start() {
	if s.running {
		return
	}
	s.running = true
	s.Refresh()
}

// Stop halts the animation. Safe to call when already stopped.
func (s *audioSpinner) Stop() {
	if !s.running {
		return
	}
	s.running = false
	s.Refresh()
}

func (s *audioSpinner) CreateRenderer() fyne.WidgetRenderer {
	dots := make([]fyne.CanvasObject, spinnerDots)
	for i := range dots {
		dots[i] = canvas.NewCircle(color.Transparent)
	}
	r := &audioSpinnerRenderer{dots: dots, parent: s, head: -1}
	r.anim = &fyne.Animation{
		Duration:    audioSpinnerCycle,
		RepeatCount: fyne.AnimationRepeatForever,
		Curve:       fyne.AnimationLinear,
		Tick:        r.tick,
	}
	r.updateColor()
	if s.running {
		r.start()
	}
	return r
}

type audioSpinnerRenderer struct {
	anim   *fyne.Animation
	dots   []fyne.CanvasObject
	parent *audioSpinner

	baseCol color.NRGBA
	started bool

	// head is which dot is currently brightest, or -1 when nothing has been
	// drawn yet. Held so a tick that hasn't moved the head can return without
	// touching the canvas.
	head int
}

func (r *audioSpinnerRenderer) MinSize() fyne.Size {
	return fyne.NewSquareSize(r.parent.Theme().Size(theme.SizeNameInlineIcon))
}

// Layout places the dots evenly around the ring. They never move afterwards -
// only their colour changes - so this is the only place geometry is computed.
func (r *audioSpinnerRenderer) Layout(size fyne.Size) {
	// The ring is bound by the shorter side: a list row stretches this widget
	// taller than it is wide.
	radius := fyne.Min(size.Width, size.Height) / 2
	dotRad := radius * spinnerDotScale
	ring := radius - dotRad
	midX, midY := size.Width/2, size.Height/2

	for i, dot := range r.dots {
		// Start at twelve o'clock and go clockwise, like every other spinner.
		angle := 2*math.Pi*float64(i)/spinnerDots - math.Pi/2
		x := midX + ring*float32(math.Cos(angle))
		y := midY + ring*float32(math.Sin(angle))
		dot.Resize(fyne.NewSquareSize(dotRad * 2))
		dot.Move(fyne.NewPos(x-dotRad, y-dotRad))
	}
}

func (r *audioSpinnerRenderer) Objects() []fyne.CanvasObject { return r.dots }

func (r *audioSpinnerRenderer) Refresh() {
	switch {
	case r.parent.running && !r.started:
		r.start()
	case !r.parent.running && r.started:
		r.stop()
	}
	r.updateColor()
}

func (r *audioSpinnerRenderer) Destroy() {
	r.parent.running = false
	r.stop()
}

func (r *audioSpinnerRenderer) start() {
	r.started = true
	r.anim.Start()
}

func (r *audioSpinnerRenderer) stop() {
	r.started = false
	r.anim.Stop()
	// Leave nothing drawn behind: a stopped spinner is an invisible one.
	r.head = -1
	for _, dot := range r.dots {
		circle := dot.(*canvas.Circle)
		circle.FillColor = color.Transparent
		circle.Refresh()
	}
}

// tick advances the bright head around the ring, repainting only when it has
// actually moved on to the next dot.
func (r *audioSpinnerRenderer) tick(done float32) {
	head := int(done * spinnerDots)
	if head >= spinnerDots {
		head = spinnerDots - 1
	}
	if head == r.head {
		return
	}
	r.head = head

	for i, dot := range r.dots {
		// How far this dot sits behind the head, 0 (the head itself) to 1.
		behind := float32((head-i+spinnerDots)%spinnerDots) / spinnerDots
		strength := 1 - behind*(1-spinnerTrailDim)

		circle := dot.(*canvas.Circle)
		circle.FillColor = color.NRGBA{
			R: r.baseCol.R,
			G: r.baseCol.G,
			B: r.baseCol.B,
			A: uint8(float32(r.baseCol.A) * strength),
		}
		circle.Refresh()
	}
}

func (r *audioSpinnerRenderer) updateColor() {
	variant := fyne.CurrentApp().Settings().ThemeVariant()
	rr, gg, bb, aa := r.parent.Theme().Color(theme.ColorNameForeground, variant).RGBA()
	r.baseCol = color.NRGBA{R: uint8(rr >> 8), G: uint8(gg >> 8), B: uint8(bb >> 8), A: uint8(aa >> 8)}
}
