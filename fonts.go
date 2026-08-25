// fonts.go — the TDF browser, as a mode rather than a menu entry.
//
// Split horizontally: the font list on top, a live render of your text
// underneath, updating as you move. 3722 fonts is unbrowsable by scrolling,
// so the list is always a filtered view and the filters are the interface.
//
// Auto-tags cost nothing — every one is derived from data already parsed
// while loading, so the browser is useful before you have tagged anything.
// Hand tags layer on top in ~/.config/tdfbrowse/tags.json and never touch
// the font files.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"switchboard/decor"
	"switchboard/tdf"
)

// The DOS palette in TheDraw's order. The index is the attribute nibble.
var dosPalette = [16]string{
	"#000000", "#0000aa", "#00aa00", "#00aaaa",
	"#aa0000", "#aa00aa", "#aa5500", "#aaaaaa",
	"#555555", "#5555ff", "#55ff55", "#55ffff",
	"#ff5555", "#ff55ff", "#ffff55", "#ffffff",
}

type fontEntry struct {
	file  string
	index int
	font  *tdf.Font
	w, h  int
	tags  []string
	key   string // file:index — a font's identity, never the filename
}

// ---------------------------------------------------------------- tagging

// autoTags derive from measurements taken during the load. Free, and enough
// to make the first run useful.
func autoTags(f *tdf.Font, w, h int) []string {
	var t []string
	t = append(t, tdf.TypeName(f.Type))

	switch {
	case h <= 4:
		t = append(t, "tiny")
	case h <= 7:
		t = append(t, "small")
	case h <= 10:
		t = append(t, "medium")
	default:
		t = append(t, "tall")
	}
	switch {
	case w <= 8:
		t = append(t, "narrow")
	case w <= 16:
		t = append(t, "regular")
	default:
		t = append(t, "wide")
	}

	n := f.Count()
	if n >= 94 {
		t = append(t, "full")
	} else if n >= 52 {
		t = append(t, "partial")
	} else {
		t = append(t, "sparse")
	}
	// a font with capitals but no lowercase — most of the collection
	if f.Glyph('A') != nil && f.Glyph('a') == nil {
		t = append(t, "caps")
	}
	if f.Monochrome() {
		t = append(t, "mono")
	} else {
		t = append(t, "multi")
	}
	if f.Glyph('0') != nil && f.Glyph('9') != nil {
		t = append(t, "digits")
	}
	return t
}

func tagsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tdfbrowse", "tags.json")
}

// ---------------------------------------------------------------- loading

func fontSearchPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".config", "tdfgo", "fonts"),
		"/usr/local/share/tdfgo/fonts",
		"/usr/share/tdfgo/fonts",
		"./fonts",
	}
}

type fontsLoadedMsg struct {
	entries []fontEntry
	err     error
}

// loadFonts walks every search path once. 1200 files parse in about a
// second, so it runs as a command rather than blocking the first frame.
func loadFonts() tea.Cmd {
	return func() tea.Msg {
		hand := map[string][]string{}
		if b, err := os.ReadFile(tagsPath()); err == nil {
			json.Unmarshal(b, &hand)
		}

		var out []fontEntry
		seen := map[string]bool{}
		for _, dir := range fontSearchPaths() {
			var files []string
			for _, pat := range []string{"*.tdf", "*.TDF", "*.Tdf"} {
				g, _ := filepath.Glob(filepath.Join(dir, pat))
				files = append(files, g...)
			}
			for _, p := range files {
				abs, _ := filepath.Abs(p)
				if seen[abs] {
					continue
				}
				seen[abs] = true
				b, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				fonts, err := tdf.Parse(b)
				if err != nil {
					continue
				}
				base := filepath.Base(p)
				for i, f := range fonts {
					w, h := f.Bounds()
					key := fmt.Sprintf("%s:%d", base, i)
					e := fontEntry{file: base, index: i, font: f, w: w, h: h, key: key}
					e.tags = append(autoTags(f, w, h), hand[key]...)
					out = append(out, e)
				}
			}
		}
		if len(out) == 0 {
			return fontsLoadedMsg{err: fmt.Errorf("no .tdf files found in %s",
				strings.Join(fontSearchPaths(), " "))}
		}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if a.font.Name != b.font.Name {
				return strings.ToLower(a.font.Name) < strings.ToLower(b.font.Name)
			}
			return a.key < b.key
		})
		return fontsLoadedMsg{entries: out}
	}
}

// ---------------------------------------------------------------- state

