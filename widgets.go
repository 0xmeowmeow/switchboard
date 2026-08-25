// widgets.go — small animated things that fill the empty space.
//
// Every widget is a function (w, h, t) -> a field of intensities in [0,1]
// plus an optional hue offset per cell. Nothing is stored as an asset: they
// are computed per frame, so they cost no disk and adapt to any size.
//
// Rendering is separate from generation, which is what makes colour cycling
// free. The classic Amiga trick is not to redraw the image but to rotate the
// palette underneath it; here the glyph field stays put and the colour ramp
// index advances, which is the same thing.
package main

import (
	"math"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"switchboard/effects"
)

// sgr matches an ANSI colour escape.
var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// A cell carries how bright it is and where it sits on the hue ramp. Keeping
// those separate is what lets one field be re-coloured without regenerating.
type wcell struct {
	v   float64 // 0 = empty, 1 = solid
	hue float64 // 0..1 along the theme ramp
}

type widget struct {
	Name string
	Gen  func(w, h int, t float64) [][]wcell
}

// The glyph ramp. Density increases left to right; the block characters at
// the end need a terminal that can draw them, which is why the bare TTY is
// not an option.
var ramp = []rune(" ·∙•◦○◍●▒▓█")

func glyphFor(v float64) rune {
	if v <= 0.02 {
		return ' '
	}
	i := int(v * float64(len(ramp)-1))
	if i >= len(ramp) {
		i = len(ramp) - 1
	}
	if i < 0 {
		i = 0
	}
	return ramp[i]
}

// ---------------------------------------------------------------- globe

// globe is a rotating sphere with a graticule. Shading is Lambertian against
// a fixed light, and the longitude lines advance with t, so it reads as
// rotation rather than as a flickering circle.
func globe(w, h int, t float64) [][]wcell {
	f := blank(w, h)
	// terminal cells are about twice as tall as wide
	cx, cy := float64(w)/2, float64(h)/2
	r := math.Min(float64(w)/2, float64(h))*0.9 - 1
	if r < 3 {
		return f
	}
	lx, ly, lz := -0.4, -0.5, 0.75 // light direction

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// unproject the cell to the unit sphere, correcting the aspect
			nx := (float64(x) - cx) / r
			ny := ((float64(y) - cy) * 2) / r
			d := nx*nx + ny*ny
			if d > 1 {
				continue
			}
			nz := math.Sqrt(1 - d)
			lambert := nx*lx + ny*ly + nz*lz
			if lambert < 0 {
				lambert = 0
			}
			v := 0.15 + 0.85*lambert

			// spherical coordinates, with longitude turning over time
			lat := math.Asin(ny)
			lon := math.Atan2(nx, nz) + t*0.9

			// graticule: brighten near whole divisions of lat and lon
			const step = math.Pi / 6
			dl := math.Abs(math.Mod(lon+10*math.Pi, step)/step - 0.5)
			da := math.Abs(math.Mod(lat+10*math.Pi, step)/step - 0.5)
			if dl > 0.46 || da > 0.46 {
				v = math.Min(1, v+0.45)
			}
			// limb darkening keeps the edge from looking cut out
			v *= 0.55 + 0.45*nz
			f[y][x] = wcell{v: v, hue: 0.5 + 0.5*math.Sin(lat*2+t*0.3)}
		}
	}
	return f
}

// ---------------------------------------------------------------- galaxy

// galaxy is a logarithmic spiral with a bright core, rotating differentially
// — the inside turns faster than the rim, which is what real ones do and
// what makes the arms shear convincingly.
func galaxy(w, h int, t float64) [][]wcell {
	f := blank(w, h)
	cx, cy := float64(w)/2, float64(h)/2
	scale := math.Min(float64(w)/2, float64(h)) * 0.95
	if scale < 3 {
		return f
	}
	const arms = 2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nx := (float64(x) - cx) / scale
			ny := ((float64(y) - cy) * 2) / scale
			rad := math.Hypot(nx, ny)
			if rad > 1.05 {
				continue
			}
			ang := math.Atan2(ny, nx)
			// differential rotation: angular speed falls off with radius
			ang += t * 0.8 / (0.25 + rad)
			// distance to the nearest arm of a log spiral
			phase := ang - math.Log(rad+0.08)*2.6
			d := math.Abs(math.Sin(phase * arms / 2))
			v := math.Pow(d, 6) * (1 - rad)
			// the core
			v += math.Exp(-rad*rad*14) * 0.9
			if v <= 0 {
				continue
			}
			f[y][x] = wcell{v: math.Min(1, v), hue: math.Min(1, rad*1.2)}
		}
	}
	return f
}

