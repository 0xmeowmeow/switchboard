package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestWindowStableWhenMovingWithinFrame is the core invariant behind "when
// I'm at the bottom of a long list and go up, it should work like normal" —
// once the window has scrolled to show the last page, moving the cursor up
// while it's still inside that page must not move the window. The old
// window(), which recomputed start fresh from cursor every call, failed
// this: any cursor >= visible pinned the window so cursor was always its
// LAST row, so every single "up" press had to scroll to compensate.
func TestWindowStableWhenMovingWithinFrame(t *testing.T) {
	var start int
	n, visible := 50, 10

	cursor := 0
	for cursor < n-1 {
		cursor++
		window(cursor, n, visible, &start)
	}
	if start != n-visible {
		t.Fatalf("expected the window pinned to the last page after scrolling to the end: start=%d want=%d", start, n-visible)
	}
	frameStart := start

	// Move up (visible-1) times: still inside the same frame every time.
	for i := 0; i < visible-1; i++ {
		cursor--
		s, _ := window(cursor, n, visible, &start)
		if s != frameStart {
			t.Fatalf("moving up within the visible frame (cursor=%d) shifted the window: got start=%d want=%d", cursor, s, frameStart)
		}
	}

	// One more "up" now crosses above the frame — exactly one line of scroll.
	cursor--
	s, _ := window(cursor, n, visible, &start)
	if s != frameStart-1 {
		t.Fatalf("expected exactly one line of scroll once cursor left the frame: got start=%d want=%d", s, frameStart-1)
	}
}

// TestWindowInvariants sweeps a cursor forward and back across lists of
// several sizes and checks the basic contract holds at every step: the
// window never runs off either end, and the cursor is always inside it
// (except when there are fewer items than the visible frame, in which case
// it's simply everything).
func TestWindowInvariants(t *testing.T) {
	for _, n := range []int{0, 1, 5, 9, 10, 11, 50, 237} {
		for _, visible := range []int{1, 5, 10, 30} {
			var start int
			check := func(cursor int) {
				s, e := window(cursor, n, visible, &start)
				if s < 0 {
					t.Fatalf("n=%d visible=%d cursor=%d: start went negative: %d", n, visible, cursor, s)
				}
				if e > n {
					t.Fatalf("n=%d visible=%d cursor=%d: end past the list: %d > %d", n, visible, cursor, e, n)
				}
				if e-s > visible {
					t.Fatalf("n=%d visible=%d cursor=%d: window wider than visible: %d", n, visible, cursor, e-s)
				}
				if n > 0 && (cursor < s || cursor >= e) {
					t.Fatalf("n=%d visible=%d cursor=%d: cursor fell outside its own window [%d,%d)", n, visible, cursor, s, e)
				}
			}
			for c := 0; c < n; c++ {
				check(c)
			}
			for c := n - 1; c >= 0; c-- {
				check(c)
			}
		}
	}
}

// TestItemsWindowStableAtBottom drives the real model through updateList,
// scrolling a long items list to the bottom with "j" and then pressing "k" —
// the same real user action the fix was for.
func TestItemsWindowStableAtBottom(t *testing.T) {
	m := initialModel()
	m.w, m.h = 160, 40
	m.cmds = nil
	for i := 0; i < 80; i++ {
		m.cmds = append(m.cmds, Cmd{Group: "bulk", Name: "item", Desc: "d"})
	}
	m.rebuildGroups()
	for i, g := range m.groups {
		if g == "bulk" {
			m.groupIdx = i
		}
	}
	m.rebuildItems()
	m.focus = focusItems

	press := func(key string) {
		nm, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = nm.(model)
	}

	for i := 0; i < len(m.items)-1; i++ {
		press("j")
	}
	if m.itemIdx != len(m.items)-1 {
		t.Fatalf("setup: expected cursor at the last item, got %d/%d", m.itemIdx, len(m.items))
	}
	bottomStart := m.itemWinStart
	if bottomStart == 0 {
		t.Fatalf("setup: expected the window to have scrolled at all with %d items", len(m.items))
	}

	_, _, rows := m.geometry()
	visible := rows - 2
	for i := 0; i < visible-1; i++ {
		press("k")
		if m.itemWinStart != bottomStart {
			t.Fatalf("pressing k within the visible frame shifted the window: itemIdx=%d start=%d want=%d",
				m.itemIdx, m.itemWinStart, bottomStart)
		}
	}
}
