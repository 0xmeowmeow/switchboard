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
	Lines  []string   // frame 0, kept for callers that do not animate
	Frames [][]string // one entry per frame; length 1 for a still
	Colour bool       // true for .ans — it carries its own escape codes
}

// Animated reports whether this piece has more than one frame.
func (a Art) Animated() bool { return len(a.Frames) > 1 }

// Frame returns frame n, wrapping. Safe on a still.
func (a Art) Frame(n int) []string {
	if len(a.Frames) == 0 {
		return a.Lines
	}
	if n < 0 {
		n = -n
	}
	return a.Frames[n%len(a.Frames)]
}

// splitFrames divides a file into frames. Two conventions are accepted, both
// of which are what ASCII animation collections actually use in the wild:
// a form feed (0x0C) between frames, or a line containing only "---".
func splitFrames(lines []string) [][]string {
	var frames [][]string
	cur := []string{}
	flush := func() {
		for len(cur) > 0 && strings.TrimSpace(cur[len(cur)-1]) == "" {
			cur = cur[:len(cur)-1]
		}
		if len(cur) > 0 {
			frames = append(frames, cur)
		}
		cur = []string{}
	}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "---" || strings.Contains(l, "\f") {
			// a form feed may sit at the end of a content line
			if idx := strings.Index(l, "\f"); idx > 0 {
				cur = append(cur, l[:idx])
			}
			flush()
			continue
		}
		cur = append(cur, l)
	}
	flush()
	return frames
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
		raw := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		frames := splitFrames(raw)
		if len(frames) == 0 {
			continue
		}
		// every frame must be small enough to be decoration
		bad := false
		for _, fr := range frames {
			if len(fr) > maxH {
				bad = true
				break
			}
			for _, l := range fr {
				if len(l) > maxW*4 { // generous: escapes inflate byte length
					bad = true
					break
				}
			}
		}
		if bad {
			continue
		}
		out = append(out, Art{
			Name:   strings.TrimSuffix(e.Name(), ext),
			Lines:  frames[0],
			Frames: frames,
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

// spliceLine puts src into dst starting at column x.
//
// The subtlety that produced a real bug: dst is rendered as ONE styled
// string, with its escape sequence at position 0. Cutting into the middle
// and keeping only the remainder throws that escape away, so the tail
// inherits whatever colour src last set — the backdrop came back green.
// dropCells therefore returns the styles that were in force at the cut, and
// they are re-emitted before the tail. src is fenced with resets so its own
// styling cannot leak either way.
func spliceLine(dst, src string, x int, width func(string) int) string {
	if x <= 0 && width(src) >= width(dst) {
		return src
	}
	head := takeCells(dst, x, width)
	srcW := width(src)
	styles, tail := dropCells(dst, x+srcW, width)

	pad := x - width(head)
	if pad < 0 {
		pad = 0
	}
	var b strings.Builder
	b.WriteString(head)
	if pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	b.WriteString("\x1b[0m")
	b.WriteString(src)
	b.WriteString("\x1b[0m")
	b.WriteString(styles) // restore the backdrop's own styling
	b.WriteString(tail)
	return b.String()
}

// Take is takeCells, exported: the prefix of s covering n visible cells with
// escape sequences preserved. Callers need this because a styled string
// cannot be truncated by rune count — the escape bytes are runes too, so a
// naive cut lands inside a sequence and shreds both the text and the colour.
func Take(s string, n int, width func(string) int) string {
	return takeCells(s, n, width)
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

// dropCells returns two things: every escape sequence that was in force
// before the cut, and the remainder of s after n visible cells. The caller
// needs the first to re-establish styling on the second.
func dropCells(s string, n int, width func(string) int) (styles, rest string) {
	var pre, out strings.Builder
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
			seq := string(r[start:i])
			if used >= n {
				out.WriteString(seq)
			} else {
				pre.WriteString(seq)
			}
			continue
		}
		w := width(string(r[i]))
		if used >= n {
			out.WriteRune(r[i])
		}
		used += w
		i++
	}
	return pre.String(), out.String()
}