// ---------------------------------------------------------------- plasma

// plasma is the demoscene standby: summed sinusoids, no structure, and the
// perfect surface for colour cycling because the field barely changes while
// the palette moves under it.
func plasma(w, h int, t float64) [][]wcell {
	f := blank(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/6, float64(y)/3
			v := math.Sin(fx+t) +
				math.Sin(fy+t*0.7) +
				math.Sin((fx+fy+t)/2) +
				math.Sin(math.Hypot(fx-8, fy-4)/2+t)
			n := (v + 4) / 8 // into 0..1
			f[y][x] = wcell{v: 0.25 + 0.75*n, hue: math.Mod(n+t*0.05, 1)}
		}
	}
	return f
}

// ---------------------------------------------------------------- julia

// julia is a fractal whose parameter walks a circle, so the shape morphs
// continuously instead of sitting still.
func julia(w, h int, t float64) [][]wcell {
	f := blank(w, h)
	cr := 0.7885 * math.Cos(t*0.25)
	ci := 0.7885 * math.Sin(t*0.25)
	const maxIter = 40
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			zr := (float64(x)/float64(w)*2 - 1) * 1.6
			zi := (float64(y)/float64(h)*2 - 1) * 1.6
			i := 0
			for ; i < maxIter; i++ {
				zr2, zi2 := zr*zr, zi*zi
				if zr2+zi2 > 4 {
					break
				}
				zr, zi = zr2-zi2+cr, 2*zr*zi+ci
			}
			if i == maxIter {
				f[y][x] = wcell{v: 1, hue: 0}
				continue
			}
			// escape times cluster low, so most of the picture lands on the
			// first glyph of the ramp. A gamma lift spreads them out.
			n := math.Pow(float64(i)/maxIter, 0.35)
			f[y][x] = wcell{v: n, hue: math.Mod(n*2+t*0.08, 1)}
		}
	}
	return f
}

// ---------------------------------------------------------------- rain

// rain is falling glyph columns — the one that reads as "terminal" rather
// than as graphics, and the cheapest of the set.
func rain(w, h int, t float64) [][]wcell {
	f := blank(w, h)
	for x := 0; x < w; x++ {
		// Each column needs an independent speed and phase. A linear
		// x*k mod 1 is NOT a hash — it is a ramp, so neighbouring columns
		// stay in step and the rain falls in visible diagonal stripes.
		// The sine-fract trick decorrelates them.
		seed := hash1(float64(x))
		speed := 3 + seed*7
		head := math.Mod(t*speed+seed*float64(h)*3, float64(h)+10)
		for y := 0; y < h; y++ {
			d := head - float64(y)
			if d < 0 || d > 9 {
				continue
			}
			v := 1 - d/9
			f[y][x] = wcell{v: v * v, hue: math.Mod(seed+t*0.1, 1)}
		}
	}
	return f
}

// hash1 is the standard sine-fract hash: deterministic, no state, and
// adjacent inputs land far apart.
func hash1(x float64) float64 {
	v := math.Sin(x*12.9898) * 43758.5453
	return v - math.Floor(v)
}

func blank(w, h int) [][]wcell {
	f := make([][]wcell, h)
	for i := range f {
		f[i] = make([]wcell, w)
	}
	return f
}

// The vendored effects return a rendered string rather than a field, so they
// cannot take part in colour cycling — their glyphs and structure are baked
// in together. They are wrapped here so the sidebar can treat both kinds
// alike: our own generators cycle, these get the theme's accent instead.
type stringWidget struct {
	Name string
	Fn   func(w, h, frame int) string
}

