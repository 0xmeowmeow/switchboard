package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestResumeIndex(t *testing.T) {
	items := []string{"S01E01", "S01E02", "S01E03", "S01E04", "S01E05"}

	if _, ok := resumeIndex(items, "Show X", map[string]string{}); ok {
		t.Fatalf("expected no resume position with no history")
	}

	idx, ok := resumeIndex(items, "Show X", map[string]string{"Show X": "S01E03"})
	if !ok || items[idx] != "S01E04" {
		t.Fatalf("expected the row after S01E03, got index %d ok=%v", idx, ok)
	}

	// a different show's history must not leak across titles.
	if _, ok := resumeIndex(items, "Show Y", map[string]string{"Show X": "S01E03"}); ok {
		t.Fatalf("expected no resume position for an unrelated title")
	}

	// the last item was the one opened — nothing after it, stay put.
	idx, ok = resumeIndex(items, "Show X", map[string]string{"Show X": "S01E05"})
	if !ok || items[idx] != "S01E05" {
		t.Fatalf("expected to stay on the last item, got index %d ok=%v", idx, ok)
	}

	// the remembered line no longer appears (the generator's output changed).
	if _, ok := resumeIndex(items, "Show X", map[string]string{"Show X": "S00E99"}); ok {
		t.Fatalf("expected no resume position for a line that's gone")
	}
}

// TestGenResumesAfterLastOpened drives the real Update loop: open a
// generator level, "run" one of its leaf items (which records it via
// markOpened), leave, and re-enter the same level — the cursor should land
// one row past what was opened, not back at the top.
func TestGenResumesAfterLastOpened(t *testing.T) {
	// markOpened persists to ~/.config/switchboard/lastopened.json — isolate
	// that to a throwaway HOME so the test can't write into the real one.
	t.Setenv("HOME", t.TempDir())

	m := initialModel()
	m.w, m.h = 160, 40

	// push() itself shells out to a real command, so build the level by hand
	// and drive it straight through the same genResultMsg path Update uses.
	m.stack = append(m.stack, genLevel{title: "Show X", tmpl: "@exec play {}"})
	nm, _ := m.Update(genResultMsg{items: []string{"S01E01", "S01E02", "S01E03", "S01E04", "S01E05"}})
	m = nm.(model)

	lv := m.top()
	if lv == nil {
		t.Fatalf("expected a generator level after loading")
	}
	lv.list.Select(2) // S01E03
	nm, _ = m.updateGen(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(model)

	if m.lastOpened["Show X"] != "S01E03" {
		t.Fatalf("expected S01E03 recorded as last opened, got %q", m.lastOpened["Show X"])
	}

	// leave and re-enter the same level, as if the show were opened again.
	m.stack = m.stack[:0]
	m.stack = append(m.stack, genLevel{title: "Show X", tmpl: "@exec play {}"})
	nm, _ = m.Update(genResultMsg{items: []string{"S01E01", "S01E02", "S01E03", "S01E04", "S01E05"}})
	m = nm.(model)

	lv = m.top()
	it, ok := lv.list.SelectedItem().(genItem)
	if !ok || it.line != "S01E04" {
		t.Fatalf("expected the cursor to resume on S01E04, got %+v ok=%v", it, ok)
	}
}
