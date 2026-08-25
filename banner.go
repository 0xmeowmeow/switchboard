// banner.go — the header, rendered in a real TheDraw font.
//
// Why this is not just SB_BANNER shelling out to tdfgo any more:
//
//  1. External output is raw text. The spaces inside and between letters
//     carry no background, so the canvas backdrop showed straight through
//     the holes in the glyphs. Rendering here means every cell — including
//     every gap — is painted, and the banner sits on a solid block.
//  2. Shelling out costs a process at startup. This costs one file read.
//  3. Rendered internally, the banner can wear the theme: the gradient runs
//     across it and changes with `t`, instead of being baked in.
//
// Font choice lives in ~/.config/switchboard/banner.conf, one line:
//
//	font = BoldMedium
//	text = switchboard
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"switchboard/tdf"
)

type bannerConf struct {
	font string
	text string
}

func loadBannerConf() bannerConf {
	c := bannerConf{font: "BoldMedium", text: "switchboard"}
	b, err := os.ReadFile(filepath.Join(confDir(), "banner.conf"))
	if err != nil {
		return c
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "font":
			c.font = strings.TrimSpace(v)
		case "text":
			c.text = strings.TrimSpace(v)
		}
	}
	return c
}

// findFont scans the search paths for a font whose name matches, without
// parsing everything: it stops at the first hit. A font's identity is
// file + index, so a pack holding sixteen fonts is handled correctly.
func findFont(name string) *tdf.Font {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return nil
	}
	for _, dir := range fontSearchPaths() {
		var files []string
		for _, pat := range []string{"*.tdf", "*.TDF", "*.Tdf"} {
			g, _ := filepath.Glob(filepath.Join(dir, pat))
			files = append(files, g...)
		}
		for _, p := range files {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			fonts, err := tdf.Parse(b)
			if err != nil {
				continue
			}
			for _, f := range fonts {
				if strings.ToLower(strings.TrimSpace(f.Name)) == want {
					return f
				}
			}
		}
	}
	return nil
}

// bannerLines renders text and returns painted lines of uniform width.
//
// The gap problem, stated exactly: a TheDraw row ends rather than pads, and
// a glyph is only as wide as its widest row. Anything not covered — short
// rows, NUL cells, the spacing between glyphs, the ragged right edge — has
// to be filled with a styled space, or it is a hole the backdrop shows
// through. Every one of those cases is handled below.
func bannerLines(f *tdf.Font, text string) []string {
	if f == nil {
		return nil
	}
	type placed struct{ g *tdf.Glyph }
	var gs []placed
	height := 0
	for _, r := range text {
		if r == ' ' {
			gs = append(gs, placed{nil})
			continue
		}
		g := f.Glyph(r)
		if g == nil {
			// try the other case before giving up — most of the collection
			// is caps-only, and a lowercase title would render as nothing
			if u := f.Glyph(upper(r)); u != nil {
				g = u
			} else {
				continue
			}
		}
		gs = append(gs, placed{g})
		if int(g.Height) > height {
			height = int(g.Height)
		}
	}
	if height == 0 {
		return nil
	}

	spacing := int(f.Spacing)
	if spacing < 1 {
		spacing = 1
	}

	// build the plain character grid first, then colour it in one pass, so
	// the gradient can run across the true width
	grid := make([][]rune, height)
	total := 0
	for row := 0; row < height; row++ {
		var line []rune
		for _, p := range gs {
			if p.g == nil {
				line = append(line, ' ', ' ', ' ')
				continue
			}
			width := int(p.g.Width)
			var cells []tdf.Cell
			if row < len(p.g.Rows) {
				cells = p.g.Rows[row]
			}
			for col := 0; col < width; col++ {
				if col >= len(cells) || cells[col].Ch == 0 {
					line = append(line, ' ')
					continue
				}
				line = append(line, tdf.Rune(cells[col].Ch))
			}
			for i := 0; i < spacing; i++ {
				line = append(line, ' ')
			}
		}
		grid[row] = line
		if len(line) > total {
			total = len(line)
		}
	}

	// pad every row to the same width — the ragged right edge is a hole too
	out := make([]string, height)
	for row := 0; row < height; row++ {
		line := grid[row]
		for len(line) < total {
			line = append(line, ' ')
		}
		var b strings.Builder
		for i, ch := range line {
			frac := 0.0
			if total > 1 {
				frac = float64(i) / float64(total-1)
			}
			st := lipgloss.NewStyle().
				Background(lipgloss.Color(themes[curTheme].Bg))
			if ch != ' ' {
				st = st.Foreground(lerp(gradFrom, gradTo, frac)).Bold(true)
			}
			b.WriteString(st.Render(string(ch)))
		}
		out[row] = b.String()
	}
	return out
}

func upper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 32
	}
	return r
}
