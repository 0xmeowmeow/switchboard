// diagrams.go — concepts you can poke, instead of figures you can only look at.
//
// A lesson names a diagram in its frontmatter:
//
//	diagram: ring-heat
//
// and `d` opens it. Each one is a tiny interactive model: keys change a
// parameter and the picture responds, so the thing being taught is something
// you did rather than something you read. That is the whole argument for
// putting them here rather than leaving them in the PDF — on paper a figure
// shows one value of one parameter, and the interesting part of every one of
// these is what happens as the parameter moves.
package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type diagram struct {
	Name  string
	Title string
	Help  string
	// View renders at a size, given the diagram's own parameter vector.
	View func(w, h int, p []float64) []string
	// Params are the adjustable values: label, min, max, step, start.
	Params []param
}

type param struct {
	Label      string
	Min, Max   float64
	Step       float64
	Start      float64
	Fmt        func(float64) string
}

func fmtF(v float64) string  { return fmt.Sprintf("%.2f", v) }
func fmtI(v float64) string  { return fmt.Sprintf("%d", int(v)) }

// ---------------------------------------------------------------- ring heat

// ringHeat integrates u' = -Lu on a cycle graph. It is the chapter 2 figure,
// except you drive the time yourself — which is the only way to see that
// oversmoothing is the heat equation succeeding rather than the network
// failing.
func ringHeat(w, h int, p []float64) []string {
	n := int(p[0])
	t := p[1]
	if n < 4 {
		n = 4
	}

	// u(t) = e^{-Lt} u0, computed in the Laplacian eigenbasis, which on a
	// ring is the DFT basis: eigenvalue 2 - 2cos(2*pi*k/n).
	u := make([]float64, n)
	for j := 0; j < n; j++ {
		s := 0.0
		for k := 0; k < n; k++ {
			lam := 2 - 2*math.Cos(2*math.Pi*float64(k)/float64(n))
			s += math.Exp(-lam*t) * math.Cos(2*math.Pi*float64(k*j)/float64(n))
		}
		u[j] = s / float64(n)
	}
	maxU, sum := 0.0, 0.0
	for _, v := range u {
		if v > maxU {
			maxU = v
		}
		sum += v
	}

	var out []string
	out = append(out, cDim.Render(fmt.Sprintf(
		"impulse at node 0, diffusing.  peak %.4f   equilibrium %.4f   total %.4f (conserved)",
		maxU, 1/float64(n), sum)))
	out = append(out, "")

	// the ring, drawn as a circle of nodes shaded by heat
	cx, cy := float64(w)/2, float64(h-6)/2
	r := math.Min(float64(w)/2.4, float64(h-6)/2) - 1
	grid := make([][]rune, h-6)
	col := make([][]float64, h-6)
	for i := range grid {
		grid[i] = []rune(strings.Repeat(" ", w))
		col[i] = make([]float64, w)
	}
	for j := 0; j < n; j++ {
		a := 2*math.Pi*float64(j)/float64(n) - math.Pi/2
		x := int(cx + r*math.Cos(a)*1.9)
		y := int(cy + r*math.Sin(a))
		if y < 0 || y >= len(grid) || x < 0 || x >= w {
			continue
		}
		v := u[j] / math.Max(maxU, 1e-9)
		grid[y][x] = glyphFor(0.15 + 0.85*v)
		col[y][x] = v
	}
	for y := range grid {
		var b strings.Builder
		for x := range grid[y] {
			ch := grid[y][x]
			if ch == ' ' {
				b.WriteString(cCanvas.Render(" "))
				continue
			}
			st := lipgloss.NewStyle().
				Foreground(lerp(gradTo, gradFrom, col[y][x])).
				Background(lipgloss.Color(themes[curTheme].Bg)).Bold(true)
			b.WriteString(st.Render(string(ch)))
		}
		out = append(out, b.String())
	}

	out = append(out, "")
	if t > 6 {
		out = append(out, cWarn.Render(
			"every node now holds the same value. that is ker L, spanned by the "))
		out = append(out, cWarn.Render(
			"constant vector — and it is exactly what deep GNNs do to features."))
	}
	return out
}

// ---------------------------------------------------------------- SCC trap

// sccTrap draws a small digraph with a one-way door and lets you walk it. The
// point lands the moment you cross and cannot get back: the strongly
// connected component boundary IS the trap structure, and it is computable.
func sccTrap(w, h int, p []float64) []string {
	at := int(p[0]) % 5
	names := []string{"door", "table", "hall", "cellar", "well"}
	// edges: 0<->1<->2->0 form one SCC; 2=>3 is one-way; 3<->4 the second SCC
	inA := at <= 2

	var out []string
	art := []string{
		"      door ────► table ────► hall",
		"        ▲                     │",
		"        └─────────────────────┘",
		"                              ║  one-way",
		"                              ▼",
		"                    cellar ◄────► well",
	}
	for _, l := range art {
		styled := l
		for i, nm := range names {
			if i == at {
				styled = strings.Replace(styled, nm, strings.ToUpper(nm), 1)
			}
		}
		if strings.Contains(l, "║") || strings.Contains(l, "one-way") {
			out = append(out, cWarn.Render(styled))
		} else {
			out = append(out, cBase.Render(styled))
		}
	}
	out = append(out, "")
	out = append(out, cCool.Bold(true).Render("you are at: "+strings.ToUpper(names[at])))
	if inA {
		out = append(out, cDim.Render(
			"SCC #1 {door, table, hall} — every node reaches every other. you can go back."))
	} else {
		out = append(out, cWarn.Render(
			"SCC #2 {cellar, well} — you crossed the one-way edge. no path returns."))
		out = append(out, cDim.Render(
			"nothing is broken. the graph says this, and Tarjan's algorithm finds it in O(V+E)."))
	}
	return out
}

