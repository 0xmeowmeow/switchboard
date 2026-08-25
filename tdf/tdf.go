// Package tdf reads and writes TheDraw font files.
//
// Why this exists rather than using tdfgo: tdfgo's cell reader takes a
// character byte and then unconditionally takes a second byte as colour,
// with no check of the font type. That is right for COLOUR fonts, which
// store 2 bytes per cell, and silently wrong for BLOCK and OUTLINE fonts,
// which store 1. On a block font every second character is eaten as an
// attribute, so you get half a glyph with random colours and no error.
//
// tdfgo also has no writer at all, so editing colours or authoring glyphs
// needs a serialiser regardless.
//
// FILE FORMAT
//
//	20 bytes   0x13 "TheDraw FONTS file" 0x1A
//	then, repeated for each font in the pack:
//	  4 bytes   0x55 0xAA 0x00 0xFF   separator
//	  1 byte    length of the name
//	 12 bytes   name, space/NUL padded
//	  4 bytes   reserved
//	  1 byte    type: 0 outline, 1 block, 2 colour
//	  1 byte    spacing
//	  2 bytes   uint16 LE, size of the glyph block that follows
//	188 bytes   94 × uint16 LE offsets into that block, one per character
//	            '!' (33) to '~' (126). 0xFFFF means the glyph is absent.
//	  n bytes   glyph data
//
// Each glyph: 1 byte width, 1 byte height, then cells.
// 0x0D ends a row, 0x00 ends the glyph.
// Colour fonts: 2 bytes per cell — character, then a DOS attribute byte
// whose low nibble is foreground and high nibble background.
// Block and outline fonts: 1 byte per cell, the character alone.
package tdf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	TypeOutline uint8 = 0
	TypeBlock   uint8 = 1
	TypeColour  uint8 = 2

	FirstChar = 33  // '!'
	NumChars  = 94  // '!'..'~'
	absent    = 0xFFFF

	rowEnd   = 0x0D
	glyphEnd = 0x00
)

var (
	magic = []byte("\x13TheDraw FONTS file\x1a")
	sep   = []byte{0x55, 0xAA, 0x00, 0xFF}
)

func TypeName(t uint8) string {
	switch t {
	case TypeOutline:
		return "outline"
	case TypeBlock:
		return "block"
	case TypeColour:
		return "colour"
	}
	return fmt.Sprintf("unknown(%d)", t)
}

// Cell is one character position in a glyph. Fg and Bg are DOS palette
// indices 0..15 and are meaningless for block and outline fonts.
type Cell struct {
	Ch byte
	Fg uint8
	Bg uint8
}

// Glyph is one character. Rows may be shorter than Width — TheDraw does not
// pad rows out, it just ends them.
type Glyph struct {
	Width  uint8
	Height uint8
	Rows   [][]Cell
}

// Cells returns every cell in reading order, for callers that do not care
// about row structure.
func (g *Glyph) Cells() []Cell {
	var out []Cell
	for _, r := range g.Rows {
		out = append(out, r...)
	}
	return out
}

// Font is one font. A file may hold many.
type Font struct {
	Name    string
	Type    uint8
	Spacing uint8
	Glyphs  [NumChars]*Glyph // nil means the font lacks that character
}

// Glyph returns the glyph for a rune, or nil if the font lacks it.
func (f *Font) Glyph(r rune) *Glyph {
	i := int(r) - FirstChar
	if i < 0 || i >= NumChars {
		return nil
	}
	return f.Glyphs[i]
}

// Count reports how many of the 94 slots are filled.
func (f *Font) Count() int {
	n := 0
	for _, g := range f.Glyphs {
		if g != nil {
			n++
		}
	}
	return n
}

// Bounds computes the widest and tallest glyph. The header fields for these
// are unreliable — block fonts routinely report 0×0 — so measure instead.
func (f *Font) Bounds() (w, h int) {
	for _, g := range f.Glyphs {
		if g == nil {
			continue
		}
		if int(g.Width) > w {
			w = int(g.Width)
		}
		if int(g.Height) > h {
			h = int(g.Height)
		}
	}
	return w, h
}

// Monochrome reports whether every cell shares one colour pair. Useful as an
// automatic tag. Always true for block and outline fonts.
func (f *Font) Monochrome() bool {
	if f.Type != TypeColour {
		return true
	}
	first, seen := Cell{}, false
	for _, g := range f.Glyphs {
		if g == nil {
			continue
		}
		for _, c := range g.Cells() {
			if c.Ch == ' ' {
				continue
			}
			if !seen {
				first, seen = c, true
				continue
			}
			if c.Fg != first.Fg || c.Bg != first.Bg {
				return false
			}
		}
	}
	return true
}

