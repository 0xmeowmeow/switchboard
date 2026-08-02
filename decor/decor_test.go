package decor

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func init() { lipgloss.SetColorProfile(termenv.TrueColor) }

func w(s string) int { return lipgloss.Width(s) }

func TestTileFillsExactly(t *testing.T) {
	rows := Tile([]string{"猫", "咪"}, 20, 3, w)
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	for i, r := range rows {
		if got := w(r); got != 20 {
			t.Fatalf("row %d width %d, want 20 (%q)", i, got, r)
		}
	}
	if rows[0] == rows[1] {
		t.Fatal("rows should be offset so the pattern does not stripe")
	}
}

func TestTileOddWidthWithWideGlyphs(t *testing.T) {
	// 21 is odd, glyphs are 2 cells: the last cell must be padded, not overrun
	rows := Tile([]string{"猫"}, 21, 2, w)
	for _, r := range rows {
		if w(r) != 21 {
			t.Fatalf("width %d want 21", w(r))
		}
	}
}

func TestSpliceKeepsWidthAndColour(t *testing.T) {
	bg := strings.Repeat("·", 40)
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff00ff")).Render("BOX")
	out := spliceLine(bg, fg, 10, w)
	if w(out) != 40 {
		t.Fatalf("width %d want 40", w(out))
	}
	if !strings.Contains(out, "BOX") {
		t.Fatal("content lost")
	}
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("colour lost")
	}
}

func TestOverlayLeavesBackdropAround(t *testing.T) {
	bg := Tile([]string{"猫", "咪"}, 30, 5, w)
	fg := []string{"┌──┐", "│hi│", "└──┘"}
	out := Overlay(bg, fg, 4, 1, w)
	if len(out) != 5 {
		t.Fatalf("lines %d", len(out))
	}
	if !strings.Contains(out[2], "hi") {
		t.Fatalf("fg missing: %q", out[2])
	}
	if !strings.Contains(out[0], "猫") {
		t.Fatal("backdrop row above the box was lost")
	}
	for i, l := range out {
		if got := w(l); got != 30 {
			t.Fatalf("line %d width %d want 30: %q", i, got, l)
		}
	}
}
