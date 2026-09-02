package main

import (
	"testing"

	"switchboard/tdf"
)

// mkTestFont builds a minimal single-glyph font ('A') of the given pixel
// height, for footprint probes that need controllable glyph heights without
// real .tdf files on disk.
func mkTestFont(name string, height int) *tdf.Font {
	f := &tdf.Font{Name: name, Type: tdf.TypeBlock, Spacing: 1}
	rows := make([][]tdf.Cell, height)
	for i := range rows {
		rows[i] = []tdf.Cell{{Ch: 'A'}}
	}
	f.Glyphs[int('A')-tdf.FirstChar] = &tdf.Glyph{Width: 1, Height: uint8(height), Rows: rows}
	return f
}

// TestFontsNoFrameJump found the same bug class as bluetooth/network: the
// preview pane's height was however tall the current font's glyphs are, with
// no fixed budget, so switching between a short font and a tall one grew or
// shrank viewFonts() and jumped the frame. Fixed with the same clampLines +
// Height treatment as everywhere else.
func TestFontsNoFrameJump(t *testing.T) {
	m := initialModel()
	m.w, m.h = 160, 40
	m.mode = modeFonts

	short := mkTestFont("short", 2)
	tall := mkTestFont("tall", 30)
	s := &fontState{
		all: []fontEntry{
			{file: "short.tdf", font: short, w: 1, h: 2},
			{file: "tall.tdf", font: tall, w: 1, h: 30},
		},
		groups: [][]int{{0}, {1}},
		shown:  []int{0, 1},
		text:   "A",
	}
	m.fonts = s

	var shapes []string
	var first shapeKey
	for i, name := range []string{"short", "tall"} {
		s.sel = i
		n, cw := footprint(m.baseView())
		k := shapeKey{n, cw}
		shapes = append(shapes, name)
		if i == 0 {
			first = k
			continue
		}
		if k != first {
			t.Errorf("selecting the %q font (glyph height differs from %q) shifted footprint from %v to %v",
				name, shapes[0], first, k)
		}
	}
}

type shapeKey struct{ n, cw int }
