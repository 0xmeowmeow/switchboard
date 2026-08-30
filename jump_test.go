package main

import (
	"strings"
	"testing"
)

// TestNoFrameJump walks every group and every selectable item across a range of
// terminal sizes; the rendered height must be identical for every selection at
// a given size, otherwise the frame "jumps" a line as the cursor moves.
func TestNoFrameJump(t *testing.T) {
	m := initialModel()

	for _, size := range [][2]int{{120, 30}, {160, 40}, {200, 50}, {140, 24}, {100, 28}, {90, 20}, {240, 60}} {
		m.w, m.h = size[0], size[1]
		counts := map[int][]string{}

		for gi := range m.groups {
			m.groupIdx = gi
			m.itemIdx = 0
			m.rebuildItems()
			for ii := range m.items {
				m.itemIdx = ii
				h := strings.Count(m.baseView(), "\n") + 1
				name := m.groups[gi]
				if c, ok := m.current(); ok {
					name += "/" + c.Name
				}
				counts[h] = append(counts[h], name)
			}
		}

		if len(counts) != 1 {
			for h, who := range counts {
				sample := who
				if len(sample) > 4 {
					sample = sample[:4]
				}
				t.Errorf("size %dx%d: %d selections render %d lines, e.g. %v",
					size[0], size[1], len(who), h, sample)
			}
		} else {
			for h := range counts {
				t.Logf("size %dx%d: all selections render %d lines ✓", size[0], size[1], h)
			}
		}
	}
}
