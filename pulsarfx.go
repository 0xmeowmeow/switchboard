// pulsarfx.go — the pulsar engine: a stateful sub-cell field, a feedback /
// warp buffer, a stack of drawing modules, and a blocks renderer.
//
// The model is lifted straight from widgets.go: a field of wcell{v, hue} that a
// renderer colours through the theme's accent→second ramp (colour cycling in
// the Amiga sense). The difference is that this field persists between frames,
// so a module can smear, echo and feed back into itself — which is what the
// Winamp-AVS / MilkDrop lineage was built on.
package main

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

// ---------------------------------------------------------------- preset data

type plsLayer struct {
	Type   string             `json:"type"`
	Params map[string]float64 `json:"params,omitempty"`
	Expr   map[string]string  `json:"expr,omitempty"`
}

type plsPreset struct {
	Name            string     `json:"name"`
	Palette         string     `json:"palette"`
	FeedbackDecay   float64    `json:"feedbackDecay"`
	BeatSensitivity float64    `json:"beatSensitivity"`
	FPS             int        `json:"fps"`
	Contrast        float64    `json:"contrast"`
	Renderer        string     `json:"renderer"`
	Frame           string     `json:"frame,omitempty"`
	Beat            string     `json:"beat,omitempty"`
	Layers          []plsLayer `json:"layers"`
}

func (p plsPreset) fps() int {
	if p.FPS < 8 {
		return 8
	}
	if p.FPS > 60 {
		return 60
	}
	return p.FPS
}

// param reads a layer parameter with a fallback, so a hand-edited or mutated
// preset that is missing a key still renders.
func (l plsLayer) param(key string, def float64) float64 {
	if v, ok := l.Params[key]; ok {
		return v
	}
	return def
}

// ---------------------------------------------------------------- engine

type plsAudio struct {
	bars                     []float64 // 0..1, low → high frequency
	bass, mid, treb, energy  float64
	bassAtt, midAtt, trebAtt float64 // slow-following versions, for swells
	beat                     bool
	beatHold                 float64
	avg                      float64   // rolling energy average, for beat detect
	pcm                      []float32 // recent samples, newest at the end
}

func (a *plsAudio) band(which float64) float64 {
	switch int(which + 0.5) {
	case 1:
		return a.bass
	case 2:
		return a.mid
	case 3:
		return a.treb
	default:
		return a.energy
	}
}

type plsLayerC struct {
	L     plsLayer
	exprs map[string]exprNode
	hist  [][]float64 // wave: PCM rows; spectro: spectrum rows (ring, newest last)
	peak  []float64   // bars: peak-hold state
	tAcc  float64     // spectro: scroll-rate accumulator
	errS  string      // last compile error for this layer, shown in the editor
}

type plsEngine struct {
	w, h, fw, fh int
	field, prev  [][]wcell
	env          *exprEnv
	t            float64
	frozen       bool
	cycle        float64
	rng          *rand.Rand

	preset     plsPreset
	frameP     *program
	beatP      *program
	layers     []*plsLayerC
	compileErr string

	audio       plsAudio
	strobeFlash float64
}

