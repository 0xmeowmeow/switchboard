// genlist.go — the generator (@gen) screens' picker, on bubbles/list instead
// of a hand-rolled loop. This is the "pick a thing from a dynamic list" case
// in sb — ollama models, a show's episodes, anything @gen produces — and it's
// the one screen where a real fuzzy filter (type "eps4" and find "S01E04",
// not just a substring match) earns its keep. list.Model owns navigation,
// filtering and pagination; genDelegate only has to draw one row the same
// way renderItems used to.
package main

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// genItem is one line of a generator's raw output, dressed with the two bits
// sb draws on top of it: whether it's marked watched, and whether selecting
// it descends into another generator (isGen(lv.tmpl)) rather than running it.
type genItem struct {
	line    string
	watched bool
	nested  bool
}

func (i genItem) FilterValue() string { return i.line }

// genItems converts a generator level's raw output into list.Items, tagging
// each with its current watched state.
func genItems(lines []string, levelTitle string, watched map[string]bool, nested bool) []list.Item {
	out := make([]list.Item, len(lines))
	for i, line := range lines {
		out[i] = genItem{line: line, watched: watched[watchedKey(levelTitle, line)], nested: nested}
	}
	return out
}

// newGenList builds the list for one generator level. Its size is set by the
// caller (main.go resizes it on load and on every WindowSizeMsg) — the value
// passed here only has to be non-zero so list.New doesn't divide by it.
func newGenList(lines []string, levelTitle string, watched map[string]bool, nested bool) list.Model {
	l := list.New(genItems(lines, levelTitle, watched, nested), genDelegate{}, 10, 10)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.FilterInput.PromptStyle = fg(themes[curTheme].Hot)
	// No manual cap on FilterInput.Width needed, unlike the wifi password
	// field: list.Model's own setSize (called from SetSize, which the caller
	// invokes on every resize) recomputes FilterInput.Width to fit every time.
	return l
}

// genDelegate renders one row exactly as renderItems' old inGen branch did:
// a watched checkmark, the line padded/truncated to width, and a nested-list
// marker — so swapping the engine underneath changed nothing on screen.
type genDelegate struct{}

func (genDelegate) Height() int                         { return 1 }
func (genDelegate) Spacing() int                        { return 0 }
func (genDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

// rebuildListItems refreshes a level's items after a watched mark flips,
// keeping any active filter and cursor position — list.SetItems re-filters
// in place rather than resetting the level back to the top.
func (lv *genLevel) rebuildListItems(watched map[string]bool) tea.Cmd {
	return lv.list.SetItems(genItems(lv.items, lv.title, watched, isGen(lv.tmpl)))
}

func (genDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	gi, ok := item.(genItem)
	if !ok {
		return
	}
	innerW := maxi(1, m.Width()-4)
	check := "  "
	if gi.watched {
		check = "✓ "
	}
	mark := " "
	if gi.nested {
		mark = "›"
	}
	text := check + pad(truncate(gi.line, innerW), innerW) + " "
	switch {
	case index == m.Index():
		fmt.Fprint(w, selOn.Render(" "+text)+cPurp.Render(mark))
	case gi.watched:
		fmt.Fprint(w, " "+cFant.Render(text)+cFant.Render(mark))
	default:
		fmt.Fprint(w, " "+cBase.Render(text)+cFant.Render(mark))
	}
}