var stringWidgets = []stringWidget{
	{"matrix", effects.RenderMatrix},
	{"fire", effects.RenderFire},
	{"starfield", effects.RenderStarfield},
	{"snow", effects.RenderSnow},
	{"dna", effects.RenderDNA},
	{"wave", effects.RenderWave},
}

// renderStringWidget paints the effect's own glyphs in the theme's ramp,
// keyed on row so it reads as depth rather than as flat colour.
func renderStringWidget(sw stringWidget, w, h, frame int, bg string) []string {
	// Several effects emit their own SGR codes. Those bytes must be removed
	// before measuring, or the escape characters get counted as glyphs and
	// the row is truncated mid-sequence — which shreds both the layout and
	// the colour. We are re-colouring to the theme anyway, so stripping is
	// the right move rather than a workaround.
	raw := strings.Split(sgr.ReplaceAllString(sw.Fn(w, h, frame), ""), "\n")
	out := make([]string, 0, h)
	for y, line := range raw {
		if y >= h {
			break
		}
		r := []rune(line)
		var b strings.Builder
		used := 0
		for _, ch := range r {
			if used >= w {
				break
			}
			if ch == ' ' {
				b.WriteString(cCanvas.Render(" "))
			} else {
				frac := float64(y) / math.Max(1, float64(h-1))
				st := lipgloss.NewStyle().
					Foreground(lerp(gradFrom, gradTo, frac)).
					Background(lipgloss.Color(bg))
				b.WriteString(st.Render(string(ch)))
			}
			used += lipgloss.Width(string(ch))
		}
		for ; used < w; used++ {
			b.WriteString(cCanvas.Render(" "))
		}
		out = append(out, b.String())
	}
	for len(out) < h {
		out = append(out, cCanvas.Render(strings.Repeat(" ", w)))
	}
	return out
}

// totalWidgets is every animation the sidebar can pick from, ours and the
// vendored ones together.
func totalWidgets() int { return len(widgets) + len(stringWidgets) }

// sidebarFrame renders widget n at the given size, whichever kind it is.
func sidebarFrame(n, w, h, frame int, t float64, bg string) ([]string, string) {
	if n < len(widgets) {
		g := widgets[n]
		return renderWidget(g.Gen(w, h, t), t*0.06, bg), g.Name
	}
	sw := stringWidgets[(n-len(widgets))%len(stringWidgets)]
	return renderStringWidget(sw, w, h, frame, bg), sw.Name
}

var widgets = []widget{
	{"globe", globe},
	{"galaxy", galaxy},
	{"plasma", plasma},
	{"julia", julia},
	{"rain", rain},
}

// ---------------------------------------------------------------- render

// renderWidget turns a field into styled lines. cycle advances the palette
// without regenerating the field — colour cycling, in the Amiga sense.
func renderWidget(f [][]wcell, cycle float64, bg string) []string {
	out := make([]string, len(f))
	for y, row := range f {
		var b strings.Builder
		for _, c := range row {
			g := glyphFor(c.v)
			if g == ' ' {
				b.WriteString(cCanvas.Render(" "))
				continue
			}
			// hue rides the theme's own accent-to-second ramp, so a widget
			// always belongs to the current theme rather than fighting it
			h := math.Mod(c.hue+cycle, 1)
			col := lerp(gradFrom, gradTo, triangle(h))
			st := lipgloss.NewStyle().
				Foreground(col).
				Background(lipgloss.Color(bg))
			b.WriteString(st.Render(string(g)))
		}
		out[y] = b.String()
	}
	return out
}

// triangle folds 0..1 into 0..1..0 so a cycling hue runs up the ramp and
// back rather than snapping at the wrap point.
func triangle(x float64) float64 {
	x = math.Mod(x, 1)
	if x < 0 {
		x++
	}
	if x < 0.5 {
		return x * 2
	}
	return (1 - x) * 2
}
