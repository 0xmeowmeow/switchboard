package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

// footprint reports the two numbers canvas() bases its placement on: how
// many lines a screen's raw content has, and how wide its widest line is.
// canvas() itself always pads its output to a constant m.h rows (the
// backdrop fills whatever content doesn't reach), so a plain line count on
// canvas()'s own output can look stable even when the screen underneath is
// not — it's this pre-canvas footprint that has to hold still, or canvas()'s
// x/y placement shifts under you and the frame visibly jumps.
func footprint(s string) (n, cw int) {
	ls := strings.Split(s, "\n")
	n = len(ls)
	for _, l := range ls {
		if w := lipgloss.Width(l); w > cw {
			cw = w
		}
	}
	return
}

var jumpTestSizes = [][2]int{
	{120, 30}, {160, 40}, {200, 50}, {140, 24}, {100, 28}, {90, 20}, {240, 60},
}

// TestNoFrameJump walks every group and every selectable item across a range
// of terminal sizes; the rendered height must be identical for every
// selection at a given size, otherwise the frame "jumps" a line as the
// cursor moves.
func TestNoFrameJump(t *testing.T) {
	m := initialModel()

	for _, size := range jumpTestSizes {
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

// TestNoFrameJumpFootprint is TestNoFrameJump's width counterpart, plus both
// focus states: the group/item browser's footprint (height AND widest line)
// must be identical for every group, every item, and both focus states at a
// given terminal size.
func TestNoFrameJumpFootprint(t *testing.T) {
	m := initialModel()

	for _, size := range jumpTestSizes {
		m.w, m.h = size[0], size[1]
		type key struct{ n, cw int }
		shapes := map[key][]string{}

		for _, f := range []focus{focusGroups, focusItems} {
			m.focus = f
			for gi := range m.groups {
				m.groupIdx = gi
				m.rebuildItems()
				n, cw := footprint(m.baseView())
				shapes[key{n, cw}] = append(shapes[key{n, cw}], m.groups[gi])
			}
		}

		if len(shapes) != 1 {
			for k, who := range shapes {
				sample := who
				if len(sample) > 4 {
					sample = sample[:4]
				}
				t.Errorf("size %dx%d: footprint %v: %v", size[0], size[1], k, sample)
			}
		}
	}
}

// mkBTDevices synthesises n paired devices for footprint probes.
func mkBTDevices(n int) []btDevice {
	out := make([]btDevice, n)
	for i := range out {
		out[i] = btDevice{mac: "AA:BB:CC:DD:EE:00", name: "device", paired: true}
	}
	return out
}

// mkNets synthesises n networks for footprint probes.
func mkNets(n int) []wifiNet {
	out := make([]wifiNet, n)
	for i := range out {
		out[i] = wifiNet{ssid: "network"}
	}
	return out
}

// TestBluetoothNoFrameJump found a real bug: viewBluetooth's device-list pane
// had no fixed height, so it grew or shrank with the live device count (bare
// after a scan, tall once devices turn up) and the whole frame jumped on
// every device found or lost — worse than anywhere else in sb, because this
// list's length swings the most of any screen. Fixed by giving the pane the
// same clampLines + Height treatment as viewMain's rail/items panes.
func TestBluetoothNoFrameJump(t *testing.T) {
	m := initialModel()
	m.mode = modeBluetooth

	for _, size := range jumpTestSizes {
		m.w, m.h = size[0], size[1]
		type key struct{ n, cw int }
		shapes := map[key][]int{}
		for _, n := range []int{0, 1, 3, 8, 20, 60} {
			m.bt = &btState{devices: mkBTDevices(n)}
			ln, cw := footprint(m.baseView())
			shapes[key{ln, cw}] = append(shapes[key{ln, cw}], n)
		}
		if len(shapes) != 1 {
			t.Errorf("size %dx%d: bluetooth footprint varies by device count: %v", size[0], size[1], shapes)
		}
	}
}

// TestNetworkNoFrameJump is TestBluetoothNoFrameJump's Wi-Fi counterpart.
func TestNetworkNoFrameJump(t *testing.T) {
	m := initialModel()
	m.mode = modeNetwork

	for _, size := range jumpTestSizes {
		m.w, m.h = size[0], size[1]
		type key struct{ n, cw int }
		shapes := map[key][]int{}
		for _, n := range []int{0, 1, 3, 8, 20, 60} {
			m.net = &netState{radioOn: true, nets: mkNets(n)}
			ln, cw := footprint(m.baseView())
			shapes[key{ln, cw}] = append(shapes[key{ln, cw}], n)
		}
		if len(shapes) != 1 {
			t.Errorf("size %dx%d: network footprint varies by ssid count: %v", size[0], size[1], shapes)
		}
	}
}

// TestBluetoothStatusMsgNoFrameJump found a second bug: the status line at
// the bottom of viewBluetooth rendered s.msg unbounded, so a long device
// name mid-pair or mid-connect ("connecting to <name>…") could widen that
// one line past every other row on screen and shift canvas()'s horizontal
// centring — a sideways jump. Fixed by truncating it to the pane width.
func TestBluetoothStatusMsgNoFrameJump(t *testing.T) {
	m := initialModel()
	m.w, m.h = 160, 40
	m.mode = modeBluetooth
	m.bt = &btState{devices: mkBTDevices(1)}

	base, baseCw := footprint(m.baseView())
	for _, msg := range []string{
		"",
		"scanning…",
		"connecting to " + strings.Repeat("x", 200) + "…",
		"paired — trusting and connecting…",
	} {
		m.bt.msg = msg
		n, cw := footprint(m.baseView())
		if n != base || cw != baseCw {
			t.Errorf("status msg %q shifted footprint from (%d,%d) to (%d,%d)", msg, base, baseCw, n, cw)
		}
	}
}

// TestNetworkPasswordEntryNoFrameJump found the same class of bug in the
// Wi-Fi join prompt: its textinput.Model had no Width, so its View() grew by
// one cell per character typed (the join screen most people will actually
// use), widening the footer line and shifting the frame sideways as you
// type. Fixed by capping the field's Width so it scrolls internally instead.
func TestNetworkPasswordEntryNoFrameJump(t *testing.T) {
	m := initialModel()
	m.w, m.h = 160, 40
	m.mode = modeNetwork
	m.net = &netState{radioOn: true, nets: mkNets(1)}
	pw := textinput.New()
	pw.EchoMode = textinput.EchoPassword
	pw.CharLimit = 128
	pw.Width = 24 // must match startNmMonitor
	m.net.pw = pw
	m.net.asking = true

	base, baseCw := footprint(m.baseView())
	for i := 0; i < 60; i++ {
		m.net.pw.SetValue(m.net.pw.Value() + "x")
		n, cw := footprint(m.baseView())
		if n != base || cw != baseCw {
			t.Fatalf("typing character %d shifted footprint from (%d,%d) to (%d,%d)", i+1, base, baseCw, n, cw)
		}
	}
}