func newPlsEngine(w, h int) *plsEngine {
	e := &plsEngine{env: newExprEnv(), rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
	e.resize(w, h)
	return e
}

func (e *plsEngine) resize(w, h int) {
	if w < 4 {
		w = 4
	}
	if h < 4 {
		h = 4
	}
	if w == e.w && h == e.h && e.field != nil {
		return
	}
	e.w, e.h = w, h
	e.fw, e.fh = w*2, h*4
	e.field = blank(e.fw, e.fh)
	e.prev = blank(e.fw, e.fh)
}

// setPreset compiles a preset's formulas and resets per-layer state. A bad
// formula is recorded on the layer (and surfaced in the editor) but never stops
// the rest of the stack from running.
func (e *plsEngine) setPreset(p plsPreset) {
	e.preset = p
	e.frameP, _ = compileProgram(p.Frame)
	e.beatP, _ = compileProgram(p.Beat)
	e.layers = e.layers[:0]
	e.compileErr = ""
	for _, l := range p.Layers {
		lc := &plsLayerC{L: l, exprs: map[string]exprNode{}}
		for k, src := range l.Expr {
			node, err := compileExpr(src)
			if err != nil {
				lc.errS = k + ": " + err.Error()
				continue
			}
			lc.exprs[k] = node
		}
		e.layers = append(e.layers, lc)
	}
	// persistent variables start clean on a preset change
	e.env = newExprEnv()
}

// ingestCava folds one raw cava frame (bar magnitudes, 0..1) into the audio
// context: three log-ish bands, their slow followers, and a beat flag derived
// from energy against its own rolling average.
func (e *plsEngine) ingestCava(bars []float64) {
	if len(bars) == 0 {
		return
	}
	e.audio.bars = bars
	n := len(bars)
	seg := func(lo, hi float64) float64 {
		a, b := int(lo*float64(n)), int(hi*float64(n))
		if b <= a {
			b = a + 1
		}
		s := 0.0
		for i := a; i < b && i < n; i++ {
			s += bars[i]
		}
		return s / float64(b-a)
	}
	e.audio.bass = seg(0, 0.14)
	e.audio.mid = seg(0.14, 0.5)
	e.audio.treb = seg(0.5, 1)
	sum := 0.0
	for _, v := range bars {
		sum += v
	}
	e.audio.energy = sum / float64(n)

	const k = 0.12
	e.audio.bassAtt += (e.audio.bass - e.audio.bassAtt) * k
	e.audio.midAtt += (e.audio.mid - e.audio.midAtt) * k
	e.audio.trebAtt += (e.audio.treb - e.audio.trebAtt) * k

	e.audio.avg += (e.audio.energy - e.audio.avg) * 0.02
	sens := e.preset.BeatSensitivity
	if sens < 1.05 {
		sens = 1.05
	}
	e.audio.beat = false
	if e.audio.beatHold > 0 {
		e.audio.beatHold -= 1
	} else if e.audio.energy > e.audio.avg*sens && e.audio.energy > 0.02 {
		e.audio.beat = true
		e.audio.beatHold = 4
	}
}

// ingestPCM keeps a rolling window of the most recent samples for the wave
// module; older audio falls off the front.
func (e *plsEngine) ingestPCM(s []float32) {
	const keep = 1 << 14 // ~0.75s at 22 kHz — plenty for a stack of rows
	e.audio.pcm = append(e.audio.pcm, s...)
	if len(e.audio.pcm) > keep {
		e.audio.pcm = append(e.audio.pcm[:0], e.audio.pcm[len(e.audio.pcm)-keep:]...)
	}
}

// step advances the whole pipeline by dt seconds and leaves the result in
// e.field, ready for a renderer.
func (e *plsEngine) step(dt float64) {
	if !e.frozen {
		e.t += dt
	}
	e.cycle += dt * 0.05

	decay := clampf(e.preset.FeedbackDecay, 0, 0.999)
	warp := e.activeWarp()

	// previous frame → working buffer. Three paths, cheapest first: no feedback
	// at all (just clear), plain decay, or a full warp resample.
	src, dst := e.field, e.prev
	switch {
	case decay <= 0 && warp == nil:
		for y := 0; y < e.fh; y++ {
			dr := dst[y]
			for x := range dr {
				dr[x] = wcell{}
			}
		}
	case warp == nil:
		for y := 0; y < e.fh; y++ {
			sr, dr := src[y], dst[y]
			for x := 0; x < e.fw; x++ {
				c := sr[x]
				c.v *= decay
				dr[x] = c
			}
		}
	default:
		for y := 0; y < e.fh; y++ {
			ny := (float64(y) + 0.5) / float64(e.fh)
			for x := 0; x < e.fw; x++ {
				nx := (float64(x) + 0.5) / float64(e.fw)
				sx, sy := warp.sample(nx, ny, e)
				c := sampleField(src, sx*float64(e.fw)-0.5, sy*float64(e.fh)-0.5)
				c.v *= decay
				dst[y][x] = c
			}
		}
	}
	e.field, e.prev = dst, src

	// variable blocks
	e.loadEnv()
	if e.frameP != nil {
		e.frameP.run(e.env)
	}
	if e.audio.beat && e.beatP != nil {
		e.beatP.run(e.env)
	}

	e.strobeFlash *= 0.8
	for _, lc := range e.layers {
		e.applyLayer(lc)
	}

	// contrast / flash pass — skipped entirely when there is nothing to do
	// (the renderer clamps intensity itself, so a plain preset needs neither).
	g := e.preset.Contrast
	if g <= 0 {
		g = 1
	}
	if g != 1 || e.strobeFlash > 1e-4 {
		for y := 0; y < e.fh; y++ {
			row := e.field[y]
			for x := 0; x < e.fw; x++ {
				v := row[x].v + e.strobeFlash
				if v < 0 {
					v = 0
				}
				if g != 1 {
					v = math.Pow(math.Min(v, 1), g)
				}
				row[x].v = v
			}
		}
	}
}

func (e *plsEngine) loadEnv() {
	v := e.env.vars
	v["t"] = e.t
	v["bass"], v["mid"], v["treb"] = e.audio.bass, e.audio.mid, e.audio.treb
	v["bass_att"], v["mid_att"], v["treb_att"] = e.audio.bassAtt, e.audio.midAtt, e.audio.trebAtt
	v["energy"] = e.audio.energy
	v["beat"] = b2f(e.audio.beat)
	v["pi"] = math.Pi
	v["tau"] = 2 * math.Pi
}

func (e *plsEngine) activeWarp() *plsLayerC {
	for _, lc := range e.layers {
		if lc.L.Type == "warp" {
			return lc
		}
	}
	return nil
}

// ---------------------------------------------------------------- modules

func (e *plsEngine) applyLayer(lc *plsLayerC) {
	switch lc.L.Type {
	case "scope":
		e.layerScope(lc)
	case "wave":
		e.layerWave(lc)
	case "spectro":
		e.layerSpectro(lc)
	case "bars":
		e.layerBars(lc)
	case "plasma":
		e.layerPlasma(lc)
	case "strobe":
		e.layerStrobe(lc)
	case "warp":
		// handled in step(); nothing to draw
	}
}

// layerScope walks a parametric curve defined by expressions and splats it into
// the field. Polar (r, th) by default; cartesian (x, y) if either is present.
func (e *plsEngine) layerScope(lc *plsLayerC) {
	n := int(lc.L.param("points", 220))
	if n < 2 {
		n = 2
	}
	if n > 4000 {
		n = 4000
	}
	gain := clampf(lc.L.param("gain", 1), -8, 8)
	thick := clampf(lc.L.param("thick", 1), 1, 6)
	hue := lc.L.param("hue", 0.5)
	aBand := e.audio.band(lc.L.param("audio", 0))
	cart := lc.exprs["x"] != nil || lc.exprs["y"] != nil

	env := e.env
	env.vars["n"] = float64(n)
	env.vars["a"] = aBand

	var px, py float64
	for i := 0; i <= n; i++ {
		f := float64(i) / float64(n)
		env.vars["i"] = f
		var nx, ny float64
		if cart {
			nx = 0.5 + 0.5*evalOr(lc.exprs["x"], env, 0)*gain
			ny = 0.5 - 0.5*evalOr(lc.exprs["y"], env, 0)*gain
		} else {
			r := evalOr(lc.exprs["r"], env, 0.5) * gain
			th := evalOr(lc.exprs["th"], env, f*2*math.Pi)
			nx = 0.5 + 0.5*r*math.Cos(th)
			ny = 0.5 + 0.5*r*math.Sin(th)
		}
		cx, cy := nx*float64(e.fw), ny*float64(e.fh)
		if i > 0 {
			e.splatLine(px, py, cx, cy, thick, 0.9, hue)
		}
		px, py = cx, cy
	}
}

// layerWave is the Unknown Pleasures ridgeline: a ring of real PCM windows,
// drawn back-to-front so nearer rows occlude the ones behind them.
func (e *plsEngine) layerWave(lc *plsLayerC) {
	rows := int(lc.L.param("rows", 30))
	if rows < 2 {
		rows = 2
	}
	if rows > e.h {
		rows = e.h
	}
	gap := clampf(lc.L.param("gap", 1), 0.1, 4)
	amp := clampf(lc.L.param("amp", 1), 0, 4)
	occlude := lc.L.param("occlude", 1) != 0
	squash := lc.L.param("squash", 0.6)
	xzoom := lc.L.param("xzoom", 1)
	hue := lc.L.param("hue", 0.5)

	// snapshot the newest window into a row of length fw
	row := make([]float64, e.fw)
	pcm := e.audio.pcm
	span := int(float64(e.fw) / clampf(xzoom, 0.2, 4))
	if span < 8 {
		span = 8
	}
	for x := 0; x < e.fw; x++ {
		var s float64
		if len(pcm) >= span {
			idx := len(pcm) - span + x*span/e.fw
			if idx >= 0 && idx < len(pcm) {
				s = float64(pcm[idx])
			}
		}
		row[x] = s
	}
	lc.hist = append(lc.hist, row)
	if len(lc.hist) > rows {
		lc.hist = append(lc.hist[:0], lc.hist[len(lc.hist)-rows:]...)
	}

	rowStep := float64(e.fh) / float64(rows+2)
	ceil := make([]int, e.fw)
	for x := range ceil {
		ceil[x] = e.fh
	}
	// oldest first (back of the stack, higher up), newest last (front, lower)
	for k := 0; k < len(lc.hist); k++ {
		hr := lc.hist[len(lc.hist)-1-k] // k=0 → newest
		baseY := float64(e.fh) - 1 - float64(k)*rowStep*gap
		lift := rowStep * 3 * amp
		var lastX, lastY float64
		for x := 0; x < e.fw; x++ {
			y := baseY - hr[x]*lift
			yy := int(y + 0.5)
			if occlude && yy >= ceil[x] {
				lastX, lastY = float64(x), y
				continue
			}
			if occlude {
				// paint the sliver between this trace and the one in front,
				// so a near ridge reads as solid and hides what is behind it
				start := yy
				if start < 0 {
					start = 0
				}
				for fill := start; fill < ceil[x] && fill < e.fh; fill++ {
					shade := squash * (1 - float64(fill-yy)/float64(e.fh))
					setPix(e.field, x, fill, shade, hue)
				}
				ceil[x] = yy
			}
			if x > 0 {
				e.splatLine(lastX, lastY, float64(x), y, 1, 1, hue)
			}
			lastX, lastY = float64(x), y
		}
	}
}

// layerSpectro is the modem-handshake waterfall: a scrolling stack of spectra.
func (e *plsEngine) layerSpectro(lc *plsLayerC) {
	bars := e.audio.bars
	if len(bars) == 0 {
		return
	}
	logf := lc.L.param("logf", 1) != 0
	gain := lc.L.param("gain", 1)
	rate := lc.L.param("rate", 1)
	dir := lc.L.param("dir", 1) // 1 = scroll up, 0 = scroll down
	hue := lc.L.param("hue", 0.5)

	e.lcAdvance(lc, rate)
	if lc.tAcc < 1 {
		// still redraw the existing history so it does not flicker out
	} else {
		lc.tAcc -= math.Floor(lc.tAcc)
		spec := make([]float64, e.fw)
		for x := 0; x < e.fw; x++ {
			fx := float64(x) / float64(e.fw)
			if logf {
				fx = (math.Pow(1000, fx) - 1) / 999
			}
			bi := int(fx * float64(len(bars)-1))
			spec[x] = clampf(bars[bi]*gain, 0, 1)
		}
		lc.hist = append(lc.hist, spec)
		if len(lc.hist) > e.fh {
			lc.hist = append(lc.hist[:0], lc.hist[len(lc.hist)-e.fh:]...)
		}
	}
	for row := 0; row < len(lc.hist); row++ {
		spec := lc.hist[len(lc.hist)-1-row]
		y := row
		if dir != 0 {
			y = e.fh - 1 - row
		}
		if y < 0 || y >= e.fh {
			continue
		}
		for x := 0; x < e.fw && x < len(spec); x++ {
			setPix(e.field, x, y, spec[x], hue+spec[x]*0.3)
		}
	}
}

func (e *plsEngine) lcAdvance(lc *plsLayerC, rate float64) {
	if rate <= 0 {
		rate = 1
	}
	lc.tAcc += rate
}

// layerBars is a plain spectrum: linear, mirrored or radial.
func (e *plsEngine) layerBars(lc *plsLayerC) {
	bars := e.audio.bars
	if len(bars) == 0 {
		return
	}
	count := int(lc.L.param("count", 48))
	if count < 4 {
		count = 4
	}
	if count > e.fw {
		count = e.fw
	}
	gain := lc.L.param("gain", 1)
	layout := lc.L.param("layout", 0) // 0 linear, 1 mirror, 2 radial
	hue := lc.L.param("hue", 0.5)
	if len(lc.peak) != count {
		lc.peak = make([]float64, count)
	}
	bw := float64(e.fw) / float64(count)
	for b := 0; b < count; b++ {
		bi := b * (len(bars) - 1) / (count - 1)
		v := clampf(bars[bi]*gain, 0, 1)
		if v > lc.peak[b] {
			lc.peak[b] = v
		} else {
			lc.peak[b] *= 0.94
		}
		h := v * float64(e.fh)
		switch int(layout + 0.5) {
		case 2: // radial
			ang := float64(b) / float64(count) * 2 * math.Pi
			for t := 0.0; t < v; t += 1.0 / float64(e.fh) {
				rr := 0.12 + t*0.7
				x := 0.5 + 0.5*rr*math.Cos(ang)
				y := 0.5 + 0.5*rr*math.Sin(ang)
				setPix(e.field, int(x*float64(e.fw)), int(y*float64(e.fh)), 1-t, hue)
			}
		case 1: // mirror from centre
			for yy := 0; yy < int(h/2); yy++ {
				for xx := int(float64(b) * bw); xx < int(float64(b+1)*bw)-1; xx++ {
					setPix(e.field, xx, e.fh/2-yy, 1, hue)
					setPix(e.field, xx, e.fh/2+yy, 1, hue)
				}
			}
		default: // linear, rising from the bottom
			for yy := 0; yy < int(h); yy++ {
				for xx := int(float64(b) * bw); xx < int(float64(b+1)*bw)-1; xx++ {
					setPix(e.field, xx, e.fh-1-yy, 1, hue)
				}
			}
			py := e.fh - 1 - int(lc.peak[b]*float64(e.fh))
			for xx := int(float64(b) * bw); xx < int(float64(b+1)*bw)-1; xx++ {
				setPix(e.field, xx, py, 0.8, hue+0.3)
			}
		}
	}
}

// layerPlasma is the widgets.go plasma, pushed around by the bass.
func (e *plsEngine) layerPlasma(lc *plsLayerC) {
	scale := lc.L.param("scale", 1)
	speed := lc.L.param("speed", 1)
	bassPush := lc.L.param("bass", 1)
	swirl := lc.L.param("swirl", 0)
	mix := lc.L.param("mix", 0.6)
	hue := lc.L.param("hue", 0)
	tt := e.t * speed
	cx, cy := float64(e.fw)/2, float64(e.fh)/2
	for y := 0; y < e.fh; y++ {
		for x := 0; x < e.fw; x++ {
			fx := float64(x) / (12 * scale)
			fy := float64(y) / (12 * scale)
			if swirl != 0 {
				dx, dy := float64(x)-cx, float64(y)-cy
				ang := math.Atan2(dy, dx) + swirl*0.3
				rad := math.Hypot(dx, dy) / (12 * scale)
				fx, fy = rad*math.Cos(ang), rad*math.Sin(ang)
			}
			v := math.Sin(fx+tt) + math.Sin(fy+tt*0.7) +
				math.Sin((fx+fy+tt)/2) +
				math.Sin(math.Hypot(fx-4, fy-2)/(1+2*bassPush*e.audio.bassAtt)+tt)
			nrm := (v + 4) / 8
			c := &e.field[y][x]
			c.v += mix * (0.25 + 0.75*nrm)
			c.hue = math.Mod(nrm+hue, 1)
		}
	}
}

// layerStrobe adds a whole-field flash on the beat and kicks the colour cycle.
func (e *plsEngine) layerStrobe(lc *plsLayerC) {
	if !e.audio.beat {
		return
	}
	e.strobeFlash = math.Max(e.strobeFlash, lc.L.param("beatFlash", 0.4))
	e.cycle += lc.L.param("hueKick", 0.05)
}

// ---------------------------------------------------------------- warp sampling

// sample maps a destination point back to the source point it should pull from
// last frame — a zoom/rotate/scroll/ripple transform around the centre.
func (lc *plsLayerC) sample(nx, ny float64, e *plsEngine) (float64, float64) {
	zoom := lc.L.param("zoom", 1)
	rot := lc.L.param("rot", 0)
	dx := lc.L.param("dx", 0)
	dy := lc.L.param("dy", 0)
	ripple := lc.L.param("ripple", 0)
	rfreq := lc.L.param("rfreq", 8)

	x, y := nx-0.5, ny-0.5
	if ripple != 0 {
		d := math.Hypot(x, y)
		amp := ripple * 0.01 * math.Sin(d*rfreq-e.t*3)
		x += x * amp
		y += y * amp
	}
	if zoom != 1 {
		x /= zoom
		y /= zoom
	}
	if rot != 0 {
		s, c := math.Sincos(rot)
		x, y = x*c-y*s, x*s+y*c
	}
	return x + 0.5 - dx*0.01, y + 0.5 - dy*0.01
}

func sampleField(f [][]wcell, fx, fy float64) wcell {
	h := len(f)
	if h == 0 {
		return wcell{}
	}
	w := len(f[0])
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	if x0 < 0 || y0 < 0 || x0 >= w-1 || y0 >= h-1 {
		if x0 < 0 || y0 < 0 || x0 >= w || y0 >= h {
			return wcell{}
		}
		return f[y0][x0]
	}
	tx, ty := fx-float64(x0), fy-float64(y0)
	c00, c10 := f[y0][x0], f[y0][x0+1]
	c01, c11 := f[y0+1][x0], f[y0+1][x0+1]
	v := (c00.v*(1-tx)+c10.v*tx)*(1-ty) + (c01.v*(1-tx)+c11.v*tx)*ty
	return wcell{v: v, hue: c00.hue}
}

// ---------------------------------------------------------------- splat helpers

func setPix(f [][]wcell, x, y int, v, hue float64) {
	if y < 0 || x < 0 || y >= len(f) || x >= len(f[0]) {
		return
	}
	c := &f[y][x]
	if v > c.v {
		c.v = v
	}
	c.hue = math.Mod(hue, 1)
}

func (e *plsEngine) splatLine(x0, y0, x1, y1, thick, amt, hue float64) {
	// a runaway formula can hand us endpoints millions of cells apart; cap the
	// work at roughly one pass of the field rather than freezing the frame.
	dist := math.Hypot(x1-x0, y1-y0)
	if math.IsNaN(dist) || dist > float64(4*(e.fw+e.fh)) {
		return
	}
	steps := int(dist) + 1
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		x := x0 + (x1-x0)*t
		y := y0 + (y1-y0)*t
		e.splat(x, y, thick, amt, hue)
	}
}

