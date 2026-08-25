package decor

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func init() { lipgloss.SetColorProfile(termenv.TrueColor) }

// The backdrop is rendered as ONE styled string: an escape at position 0,
// then the glyphs. Splicing content into the middle drops that opening
// escape from the tail, so the tail inherits whatever colour the content
// last set. That is the green-glyphs bug.
func TestTailKeepsItsOwnColour(t *testing.T) {
	deco := lipgloss.NewStyle().Foreground(lipgloss.Color("#241a3d"))
	bg := deco.Render(strings.Repeat("猫咪", 10)) // 40 cells, one escape at the front

	green := lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00"))
	fg := green.Render("HEADER")

	out := spliceLine(bg, fg, 2, lipgloss.Width)

	// after the content ends, the next visible glyph must be re-coloured by
	// the backdrop style, not left running in the content's colour
	i := strings.Index(out, "HEADER")
	if i < 0 {
		t.Fatal("content missing")
	}
	tail := out[i+len("HEADER"):]
	if !strings.Contains(tail, "241a3d") && !strings.Contains(tail, "36;26;60") {
		t.Fatalf("tail lost the backdrop colour, it will inherit the content's:\n%q", tail)
	}
}
