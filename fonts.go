// fonts.go — the TDF browser, as a mode rather than a menu entry.
//
// Split horizontally: the font list on top, a live render of your text
// underneath, updating as you move. 3722 fonts is unbrowsable by scrolling,
// so the list is always a filtered view and the filters are the interface.
//
// A collection routinely ships the same design several times over — an
// outline, a block and a colour version, or the same shapes in a different
// DOS palette — under the identical font name. Those collapse into one row
// here; v/V cycle through the variants without cluttering the list with
// what is, to the eye, the same font five times.
//
// Auto-tags cost nothing — every one is derived from data already parsed
// while loading, so the browser is useful before you have touched anything.
package main

import (
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
// to make the browser useful and searchable with nothing hand-maintained.
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
					e.tags = autoTags(f, w, h)
					out = append(out, e)
				}
			}
		}
		if len(out) == 0 {
			return fontsLoadedMsg{err: fmt.Errorf("no .tdf files found in %s",
				strings.Join(fontSearchPaths(), " "))}
		}
		// grouping below relies on same-named fonts sitting next to each
		// other, so name is the primary sort key.
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

// buildGroups collapses consecutive same-name entries (the sort above puts
// every variant of a font next to each other) into one row apiece.
func buildGroups(all []fontEntry) [][]int {
	var groups [][]int
	for i := range all {
		name := strings.ToLower(all[i].font.Name)
		if n := len(groups); n > 0 {
			last := groups[n-1]
			if strings.ToLower(all[last[0]].font.Name) == name {
				groups[n-1] = append(last, i)
				continue
			}
		}
		groups = append(groups, []int{i})
	}
	return groups
}

// ---------------------------------------------------------------- state

type fontState struct {
	all      []fontEntry
	groups   [][]int // each: indices into `all` sharing a font name
	shown    []int   // indices into `groups`, filtered by query + spellability
	sel      int
	winStart int // see window() — persists so scrolling doesn't shift the frame
	variant  int // which member of the selected group is being previewed

	loading bool
	err     string
	text    string // what gets rendered in the preview
	scrollX int
	msg     string

	editing   bool // text-edit modal owns the keyboard
	exporting bool // export-format modal owns the keyboard
}

func newFontState() *fontState {
	return &fontState{loading: true, text: "SWITCHBOARD"}
}

func (s *fontState) refilter(query string) {
	s.shown = s.shown[:0]
	q := strings.ToLower(query)
	for gi, grp := range s.groups {
		e := s.all[grp[0]]
		ok := true
		if q != "" {
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
			s.shown = append(s.shown, gi)
		}
	}
	// always to the top: a position that's still numerically valid after
	// the query changes is not necessarily still the same font (see the
	// identical fix to the main list's rebuildItems for the full story).
	s.sel = 0
	s.variant = 0
}

// current is the specific variant the preview is showing right now.
func (s *fontState) current() *fontEntry {
	if len(s.shown) == 0 {
		return nil
	}
	grp := s.groups[s.shown[s.sel]]
	v := s.variant
	if v >= len(grp) {
		v = 0
	}
	return &s.all[grp[v]]
}

func (s *fontState) selGroup() []int {
	if len(s.shown) == 0 {
		return nil
	}
	return s.groups[s.shown[s.sel]]
}

// ---------------------------------------------------------------- render