// Parse reads a whole .tdf file, which may contain several fonts.
func Parse(data []byte) ([]*Font, error) {
	if len(data) < len(magic) || string(data[:len(magic)]) != string(magic) {
		return nil, errors.New("not a TheDraw font file: bad magic")
	}
	pos := len(magic)

	var fonts []*Font
	for {
		// Scan to the next separator. Some files carry padding between
		// fonts, so search rather than assuming it sits flush.
		next := indexFrom(data, sep, pos)
		if next < 0 {
			break
		}
		p := next + len(sep)

		f, after, err := parseFont(data, p)
		if err != nil {
			// A malformed font should not cost us the rest of the pack.
			pos = p
			continue
		}
		fonts = append(fonts, f)
		pos = after
	}
	if len(fonts) == 0 {
		return nil, errors.New("no fonts found")
	}
	return fonts, nil
}

func indexFrom(data, needle []byte, from int) int {
	if from >= len(data) {
		return -1
	}
	for i := from; i+len(needle) <= len(data); i++ {
		if string(data[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}

func parseFont(data []byte, p int) (*Font, int, error) {
	// 1 name-length + 12 name + 4 reserved + 1 type + 1 spacing + 2 blocksize
	const headSize = 21
	if p+headSize+NumChars*2 > len(data) {
		return nil, 0, errors.New("truncated font header")
	}

	nameLen := int(data[p])
	if nameLen > 12 {
		nameLen = 12
	}
	name := strings.TrimRight(string(data[p+1:p+1+nameLen]), "\x00 ")
	p += 13 // length byte + 12 name bytes
	p += 4  // reserved

	f := &Font{Type: data[p], Spacing: data[p+1], Name: name}
	p += 2

	blockSize := int(binary.LittleEndian.Uint16(data[p : p+2]))
	p += 2

	offsets := make([]uint16, NumChars)
	for i := range offsets {
		offsets[i] = binary.LittleEndian.Uint16(data[p : p+2])
		p += 2
	}

	base := p
	end := base + blockSize
	if end > len(data) {
		end = len(data)
	}

	for i, off := range offsets {
		if off == absent {
			continue
		}
		start := base + int(off)
		if start+2 > len(data) {
			continue
		}
		g, err := parseGlyph(data, start, f.Type)
		if err != nil {
			continue // one bad glyph is not a bad font
		}
		f.Glyphs[i] = g
	}
	return f, end, nil
}

// parseGlyph is the whole point of this package: cellBytes depends on type.
func parseGlyph(data []byte, p int, fontType uint8) (*Glyph, error) {
	g := &Glyph{Width: data[p], Height: data[p+1]}
	p += 2

	cellBytes := 1
	if fontType == TypeColour {
		cellBytes = 2
	}

	row := []Cell{}
	for p < len(data) {
		ch := data[p]
		if ch == glyphEnd {
			g.Rows = append(g.Rows, row)
			return g, nil
		}
		if ch == rowEnd {
			g.Rows = append(g.Rows, row)
			row = []Cell{}
			p++
			continue
		}
		c := Cell{Ch: ch}
		if cellBytes == 2 {
			if p+1 >= len(data) {
				return nil, errors.New("truncated colour cell")
			}
			attr := data[p+1]
			c.Fg = attr & 0x0F
			c.Bg = attr >> 4
		}
		row = append(row, c)
		p += cellBytes
	}
	return nil, errors.New("unterminated glyph")
}

// Encode serialises fonts back to a .tdf file. tdfgo has no writer, so this
// is what makes copying a font and editing its colours possible.
func Encode(fonts []*Font) []byte {
	out := append([]byte{}, magic...)
	for _, f := range fonts {
		out = append(out, sep...)

		name := f.Name
		if len(name) > 12 {
			name = name[:12]
		}
		out = append(out, byte(len(name)))
		nameField := make([]byte, 12)
		copy(nameField, name)
		out = append(out, nameField...)
		out = append(out, 0, 0, 0, 0) // reserved
		out = append(out, f.Type, f.Spacing)

		// Build the glyph block first so offsets and block size are exact.
		var block []byte
		offsets := make([]uint16, NumChars)
		for i, g := range f.Glyphs {
			if g == nil {
				offsets[i] = absent
				continue
			}
			offsets[i] = uint16(len(block))
			block = append(block, g.Width, g.Height)
			for ri, row := range g.Rows {
				if ri > 0 {
					block = append(block, rowEnd)
				}
				for _, c := range row {
					block = append(block, c.Ch)
					if f.Type == TypeColour {
						block = append(block, c.Fg|(c.Bg<<4))
					}
				}
			}
			block = append(block, glyphEnd)
		}

		var sz [2]byte
		binary.LittleEndian.PutUint16(sz[:], uint16(len(block)))
		out = append(out, sz[:]...)
		for _, off := range offsets {
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], off)
			out = append(out, b[:]...)
		}
		out = append(out, block...)
	}
	return out
}
