package main

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The home screen is a grid of tiles, one per group, shown before the
// two-pane browser. It is the top level: `q`/`esc` here quits, `q`/`esc`
// from the list steps back here. Picking a tile drops into the existing
// list view scoped to that group, so nothing about that view changes.
//
// The tile cursor IS m.groupIdx — the tiles are m.groups in order — so
// entering a tile needs no extra state, and coming back lands on the tile
// you last opened.

// groupGlyph is the icon on a group's tile. Single-cell glyphs only, so the
// grid stays aligned; unknown groups get a neutral dot.
func groupGlyph(name string) string {
	switch name {
	case allGroups:
		return "✦"
	case "ai":
		return "✧"
	case "music":
		return "♪"
	case "play":
		return "▶"
	case "writing":
		return "✎"
	case "making":
		return "⚒"
	case "code":
		return "⌘"
	case "work":
		return "▦"
	case "notes":
		return "✐"
	case "system":
		return "⚙"
	case "apps":
		return "▤"
	case "study":
		return "❃"
	}
	return "•"
}

// groupLabel is the word on the tile. Groups are mostly already verbs; only
// "all" reads better renamed. Real intent-routing (a "watch" tile that pulls
// the right entries regardless of group) comes with config tags later.
func groupLabel(name string) string {
	if name == allGroups {
		return "everything"
	}
	return name
}

const (
	homeTileW   = 22
	homeTileGap = 2
)

func (m model) homeCols() int {
	cols := (m.contentWidth() + homeTileGap) / (homeTileW + homeTileGap)
	if cols < 2 {
		cols = 2
	}
	if cols > len(m.groups) {
		cols = len(m.groups)
	}
	if cols < 1 {
		cols = 1
	}
	return cols
}

func (m model) viewHome() string {
	head := m.bannerTxt
	if head == "" {
		head = " " + gradient("▌ S W I T C H B O A R D", true) + "  " +
			cDim.Render("what are you here to do?")
	}

	counts := map[string]int{}
	for _, c := range m.cmds {
		counts[c.Group]++
	}

	inner := homeTileW - 2 // paneOn/paneOff have Padding(0,1)
	tiles := make([]string, len(m.groups))
	for i, g := range m.groups {
		n := counts[g]
		if g == allGroups {
			n = len(m.cmds)
		}
		noun := "things"
		if n == 1 {
			noun = "thing"
		}

		icon := cCool.Render(groupGlyph(g))
		name := groupLabel(g)
		count := fmt.Sprintf("%d %s", n, noun)

		style := paneOff
		nameStyle, countStyle := cBase, cFant
		if i == m.groupIdx {
			style = paneOn
			nameStyle, countStyle = cCool.Bold(true), cDim
		}

		row1 := icon + "  " + nameStyle.Render(truncate(name, inner-3))
		row2 := countStyle.Render(truncate(count, inner))
		tiles[i] = style.Width(homeTileW).MarginRight(homeTileGap).
			Render(row1 + "\n" + row2)
	}

	cols := m.homeCols()
	var rows []string
	for r := 0; r < len(tiles); r += cols {
		end := r + cols
		if end > len(tiles) {
			end = len(tiles)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, tiles[r:end]...))
	}
	grid := lipgloss.JoinVertical(lipgloss.Left, rows...)

	hint := cDim.Render("  ↵ open   / search everything   a add   t theme   , prefs   q quit")

	return head + "\n\n" + grid + "\n\n" + hint
}

func (m model) updateHome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cols := m.homeCols()
	n := len(m.groups)

	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.quitting = true
		return m, tea.Quit

	case "left", "h":
		if m.groupIdx > 0 {
			m.groupIdx--
		}
	case "right", "l":
		if m.groupIdx < n-1 {
			m.groupIdx++
		}
	case "up", "k":
		if m.groupIdx-cols >= 0 {
			m.groupIdx -= cols
		}
	case "down", "j":
		if m.groupIdx+cols < n {
			m.groupIdx += cols
		}
	case "g", "home":
		m.groupIdx = 0
	case "G", "end":
		m.groupIdx = n - 1

	case "enter", " ": // space or enter opens the tile
		m.itemIdx = 0
		m.rebuildItems()
		m.mode = modeList
		m.focus = focusItems
		m.status = ""

	case "/":
		m.groupIdx = 0 // filter searches every group
		m.itemIdx = 0
		m.rebuildItems()
		m.mode = modeFilter
		m.filter.Focus()
		m.status = ""
		return m, textinput.Blink

	case "a":
		m.mode = modeAdd
		m.addField = 0
		m.addBuf[0].Focus()
		m.status = ""
		return m, textinput.Blink

	case "S":
		if m.study == nil {
			m.study = newStudyState()
		}
		m.mode = modeStudy
		m.status = ""

	case "t", "T":
		cycleTheme(msg.String() == "t")
		m.bannerTxt = customBanner()
		m.filter.PromptStyle = fg(themes[curTheme].Hot)
		m.status = "theme: " + themes[curTheme].Name
	}

	return m, nil
}

// cycleTheme steps the active theme and persists it. Shared by the home grid
// and the list view so `t`/`T` behave the same in both.
func cycleTheme(forward bool) {
	if forward {
		curTheme = (curTheme + 1) % len(themes)
	} else {
		curTheme = (curTheme - 1 + len(themes)) % len(themes)
	}
	applyTheme(themes[curTheme])
	saveTheme(themes[curTheme].Name)
}
