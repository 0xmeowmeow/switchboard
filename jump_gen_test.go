package main

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// mkGenLevel builds a generator level the way genResultMsg does, without
// actually shelling out to a generator command.
func mkGenLevel(m *model, title string, n int) genLevel {
	lv := genLevel{title: title, tmpl: "{}"}
	for i := 0; i < n; i++ {
		lv.items = append(lv.items, "item "+string(rune('a'+i%26))+string(rune('0'+i/26)))
	}
	lv.list = newGenList(lv.items, lv.title, m.watched, isGen(lv.tmpl))
	_, itemW, rows := m.geometry()
	lv.list.SetSize(itemW, maxi(1, rows-2))
	return lv
}

// TestGenListNoFrameJump found the same bug class as bluetooth/network/fonts
// in the generator (@gen) screens' new list.Model-backed picker: item count
// must not change the screen's footprint.
func TestGenListNoFrameJump(t *testing.T) {
	m := initialModel()
	m.mode = modeGen

	for _, size := range jumpTestSizes {
		m.w, m.h = size[0], size[1]
		type shape struct{ n, cw int }
		shapes := map[shape][]int{}
		for _, n := range []int{1, 3, 8, 20, 60, 200} {
			m.stack = []genLevel{mkGenLevel(&m, "test", n)}
			ln, cw := footprint(m.baseView())
			shapes[shape{ln, cw}] = append(shapes[shape{ln, cw}], n)
		}
		if len(shapes) != 1 {
			t.Errorf("size %dx%d: generator list footprint varies by item count: %v", size[0], size[1], shapes)
		}
	}
}

// TestGenListFilterNoFrameJump types a real query into the list's own fuzzy
// filter character by character and checks the footprint holds — the same
// class of bug the wifi password field had (an uncapped textinput growing
// the frame per keystroke). newGenList caps FilterInput.Width for this.
func TestGenListFilterNoFrameJump(t *testing.T) {
	m := initialModel()
	m.w, m.h = 160, 40
	m.mode = modeGen
	lv := mkGenLevel(&m, "test", 40)
	m.stack = []genLevel{lv}

	press := func(r rune) {
		var msg tea.KeyMsg
		if r == ' ' {
			msg = tea.KeyMsg{Type: tea.KeySpace}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		}
		newM, _ := m.updateGen(msg)
		m = newM.(model)
	}

	// enter the filter
	nm, _ := m.updateGen(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = nm.(model)
	if m.stack[0].list.FilterState() != list.Filtering {
		t.Fatalf("expected filtering to have started")
	}

	base, baseCw := footprint(m.baseView())
	for _, ch := range "a very long search query that keeps going and going" {
		press(ch)
		n, cw := footprint(m.baseView())
		if n != base || cw != baseCw {
			t.Fatalf("typing %q into the filter shifted footprint from (%d,%d) to (%d,%d)", ch, base, baseCw, n, cw)
		}
	}
}
