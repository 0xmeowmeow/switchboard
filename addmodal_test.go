package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestEditKeyOpensPopupNotEditor is the regression this was actually about:
// "e" used to hand off to $EDITOR on the raw config file. It must now open
// the in-app popup instead — never quit, never set m.chosen to a shell-out.
func TestEditKeyOpensPopupNotEditor(t *testing.T) {
	m := initialModel()
	m.cmds = []Cmd{{Group: "system", Name: "apps", Desc: "d", Run: "r"}}
	m.rebuildGroups()
	m.rebuildItems()
	for i, g := range m.groups {
		if g == "system" {
			m.groupIdx = i
		}
	}
	m.rebuildItems()
	m.focus = focusItems

	nm, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = nm.(model)

	if m.quitting {
		t.Fatalf("pressing e should not quit sb")
	}
	if m.chosen != nil {
		t.Fatalf("pressing e should not hand off to a shell command, got %+v", m.chosen)
	}
	if !m.addOpen {
		t.Fatalf("expected the edit popup to be open")
	}
	if m.addEditIdx < 0 {
		t.Fatalf("expected addEditIdx to point at the command being edited")
	}
	if v := m.addBuf[1].Value(); v != "apps" {
		t.Fatalf("expected the name field pre-filled with %q, got %q", "apps", v)
	}
}

// TestEditSavesInPlace checks editing a command replaces it rather than
// appending a duplicate, and that the popup closes (addOpen false) with the
// underlying mode untouched.
//
// This test's "enter" reaches updateAdd's save path, which calls
// saveCommands — a real write to ~/.config/switchboard/commands.conf. HOME
// is isolated to a throwaway dir for exactly that reason: this test ran
// without it once, on 2026-09-02, and overwrote the real config with its
// two-line fixture. Recovered from a stale backup plus session notes; never
// again — every test whose path can reach a save/write must isolate HOME.
func TestEditSavesInPlace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := initialModel()
	m.cmds = []Cmd{
		{Group: "system", Name: "apps", Desc: "old desc", Run: "old-run"},
		{Group: "system", Name: "other", Desc: "d", Run: "r"},
	}
	m.rebuildGroups()
	for i, g := range m.groups {
		if g == "system" {
			m.groupIdx = i
		}
	}
	m.rebuildItems()
	m.focus = focusItems
	// select "apps" specifically
	for i, idx := range m.items {
		if m.cmds[idx].Name == "apps" {
			m.itemIdx = i
		}
	}

	nm, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = nm.(model)

	m.addBuf[0].SetValue("ai") // move it into a new group
	nm, _ = m.updateAdd(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(model)

	if m.addOpen {
		t.Fatalf("expected the popup to close after saving")
	}
	if len(m.cmds) != 2 {
		t.Fatalf("expected editing to replace in place, not add a duplicate: got %d commands", len(m.cmds))
	}
	found := false
	for _, c := range m.cmds {
		if c.Name == "apps" {
			found = true
			if c.Group != "ai" {
				t.Fatalf("expected apps moved to group ai, got %q", c.Group)
			}
			if c.Desc != "old desc" {
				t.Fatalf("expected the untouched fields preserved, desc got %q", c.Desc)
			}
		}
	}
	if !found {
		t.Fatalf("expected apps to still exist after editing")
	}
}

// TestAddModalNoFrameJump types long values into every field and checks the
// popup's rendered footprint never moves — same discipline as the rest of
// this codebase's jump fixes. It also checks every field's prompt is still
// present: modalBox's pad() measures the whole multi-line body as one
// string and, if any line is too wide, truncates the WHOLE thing by rune
// count rather than per line — so an uncapped field doesn't just widen the
// popup, it can silently cut later fields out of it entirely. addBuf's
// capped Width is what keeps every line short enough that path never fires.
func TestAddModalNoFrameJump(t *testing.T) {
	m := initialModel()
	m.w, m.h = 160, 40
	m.addOpen = true
	m.addEditIdx = -1

	base, baseCw := footprint(m.View())
	long := strings.Repeat("x", 300)
	for i := range m.addBuf {
		m.addBuf[i].SetValue(long)
		out := m.View()
		n, cw := footprint(out)
		if n != base || cw != baseCw {
			t.Fatalf("a long value in field %d shifted the popup's footprint from (%d,%d) to (%d,%d)", i, base, baseCw, n, cw)
		}
		if !strings.Contains(out, "note (optional)") {
			t.Fatalf("a long value in field %d caused the last field to disappear from the popup", i)
		}
	}
}