type fontState struct {
	all      []fontEntry
	shown    []int
	sel      int
	loading  bool
	err      string
	text     string   // what gets rendered in the preview
	filters  []string // active tag filters, ANDed
	scrollX  int
	msg      string
	tagInput bool
	editing  bool
}

// facets are the filters offered, in the order they are cycled.
var facets = []string{
	"", "colour", "block", "tiny", "small", "medium", "tall",
	"narrow", "regular", "wide", "full", "partial", "sparse",
	"mono", "multi", "caps", "digits",
}

func newFontState() *fontState {
	return &fontState{loading: true, text: "SWITCHBOARD"}
}

func (s *fontState) hasTag(e fontEntry, t string) bool {
	for _, x := range e.tags {
		if x == t {
			return true
		}
	}
	return false
}

func (s *fontState) refilter(query string) {
	s.shown = s.shown[:0]
	q := strings.ToLower(query)
	for i, e := range s.all {
		ok := true
		for _, f := range s.filters {
			if !s.hasTag(e, f) {
				ok = false
				break
			}
		}
		if ok && q != "" {
			ok = strings.Contains(strings.ToLower(e.font.Name), q) ||
				strings.Contains(strings.ToLower(e.file), q) ||
				strings.Contains(strings.ToLower(strings.Join(e.tags, " ")), q)
		}
		// a font that cannot spell the preview text is not a candidate
		if ok && s.text != "" {
			for _, r := range s.text {
				if r == ' ' {
					continue
				}
				if e.font.Glyph(r) == nil {
					ok = false
					break
				}
			}
		}
		if ok {
			s.shown = append(s.shown, i)
		}
	}
	if s.sel >= len(s.shown) {
		s.sel = maxi(0, len(s.shown)-1)
	}
}

func (s *fontState) current() *fontEntry {
	if len(s.shown) == 0 {
		return nil
	}
	return &s.all[s.shown[s.sel]]
}

func (s *fontState) toggleFilter(t string) {
	if t == "" {
		s.filters = nil
		return
	}
	for i, f := range s.filters {
		if f == t {
			s.filters = append(s.filters[:i], s.filters[i+1:]...)
			return
		}
	}
	s.filters = append(s.filters, t)
}

// saveTag records a hand tag against file:index — never against the filename,
// because one file can hold sixteen fonts.
func (s *fontState) saveTag(e *fontEntry, tag string) error {
	hand := map[string][]string{}
	if b, err := os.ReadFile(tagsPath()); err == nil {
		json.Unmarshal(b, &hand)
	}
	for _, t := range hand[e.key] {
		if t == tag {
			return nil
		}
	}
	hand[e.key] = append(hand[e.key], tag)
	e.tags = append(e.tags, tag)
	os.MkdirAll(filepath.Dir(tagsPath()), 0755)
	b, _ := json.MarshalIndent(hand, "", "  ")
	return os.WriteFile(tagsPath(), b, 0644)
}

// ---------------------------------------------------------------- render

// renderFont lays glyphs side by side into styled lines. Glyph rows are
// ragged — TheDraw ends a row rather than padding it — so short rows pad here.
func renderFont(f *tdf.Font, text string, mono bool) []string {
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
		return []string{"(this font has none of those characters)"}
	}
	spacing := int(f.Spacing)
	if spacing < 1 {
		spacing = 1
	}

	out := make([]string, height)
	for row := 0; row < height; row++ {
		var b strings.Builder
		for _, x := range gs {
			if x.glyph == nil { // a space between words
				b.WriteString(cCanvas.Render("    "))
				continue
			}
			width := int(x.glyph.Width)
			var cells []tdf.Cell
			if row < len(x.glyph.Rows) {
				cells = x.glyph.Rows[row]
			}
			for col := 0; col < width; col++ {
				// A glyph row is ragged — TheDraw ends a row rather than
				// padding it. An unpainted space here lets the canvas
				// backdrop show through the middle of a letter, so pad with
				// the app background instead of a bare " ".
				if col >= len(cells) {
					b.WriteString(cCanvas.Render(" "))
					continue
				}
				c := cells[col]
				if c.Ch == 0 {
					b.WriteString(cCanvas.Render(" "))
					continue
				}
				ch := string(tdf.Rune(c.Ch))
				if mono || f.Type != tdf.TypeColour {
					b.WriteString(cCool.Render(ch))
				} else {
					st := lipgloss.NewStyle().
						Foreground(lipgloss.Color(dosPalette[c.Fg&0x0F])).
						Background(lipgloss.Color(dosPalette[c.Bg&0x0F]))
					b.WriteString(st.Render(ch))
				}
			}
			b.WriteString(cCanvas.Render(strings.Repeat(" ", spacing)))
		}
		out[row] = b.String()
	}
	return out
}

