// export.go — turning a rendered font into a file someone else can grab:
// HTML, PNG (via ansilove), a raw ANSI-art terminal dump, or plain text.
//
// All four read the same glyph grid the live terminal preview draws from
// (walkGlyphs, below), so what lands on disk is exactly what you were
// looking at — never a second, drifting implementation of the layout.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/atotto/clipboard"

	"switchboard/tdf"
)

// ---------------------------------------------------------------- layout

// glyphCell is one cell of the laid-out grid: a CP437 byte and its DOS
// attribute, or a gap (the space between glyphs or between words — not a
// painted cell at all).
type glyphCell struct {
	ch     byte
	fg, bg uint8
	gap    bool
}

// walkGlyphs lays text out as a grid of source cells against the font's
// natural row height, ragged rows padded with unpainted cells. Every
// renderer — the terminal preview and all four export formats — reads this
// same grid, so they can never disagree about what the text looks like.
func walkGlyphs(f *tdf.Font, text string) [][]glyphCell {
	type g struct{ glyph *tdf.Glyph }
	var gs []g
	height := 0
	for _, r := range text {
		if r == ' ' {
			gs = append(gs, g{nil})
			continue
		}
		gl := f.Glyph(r)
		if gl == nil {
			continue
		}
		gs = append(gs, g{gl})
		if int(gl.Height) > height {
			height = int(gl.Height)
		}
	}
	if height == 0 {
		return nil
	}
	spacing := int(f.Spacing)
	if spacing < 1 {
		spacing = 1
	}

	grid := make([][]glyphCell, height)
	for row := 0; row < height; row++ {
		var line []glyphCell
		for _, x := range gs {
			if x.glyph == nil { // a space between words
				for i := 0; i < 4; i++ {
					line = append(line, glyphCell{gap: true})
				}
				continue
			}
			width := int(x.glyph.Width)
			var cells []tdf.Cell
			if row < len(x.glyph.Rows) {
				cells = x.glyph.Rows[row]
			}
			for col := 0; col < width; col++ {
				if col >= len(cells) || cells[col].Ch == 0 {
					line = append(line, glyphCell{})
					continue
				}
				c := cells[col]
				line = append(line, glyphCell{ch: c.Ch, fg: c.Fg, bg: c.Bg})
			}
			for i := 0; i < spacing; i++ {
				line = append(line, glyphCell{gap: true})
			}
		}
		grid[row] = line
	}
	return grid
}

// ---------------------------------------------------------------- export

// exportDir is where exports land unless overridden in prefs.
func exportDir(p prefs) string {
	if p.ExportDir != "" {
		return p.ExportDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Pictures", "switchboard-fonts")
}

func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r == ' ':
			return '-'
		}
		return -1
	}, s)
	if s == "" {
		return "font"
	}
	return s
}

// copyPath puts an exported file's path on the clipboard — the "grab it
// from something else" path for anything that doesn't want to watch a
// directory: a game's build script can just paste.
func copyPath(path string) {
	_ = clipboard.WriteAll(path)
}

// exportFont writes one file for the given entry+text in the requested
// format and returns its path.
func exportFont(e *fontEntry, text, format string, p prefs) (string, error) {
	dir := exportDir(p)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	base := sanitizeName(e.font.Name) + "-" + sanitizeName(text)

	switch format {
	case "html":
		path := filepath.Join(dir, base+".html")
		return path, os.WriteFile(path, []byte(exportHTML(e.font, text)), 0644)
	case "txt":
		path := filepath.Join(dir, base+".txt")
		return path, os.WriteFile(path, []byte(exportTXT(e.font, text)), 0644)
	case "ansi":
		path := filepath.Join(dir, base+".ans")
		return path, os.WriteFile(path, exportANSI(e.font, text), 0644)
	case "png":
		return exportPNG(e.font, text, dir, base)
	}
	return "", fmt.Errorf("unknown export format: %s", format)
}

func htmlEscape(r rune) string {
	switch r {
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	case '&':
		return "&amp;"
	}
	return string(r)
}

func exportHTML(f *tdf.Font, text string) string {
	grid := walkGlyphs(f, text)
	var b strings.Builder
	b.WriteString("<!doctype html>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<pre style=\"background:#000;color:#aaa;font-family:monospace;line-height:1.15;padding:1em\">\n")
	for _, row := range grid {
		lastFg, lastBg := -1, -1
		open := false
		for _, c := range row {
			if c.gap || c.ch == 0 {
				if open {
					b.WriteString("</span>")
					open = false
				}
				b.WriteString(" ")
				lastFg, lastBg = -1, -1
				continue
			}
			fg, bg := int(c.fg&0x0F), int(c.bg&0x0F)
			if fg != lastFg || bg != lastBg {
				if open {
					b.WriteString("</span>")
				}
				fmt.Fprintf(&b, "<span style=\"color:%s;background:%s\">", dosPalette[fg], dosPalette[bg])
				lastFg, lastBg = fg, bg
				open = true
			}
			b.WriteString(htmlEscape(tdf.Rune(c.ch)))
		}
		if open {
			b.WriteString("</span>")
		}
		b.WriteString("\n")
	}
	b.WriteString("</pre>\n")
	return b.String()
}

func exportTXT(f *tdf.Font, text string) string {
	grid := walkGlyphs(f, text)
	var b strings.Builder
	for _, row := range grid {
		for _, c := range row {
			if c.gap || c.ch == 0 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(tdf.Rune(c.ch))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// dosToAnsiFg/Bg map TheDraw's 16-colour DOS attribute nibble to the
// standard ANSI SGR codes ansilove — and every real terminal — expects.
// The order isn't 1:1: DOS/CGA and ANSI number the same sixteen colours
// differently.
var dosToAnsiFg = [16]int{30, 34, 32, 36, 31, 35, 33, 37, 90, 94, 92, 96, 91, 95, 93, 97}
var dosToAnsiBg = [16]int{40, 44, 42, 46, 41, 45, 43, 47, 100, 104, 102, 106, 101, 105, 103, 107}

// exportANSI renders a classic .ans dump: raw CP437 bytes with SGR colour
// codes, CRLF line endings — what ansilove (and any real ANSI viewer)
// expects.
func exportANSI(f *tdf.Font, text string) []byte {
	grid := walkGlyphs(f, text)
	var b bytes.Buffer
	for _, row := range grid {
		lastFg, lastBg := -1, -1
		for _, c := range row {
			if c.gap || c.ch == 0 {
				b.WriteByte(' ')
				lastFg, lastBg = -1, -1
				continue
			}
			fg, bg := dosToAnsiFg[c.fg&0x0F], dosToAnsiBg[c.bg&0x0F]
			if fg != lastFg || bg != lastBg {
				fmt.Fprintf(&b, "\x1b[0;%d;%dm", fg, bg)
				lastFg, lastBg = fg, bg
			}
			b.WriteByte(c.ch)
		}
		b.WriteString("\x1b[0m\r\n")
	}
	return b.Bytes()
}

// exportPNG rasterizes via ansilove rather than reimplementing a CP437
// bitmap renderer — it's already installed, and it's the tool that already
// gets this exactly right.
func exportPNG(f *tdf.Font, text, dir, base string) (string, error) {
	if _, err := exec.LookPath("ansilove"); err != nil {
		return "", fmt.Errorf("ansilove not installed — needed for PNG export")
	}
	tmp, err := os.CreateTemp("", "sb-font-*.ans")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(exportANSI(f, text)); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()

	path := filepath.Join(dir, base+".png")
	out, err := exec.Command("ansilove", "-o", path, tmpPath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ansilove: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return path, nil
}
