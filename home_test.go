package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func keyPress(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestHomeRenders(t *testing.T) {
	m := initialModel()
	for _, size := range [][2]int{{120, 32}, {160, 40}, {90, 24}, {200, 50}} {
		m.w, m.h = size[0], size[1]
		v := m.canvas(m.baseView())
		h := strings.Count(v, "\n") + 1
		w := lipgloss.Width(v)
		t.Logf("=== %dx%d -> %dx%d ===\n%s", size[0], size[1], w, h, v)
		if h > size[1] {
			t.Errorf("%dx%d: home is %d lines, taller than the terminal", size[0], size[1], h)
		}
	}
}

func TestHomeEnterOpensGroup(t *testing.T) {
	m := initialModel()
	m.w, m.h = 120, 32
	if m.mode != modeHome {
		t.Fatalf("initial mode = %v, want modeHome", m.mode)
	}
	want := m.groups[m.groupIdx]
	nm, _ := m.updateHome(keyPress("enter"))
	got := nm.(model)
	if got.mode != modeList {
		t.Errorf("after enter, mode = %v, want modeList", got.mode)
	}
	if got.focus != focusItems {
		t.Errorf("after enter, focus = %v, want focusItems", got.focus)
	}
	if got.groups[got.groupIdx] != want {
		t.Errorf("after enter, group = %q, want %q", got.groups[got.groupIdx], want)
	}
	// q from the list goes back home, not quit
	nm2, _ := got.updateList(keyPress("q"))
	got2 := nm2.(model)
	if got2.quitting {
		t.Errorf("q in the list quit instead of returning home")
	}
	if got2.mode != modeHome {
		t.Errorf("after q, mode = %v, want modeHome", got2.mode)
	}
}