func (e *plsEngine) splat(x, y, thick, amt, hue float64) {
	r := int(thick)
	if r < 1 {
		r = 1
	}
	ix, iy := int(x+0.5), int(y+0.5)
	for dy := -r + 1; dy < r; dy++ {
		for dx := -r + 1; dx < r; dx++ {
			fall := 1 - math.Hypot(float64(dx), float64(dy))/float64(r)
			if fall <= 0 {
				continue
			}
			setPix(e.field, ix+dx, iy+dy, amt*fall, hue)
		}
	}
}

func evalOr(n exprNode, env *exprEnv, def float64) float64 {
	if n == nil {
		return def
	}
	v := n.eval(env)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return def
	}
	return v
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------- blocks renderer

// palettes are (from, to) RGB pairs the intensity ramp runs across. "theme"
// (the zero value) defers to the app's own accent→second gradient.
var plsPalettes = map[string][2][3]int{
	"phosphor": {{2, 12, 6}, {120, 255, 150}},
	"amber":    {{20, 8, 0}, {255, 190, 60}},
	"ice":      {{4, 10, 24}, {150, 220, 255}},
	"fire":     {{18, 2, 0}, {255, 210, 90}},
	"mono":     {{10, 10, 12}, {235, 235, 245}},
}

// braille dot bits, indexed [dx*4 + dy] for the 2×4 cell:
//
//	(0,0)=0x01  (1,0)=0x08
//	(0,1)=0x02  (1,1)=0x10
//	(0,2)=0x04  (1,2)=0x20
//	(0,3)=0x40  (1,3)=0x80
var brailleBit = [8]byte{0x01, 0x02, 0x04, 0x40, 0x08, 0x10, 0x20, 0x80}