// renderFont lays glyphs side by side into styled lines. Glyph rows are
// ragged — TheDraw ends a row rather than padding it — so short rows pad here.
func renderFont(f *tdf.Font, text string, mono bool) []string {
	grid := walkGlyphs(f, text)
	if grid == nil {
		return []string{"(this font has none of those characters)"}
	}
	out := make([]string, len(grid))
	for row, line := range grid {
		var b strings.Builder
		for _, c := range line {
			if c.gap || c.ch == 0 {
				b.WriteString(cCanvas.Render(" "))
				continue
			}
			ch := string(tdf.Rune(c.ch))
			if mono || f.Type != tdf.TypeColour {
				b.WriteString(cCool.Render(ch))
			} else {
				st := lipgloss.NewStyle().
					Foreground(lipgloss.Color(dosPalette[c.fg&0x0F])).
					Background(lipgloss.Color(dosPalette[c.bg&0x0F]))
				b.WriteString(st.Render(ch))
			}
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

	// the export modal owns the keyboard while it is open
	if s.exporting {
		switch msg.String() {
		case "esc":
			s.exporting = false
		case "1", "2", "3", "4":
			formats := map[string]string{"1": "html", "2": "png", "3": "ansi", "4": "txt"}
			e := s.current()
			path, err := exportFont(e, s.text, formats[msg.String()], m.prefs)
			if err != nil {
				s.msg = "export failed: " + err.Error()
			} else {
				copyPath(path)
				s.msg = "exported → " + path + "  (path copied)"
			}
			s.exporting = false
		}
		return m, nil
	}

	// the text-edit modal owns the keyboard while it is open
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

	e := s.current()

	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.mode = modeList
		return m, nil

	case "j", "down":
		if s.sel < len(s.shown)-1 {
			s.sel++
			s.scrollX, s.variant = 0, 0
		}
		return m, nil
	case "k", "up":
		if s.sel > 0 {
			s.sel--
			s.scrollX, s.variant = 0, 0
		}
		return m, nil
	case "ctrl+d":
		s.sel = mini(len(s.shown)-1, s.sel+10)
		s.variant = 0
		return m, nil
	case "ctrl+u":
		s.sel = maxi(0, s.sel-10)
		s.variant = 0
		return m, nil
	case "g":
		s.sel, s.scrollX, s.variant = 0, 0, 0
		return m, nil
	case "G":
		s.sel = maxi(0, len(s.shown)-1)
		s.variant = 0
		return m, nil

	// cycle the variants a row collapsed — same name, different type/palette
	case "v":
		if grp := s.selGroup(); len(grp) > 1 {
			s.variant = (s.variant + 1) % len(grp)
		}
		return m, nil
	case "V":
		if grp := s.selGroup(); len(grp) > 1 {
			s.variant = (s.variant - 1 + len(grp)) % len(grp)
		}
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

	case "e":
		s.editing = true
		m.filter.SetValue(s.text)
		m.filter.Focus()
		return m, nil

	case "x":
		if e != nil {
			s.exporting = true
			s.msg = ""
		}
		return m, nil

	case "r":
		s.loading = true
		return m, loadFonts()
	}
	return m, nil
}

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

	head += cDim.Render(fmt.Sprintf("%d of %d", len(s.shown), len(s.groups)))

	// ---- top pane: the list
	listRows := (h - 14) / 2
	if listRows < 4 {
		listRows = 4
	}
	var list strings.Builder
	list.WriteString(paneTitle("fonts", w-4, true) + "\n")
	if len(s.shown) == 0 {
		list.WriteString(cDim.Render("nothing matches — e changes the preview text"))
	}
	start, end := window(s.sel, len(s.shown), listRows, &s.winStart)
	for i := start; i < end; i++ {
		grp := s.groups[s.shown[i]]
		e := s.all[grp[0]]
		variants := ""
		if len(grp) > 1 {
			variants = fmt.Sprintf("×%d", len(grp))
		}
		tags := strings.Join(pickTags(e.tags, 4), " ")
		line := fmt.Sprintf(" %-16s %-15s %2dx%-2d %3d/94 %-3s %s",
			truncate(e.font.Name, 16), truncate(e.file, 15),
			e.w, e.h, e.font.Count(), variants, tags)
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
		if grp := s.selGroup(); len(grp) > 1 {
			title += fmt.Sprintf("  (variant %d/%d — v/V)", s.variant+1, len(grp))
		}
	}
	prev.WriteString(paneTitle(title, w-4, false) + "\n")
	if e != nil {
		lines := renderFont(e.font, s.text, false)
		for _, ln := range lines {
			prev.WriteString(clipCells(ln, s.scrollX, w-6) + "\n")
		}
	}

	// Both panes must render to a FIXED height, same reasoning as the
	// clampLines comment on viewMain's rail/items panes: without it, a font
	// with a taller glyph height than the last one selected grows the
	// preview pane and the whole frame jumps as you browse. A font taller
	// than the budget below gets its bottom rows clipped rather than
	// reflowing the screen — same trade sb already makes for a long
	// description in the detail strip.
	budget := listRows + 2 // paneTitle's 2 lines + up to `listRows` content lines
	body := lipgloss.JoinVertical(lipgloss.Left,
		paneOn.Width(w-2).Height(budget).Render(clampLines(list.String(), budget)),
		paneOff.Width(w-2).Height(budget).Render(clampLines(prev.String(), budget)),
	)

	foot := " " + cBase.Render("text: ") + cCool.Render(s.text)
	keys := "jk move  v variant  hl scroll  e text  x export  q back"
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
	if s.exporting {
		view = overlayModal(view, modalBox(
			"EXPORT",
			"1 html    2 png    3 ansi (.ans)    4 txt",
			"→ "+exportDir(m.prefs)+"    esc cancel",
			mini(60, w-8)))
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
		BorderBackground(lipgloss.Color(themes[curTheme].Bg)).
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