// ---------------------------------------------------------------- update

func (m model) updateFonts(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.fonts
	if s == nil {
		m.mode = modeList
		return m, nil
	}
	e := s.current()

	// the modal owns the keyboard while it is open
	if s.editing {
		switch msg.String() {
		case "esc":
			s.editing = false
			m.filter.Blur()
		case "enter":
			s.editing = false
			s.text = m.filter.Value()
			m.filter.Blur()
			s.refilter("")
			s.sel = 0
			s.scrollX = 0
			s.msg = fmt.Sprintf("%d fonts can spell that", len(s.shown))
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	// typing a tag takes priority over every other binding
	if s.tagInput {
		switch msg.String() {
		case "esc":
			s.tagInput = false
			s.msg = ""
		case "enter":
			s.tagInput = false
			t := strings.TrimSpace(strings.ToLower(m.filter.Value()))
			if t != "" && e != nil {
				if err := s.saveTag(e, t); err != nil {
					s.msg = "tag failed: " + err.Error()
				} else {
					s.msg = "tagged " + e.font.Name + " " + t
				}
			}
			m.filter.SetValue("")
			m.filter.Blur()
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.mode = modeList
		return m, nil

	case "j", "down":
		if s.sel < len(s.shown)-1 {
			s.sel++
			s.scrollX = 0
		}
		return m, nil
	case "k", "up":
		if s.sel > 0 {
			s.sel--
			s.scrollX = 0
		}
		return m, nil
	case "ctrl+d":
		s.sel = mini(len(s.shown)-1, s.sel+10)
		return m, nil
	case "ctrl+u":
		s.sel = maxi(0, s.sel-10)
		return m, nil
	case "g":
		s.sel, s.scrollX = 0, 0
		return m, nil
	case "G":
		s.sel = maxi(0, len(s.shown)-1)
		return m, nil

	// the preview is wider than any terminal — 30 columns per glyph
	case "l", "right":
		s.scrollX += 8
		return m, nil
	case "h", "left":
		s.scrollX = maxi(0, s.scrollX-8)
		return m, nil
	case "0":
		s.scrollX = 0
		return m, nil

	case "f":
		// cycle the facet filter forward
		next := facets[0]
		if len(s.filters) > 0 {
			for i, f := range facets {
				if f == s.filters[len(s.filters)-1] {
					next = facets[(i+1)%len(facets)]
				}
			}
			s.filters = s.filters[:len(s.filters)-1]
		} else {
			next = facets[1]
		}
		s.toggleFilter(next)
		s.refilter(s.msgQuery())
		return m, nil

	case "F":
		s.filters = nil
		s.refilter(s.msgQuery())
		s.msg = "filters cleared"
		return m, nil

	case "t":
		if e != nil {
			s.tagInput = true
			m.filter.SetValue("")
			m.filter.Focus()
			s.msg = "tag: "
		}
		return m, nil

	case "e":
		s.editing = true
		s.tagInput = false
		m.filter.SetValue(s.text)
		m.filter.Focus()
		return m, nil

	case "y":
		if e != nil {
			s.msg = fmt.Sprintf("tdf -f %s -i %d '%s'", e.file, e.index, s.text)
		}
		return m, nil

	case "r":
		s.loading = true
		return m, loadFonts()
	}
	return m, nil
}

func (s *fontState) msgQuery() string { return "" }

// ---------------------------------------------------------------- view

func (m model) viewFonts() string {
	s := m.fonts
	if s == nil {
		return ""
	}
	w := m.contentWidth()
	h := m.h
	if h < 20 {
		h = 20
	}

	head := " " + gradient("▌ F O N T S", true) + "  "
	if s.loading {
		head += m.spin.View() + cDim.Render(" parsing every .tdf on disk…")
		return head + "\n"
	}
	if s.err != "" {
		return head + "\n\n " + cWarn.Render(s.err) + "\n"
	}

	head += cDim.Render(fmt.Sprintf("%d of %d", len(s.shown), len(s.all)))
	if len(s.filters) > 0 {
		head += "  " + cPurp.Render(strings.Join(s.filters, "+"))
	}

	// ---- top pane: the list
	listRows := (h - 14) / 2
	if listRows < 4 {
		listRows = 4
	}
	var list strings.Builder
	list.WriteString(paneTitle("fonts", w-4, true) + "\n")
	if len(s.shown) == 0 {
		list.WriteString(cDim.Render("nothing matches — F clears the filters"))
	}
	start, end := window(s.sel, len(s.shown), listRows)
	for i := start; i < end; i++ {
		e := s.all[s.shown[i]]
		tags := strings.Join(pickTags(e.tags, 4), " ")
		line := fmt.Sprintf(" %-16s %-15s %2dx%-2d %3d/94  %s",
			truncate(e.font.Name, 16), truncate(e.file, 15),
			e.w, e.h, e.font.Count(), tags)
		line = pad(truncate(line, w-6), w-6)
		if i == s.sel {
			list.WriteString(selOn.Render(line))
		} else {
			list.WriteString(cDim.Render(line))
		}
		list.WriteString("\n")
	}

	// ---- bottom pane: the render, scrolled horizontally
	var prev strings.Builder
	e := s.current()
	title := "preview"
	if e != nil {
		title = fmt.Sprintf("%s · %s #%d · %s",
			e.font.Name, e.file, e.index, tdf.TypeName(e.font.Type))
	}
	prev.WriteString(paneTitle(title, w-4, false) + "\n")
	if e != nil {
		lines := renderFont(e.font, s.text, false)
		for _, ln := range lines {
			prev.WriteString(clipCells(ln, s.scrollX, w-6) + "\n")
		}
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		paneOn.Width(w-2).Render(list.String()),
		paneOff.Width(w-2).Render(prev.String()),
	)

	foot := " " + cBase.Render("text: ") + cCool.Render(s.text)
	if s.tagInput {
		foot = m.filter.View()
	}
	keys := "jk move  hl scroll  e text  f filter  F clear  t tag  y copy cmd  q back"
	status := stTag.Render("FONTS") +
		stMid.Width(maxi(4, w-14)).Render(truncate(s.msg+"   "+cDim.Render(keys), maxi(4, w-18)))
	view := head + "\n" + body + "\n" + foot + "\n" + status

	if s.editing {
		view = overlayModal(view, modalBox(
			"PREVIEW TEXT",
			m.filter.View(),
			"↵ apply    esc cancel    only fonts that can spell it are listed",
			mini(56, w-8)))
	}
	return view
}

// pickTags keeps the informative ones and drops the noise.
func pickTags(tags []string, n int) []string {
	skip := map[string]bool{"regular": true, "full": true, "colour": true}
	var out []string
	for _, t := range tags {
		if skip[t] {
			continue
		}
		out = append(out, t)
		if len(out) >= n {
			break
		}
	}
	return out
}

// clipCells takes a window of a rendered line by visible cells. An escape
// sequence must be consumed whole — writing the ESC and then counting its
// parameter bytes as content shreds the colours, which is exactly what the
// first version did.
func clipCells(s string, from, width int) string {
	var b strings.Builder
	used := 0
	r := []rune(s)
	for i := 0; i < len(r); {
		if r[i] == 0x1b {
			start := i
			for i < len(r) && r[i] != 'm' {
				i++
			}
			if i < len(r) {
				i++
			}
			// keep every escape so the style in force is correct at the cut
			b.WriteString(string(r[start:i]))
			continue
		}
		cw := lipgloss.Width(string(r[i]))
		if used >= from+width {
			break
		}
		if used >= from {
			b.WriteRune(r[i])
		}
		used += cw
		i++
	}
	return b.String() + "\x1b[0m"
}

// ---------------------------------------------------------------- modal

// modalBox is a floating dialog: a titled, bordered panel that sits over
// whatever is behind it rather than replacing the screen.
func modalBox(title, body, help string, w int) string {
	if w < 24 {
		w = 24
	}
	inner := gradient("▌ "+title, true) + "\n\n" +
		pad(body, w-4) + "\n\n" +
		cDim.Render(truncate(help, w-4))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(themes[curTheme].Second)).
		Background(lipgloss.Color(themes[curTheme].Bg)).
		Padding(1, 2).Width(w).Render(inner)
}

// overlayModal composites a box over the centre of an already-rendered view,
// leaving everything around it visible.
func overlayModal(view, box string) string {
	bg := strings.Split(view, "\n")
	fg := strings.Split(box, "\n")
	bw, bh := lipgloss.Width(box), len(fg)
	vw := 0
	for _, l := range bg {
		if n := lipgloss.Width(l); n > vw {
			vw = n
		}
	}
	x := (vw - bw) / 2
	y := (len(bg) - bh) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return strings.Join(decor.Overlay(bg, fg, x, y, lipgloss.Width), "\n")
}