// renderBlocks turns the field into h lines of w cells, each a braille glyph
// coloured by the mean hue/intensity of its 2×4 sub-cells. It writes raw
// truecolor SGR rather than going through lipgloss per cell — at a full-screen
// 24 fps that difference matters.
func (e *plsEngine) renderBlocks() string {
	from, to, useTheme := e.paletteEnds()
	var b strings.Builder
	b.Grow(e.w * e.h * 20)
	bgR, bgG, bgB := hexRGB(themes[curTheme].Bg)

	for cy := 0; cy < e.h; cy++ {
		fmt.Fprintf(&b, "\x1b[48;2;%d;%d;%dm", bgR, bgG, bgB)
		for cx := 0; cx < e.w; cx++ {
			var bits byte
			var sumV, sumHue float64
			var lit int
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					c := e.field[cy*4+dy][cx*2+dx]
					if c.v > 0.14 {
						bits |= brailleBit[dx*4+dy]
						sumV += c.v
						sumHue += c.hue
						lit++
					}
				}
			}
			if lit == 0 {
				b.WriteByte(' ')
				continue
			}
			mv := sumV / float64(lit)
			if mv > 1 {
				mv = 1
			}
			var r, g, bl int
			if useTheme {
				r, g, bl = lerpRGB(gradFrom, gradTo, triangle(sumHue/float64(lit)+e.cycle))
				r, g, bl = mixRGB(bgR, bgG, bgB, r, g, bl, 0.25+0.75*mv)
			} else {
				r, g, bl = lerpRGB(from, to, clampf(mv, 0, 1))
			}
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm%c", clip8(r), clip8(g), clip8(bl), rune(0x2800+int(bits)))
		}
		b.WriteString("\x1b[0m")
		if cy < e.h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (e *plsEngine) paletteEnds() (from, to [3]int, useTheme bool) {
	p, ok := plsPalettes[e.preset.Palette]
	if !ok || e.preset.Palette == "" || e.preset.Palette == "theme" {
		return [3]int{}, [3]int{}, true
	}
	return p[0], p[1], false
}

func mixRGB(r0, g0, b0, r1, g1, b1 int, t float64) (int, int, int) {
	return int(float64(r0) + (float64(r1)-float64(r0))*t),
		int(float64(g0) + (float64(g1)-float64(g0))*t),
		int(float64(b0) + (float64(b1)-float64(b0))*t)
}

func lerpRGB(a, b [3]int, t float64) (int, int, int) {
	return a[0] + int(float64(b[0]-a[0])*t),
		a[1] + int(float64(b[1]-a[1])*t),
		a[2] + int(float64(b[2]-a[2])*t)
}

func hexRGB(h string) (int, int, int) {
	c := hex3(h)
	return c[0], c[1], c[2]
}

func clip8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