// ---------------------------------------------------------------- lattice

// tokenLattice shows segmentation as a path through a DAG: the same string,
// two different tokenisations, two different costs. This is the figure the
// autistic–allistic argument turns on.
func tokenLattice(w, h int, p []float64) []string {
	which := int(p[0]) % 3
	text := "thebrother"
	segs := [][]string{
		{"the", "brother"},
		{"th", "e", "bro", "ther"},
		{"t", "h", "e", "b", "r", "o", "t", "h", "e", "r"},
	}
	labels := []string{
		"vocabulary has both words: 2 tokens",
		"vocabulary is missing 'brother': 4 tokens, assembled",
		"character fallback: 10 tokens",
	}

	var out []string
	out = append(out, cDim.Render("positions 0.."+fmt.Sprint(len(text))+
		", an edge for every substring in the vocabulary"))
	out = append(out, "")
	out = append(out, cBase.Render("  "+strings.Join(strings.Split(text, ""), " ")))

	// draw the chosen path as brackets under the string
	var arcs strings.Builder
	arcs.WriteString("  ")
	for _, tok := range segs[which] {
		n := len(tok)*2 - 1
		if n < 1 {
			n = 1
		}
		arcs.WriteString(cCool.Render("└" + strings.Repeat("─", maxi(0, n-2)) + "┘"))
		arcs.WriteString(" ")
	}
	out = append(out, arcs.String())
	out = append(out, "")
	out = append(out, cCool.Bold(true).Render(labels[which]))
	out = append(out, "")
	out = append(out, cDim.Render(
		"same signal, same graph. a different vocabulary is a different shortest"))
	out = append(out, cDim.Render(
		"path, and the cost of communicating is the divergence between codebooks."))
	return out
}

// ---------------------------------------------------------------- registry

var diagrams = map[string]diagram{
	"ring-heat": {
		Name: "ring-heat", Title: "heat diffusion on a ring",
		Help: "hl time   jk nodes",
		View: ringHeat,
		Params: []param{
			{"nodes", 6, 32, 1, 16, fmtI},
			{"time t", 0, 12, 0.25, 0, fmtF},
		},
	},
	"scc-trap": {
		Name: "scc-trap", Title: "one-way doors are SCC boundaries",
		Help: "hl walk the graph",
		View: sccTrap,
		Params: []param{
			{"at node", 0, 4, 1, 0, fmtI},
		},
	},
	"token-lattice": {
		Name: "token-lattice", Title: "tokenisation is a path through a DAG",
		Help: "hl change vocabulary",
		View: tokenLattice,
		Params: []param{
			{"vocabulary", 0, 2, 1, 0, fmtI},
		},
	},
}

// ---------------------------------------------------------------- view

type diagramState struct {
	d      diagram
	values []float64
	sel    int
}

func newDiagramState(name string) *diagramState {
	d, ok := diagrams[name]
	if !ok {
		return nil
	}
	s := &diagramState{d: d, values: make([]float64, len(d.Params))}
	for i, p := range d.Params {
		s.values[i] = p.Start
	}
	return s
}

func (s *diagramState) adjust(delta int) {
	if len(s.d.Params) == 0 {
		return
	}
	p := s.d.Params[s.sel]
	v := s.values[s.sel] + float64(delta)*p.Step
	if v < p.Min {
		v = p.Min
	}
	if v > p.Max {
		v = p.Max
	}
	s.values[s.sel] = v
}

func (m model) viewDiagram() string {
	s := m.diagram
	if s == nil {
		return ""
	}
	w := mini(84, maxi(50, m.contentWidth()-8))
	h := maxi(14, mini(24, m.h-10))

	body := s.d.View(w-4, h, s.values)

	var ctl strings.Builder
	for i, p := range s.d.Params {
		v := p.Fmt(s.values[i])
		line := fmt.Sprintf("%s %s", pad(p.Label, 12), v)
		if i == s.sel {
			ctl.WriteString(cCool.Bold(true).Render("▸ "+line) + "   ")
		} else {
			ctl.WriteString(cDim.Render("  "+line) + "   ")
		}
	}

	inner := strings.Join(clip(body, h, w-4), "\n") + "\n\n" + ctl.String()
	return modalBox(strings.ToUpper(s.d.Title), inner,
		s.d.Help+"   jk pick a control   esc close", w)
}
