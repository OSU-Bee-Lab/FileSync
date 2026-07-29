package ui

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// audioSpinnerCycle is one full pulse of the loading indicator. Fyne's
// widget.Activity, which this otherwise mirrors, hardcodes 2 seconds; with its
// three phase-offset circles that puts a pulse on screen roughly every half
// second, which at the size of a list row reads as frantic shaking rather than
// "opening the file". Four seconds gives the same shape a slow breath.
const audioSpinnerCycle = 4 * time.Second

// audioSpinner is a calmer widget.Activity: the same three concentric circles
// growing and fading in sequence, but on audioSpinnerCycle and with a linear
// curve. The curve matters as much as the duration - Fyne's version leaves it
// at the default ease-in-out, which lands the pulse in a lurch instead of an
// even swell.
//
// This is a copy of that widget rather than a wrapper because neither its
// duration nor its curve is reachable from outside: both live in the animation
// its renderer builds privately.
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
	dots := make([]fyne.CanvasObject, 3)
	for i := range dots {
		dots[i] = canvas.NewCircle(color.Transparent)
	}
	r := &audioSpinnerRenderer{dots: dots, parent: s}
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

	bound   fyne.Size
	maxCol  color.NRGBA
	maxRad  float32
	started bool
}

func (r *audioSpinnerRenderer) MinSize() fyne.Size {
	return fyne.NewSquareSize(r.parent.Theme().Size(theme.SizeNameInlineIcon))
}

func (r *audioSpinnerRenderer) Layout(size fyne.Size) {
	// The circles are drawn from the centre outwards, so the radius is bound
	// by the shorter side - a row stretches this taller than it is wide.
	r.maxRad = fyne.Min(size.Width, size.Height) / 2
	r.bound = size
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
	// Leave nothing drawn behind: a stopped spinner is a hidden one.
	for _, dot := range r.dots {
		circle := dot.(*canvas.Circle)
		circle.FillColor = color.Transparent
		circle.Refresh()
	}
}

// tick drives the three circles from one animation, each a third of a cycle
// apart, so one is always swelling as another fades out.
func (r *audioSpinnerRenderer) tick(done float32) {
	for i, dot := range r.dots {
		phase := done + float32(i)/3
		if phase >= 1 {
			phase -= 1
		}
		r.drawDot(dot.(*canvas.Circle), triangle(phase))
	}
}

// triangle turns a 0..1 position in the cycle into a 0..1..0 swell, so each
// circle grows and shrinks once per cycle rather than jumping back at the end.
func triangle(phase float32) float32 {
	if phase > 0.5 {
		return 2 - phase*2
	}
	return phase * 2
}

// drawDot sizes and fades one circle: off 0 is fully grown and invisible, off 1
// is small and at full strength.
func (r *audioSpinnerRenderer) drawDot(dot *canvas.Circle, off float32) {
	rad := r.maxRad - r.maxRad*off/1.2
	mid := fyne.NewPos(r.bound.Width/2, r.bound.Height/2)

	dot.Move(mid.Subtract(fyne.NewSquareOffsetPos(rad)))
	dot.Resize(fyne.NewSquareSize(rad * 2))
	dot.FillColor = color.NRGBA{R: r.maxCol.R, G: r.maxCol.G, B: r.maxCol.B, A: uint8(float32(r.maxCol.A) * off)}
	dot.Refresh()
}

func (r *audioSpinnerRenderer) updateColor() {
	variant := fyne.CurrentApp().Settings().ThemeVariant()
	rr, gg, bb, aa := r.parent.Theme().Color(theme.ColorNameForeground, variant).RGBA()
	r.maxCol = color.NRGBA{R: uint8(rr >> 8), G: uint8(gg >> 8), B: uint8(bb >> 8), A: uint8(aa >> 8)}
}
