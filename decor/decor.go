// decor.go — the part that makes sb look like something rather than a menu.
//
// Four things live here:
//
//   backdrop   sb paints its own background instead of showing through to
//              whatever the terminal is set to, and tiles a faint glyph
//              pattern behind the panes (the 猫 咪 trick from the lipgloss
//              demo). Both come from the theme.
//   art        ANSI or text files dropped in ~/.config/switchboard/decor/
//              are rendered in the corner. Anything from your .ans collection
//              works; nothing there means nothing is drawn.
//   rules      gradient horizontal rules and section headings.
//   swatch     the little colour grid, which doubles as a live check that a
//              theme's ramp is doing what you meant.
package decor

import (
	"os"
	"path/filepath"
	"strings"
)

// Nothing here imports the model, so it stays testable on its own.

// Tile repeats a glyph set into a w x h block. Glyphs may be multi-rune and
// may be wide characters; width is counted in cells by the caller's measure
// function, which keeps this file free of a lipgloss dependency.
func Tile(glyphs []string, w, h int, width func(string) int) []string {
	if len(glyphs) == 0 || w <= 0 || h <= 0 {
		return nil
	}
	rows := make([]string, 0, h)
	n := 0
	for y := 0; y < h; y++ {
		var b strings.Builder
		used := 0
		for used < w {
			g := glyphs[n%len(glyphs)]
			n++
			gw := width(g)
			if gw <= 0 {
				gw = 1
			}
			if used+gw > w {
				b.WriteString(strings.Repeat(" ", w-used))
				used = w
				break
			}
			b.WriteString(g)
			used += gw
		}
		rows = append(rows, b.String())
		n++ // shift each row so the pattern does not form vertical stripes
	}
	return rows
}

// ArtDir is where decorative files live. One per file; any of them may be
// picked. .ans keeps its own colours, .txt is drawn in the theme's dim tone.
func ArtDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "switchboard", "decor")
}

type Art struct {
	Name   string
	Lines  []string
	Colour bool // true for .ans — it carries its own escape codes
}

// LoadArt reads every decorative file, skipping anything too large to be
// decoration. Returns nil rather than an error when the directory is absent:
// decor is optional by design.
func LoadArt(maxW, maxH int) []Art {
	entries, err := os.ReadDir(ArtDir())
	if err != nil {
		return nil
	}
	var out []Art
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".ans" && ext != ".txt" && ext != ".asc" && ext != ".nfo" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ArtDir(), e.Name()))
		if err != nil {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		// trim trailing blank lines so the box hugs the art
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) == 0 || len(lines) > maxH {
			continue
		}
		too := false
		for _, l := range lines {
			if len(l) > maxW*4 { // generous: escapes inflate byte length
				too = true
				break
			}
		}
		if too {
			continue
		}
		out = append(out, Art{
			Name:   strings.TrimSuffix(e.Name(), ext),
			Lines:  lines,
			Colour: ext == ".ans",
		})
	}
	return out
}

// PickArt chooses deterministically from a seed, so the same session keeps
// the same decoration instead of flickering between frames.
func PickArt(all []Art, seed int) *Art {
	if len(all) == 0 {
		return nil
	}
	if seed < 0 {
		seed = -seed
	}
	a := all[seed%len(all)]
	return &a
}

// Overlay composites fg over a bg block, at the given offset, ignoring spaces
// in fg so the backdrop shows through the gaps. Both are slices of already
// rendered lines. Widths are measured by the caller's function.
func Overlay(bg, fg []string, x, y int, width func(string) int) []string {
	out := make([]string, len(bg))
	copy(out, bg)
	for i, line := range fg {
		row := y + i
		if row < 0 || row >= len(out) {
			continue
		}
		out[row] = spliceLine(out[row], line, x, width)
	}
	return out
}

// spliceLine puts src into dst starting at column x. It works on rendered
// strings containing escape sequences, so it cannot index by byte: it walks
// dst until it has consumed x visible cells, then emits src and skips the
// cells src covers.
func spliceLine(dst, src string, x int, width func(string) int) string {
	if x <= 0 && width(src) >= width(dst) {
		return src
	}
	head := takeCells(dst, x, width)
	srcW := width(src)
	tail := dropCells(dst, x+srcW, width)
	pad := x - width(head)
	if pad < 0 {
		pad = 0
	}
	return head + strings.Repeat(" ", pad) + src + tail
}

// takeCells returns the prefix of s covering n visible cells, keeping any
// escape sequences it passes through intact.
func takeCells(s string, n int, width func(string) int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	i := 0
	r := []rune(s)
	for i < len(r) && used < n {
		if r[i] == 0x1b { // copy an escape sequence without counting it
			start := i
			for i < len(r) && r[i] != 'm' {
				i++
			}
			if i < len(r) {
				i++
			}
			b.WriteString(string(r[start:i]))
			continue
		}
		w := width(string(r[i]))
		if used+w > n {
			break
		}
		b.WriteRune(r[i])
		used += w
		i++
	}
	return b.String()
}

// dropCells returns the remainder of s after n visible cells.
func dropCells(s string, n int, width func(string) int) string {
	var b strings.Builder
	used := 0
	r := []rune(s)
	i := 0
	for i < len(r) {
		if r[i] == 0x1b {
			start := i
			for i < len(r) && r[i] != 'm' {
				i++
			}
			if i < len(r) {
				i++
			}
			if used >= n {
				b.WriteString(string(r[start:i]))
			}
			continue
		}
		w := width(string(r[i]))
		if used >= n {
			b.WriteRune(r[i])
		}
		used += w
		i++
	}
	return b.String()
}
