// prefs.go — settings that are yours rather than the theme's.
//
// The split is deliberate. A theme is a palette: it says what colours things
// are. Preferences say how much room things get and how fast they move. You
// change themes for a mood; you change preferences once and forget.
//
// Stored at ~/.config/switchboard/prefs.json so anything else can read them.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

)

type prefs struct {
	// reading
	ReadWidth  int  `json:"read_width"`  // wrap column in study mode
	ReadCentre bool `json:"read_centre"` // centre the reading column
	ShowGotcha bool `json:"show_gotcha"`

	// the sidebar widget
	Widget      bool    `json:"widget"`       // draw it at all
	WidgetSpeed float64 `json:"widget_speed"` // 1.0 is the default rate
	WidgetScale float64 `json:"widget_scale"` // per-widget parameter, 0..2
	WidgetPick  int     `json:"widget_pick"`  // -1 = random each launch

	// terminal
	FontSize int `json:"font_size"` // kitty only; 0 leaves it alone

	// fonts mode
	ExportDir string `json:"export_dir"` // "" = ~/Pictures/switchboard-fonts
}

func defaultPrefs() prefs {
	return prefs{
		ReadWidth: 72, ReadCentre: true, ShowGotcha: true,
		Widget: true, WidgetSpeed: 1.0, WidgetScale: 1.0, WidgetPick: -1,
		FontSize: 0,
	}
}

func prefsPath() string { return filepath.Join(confDir(), "prefs.json") }

func loadPrefs() prefs {
	p := defaultPrefs()
	b, err := os.ReadFile(prefsPath())
	if err != nil {
		return p
	}
	json.Unmarshal(b, &p)
	// a corrupt or half-written file should degrade, not break the app
	if p.ReadWidth < 30 {
		p.ReadWidth = 30
	}
	if p.ReadWidth > 200 {
		p.ReadWidth = 200
	}
	if p.WidgetSpeed <= 0 {
		p.WidgetSpeed = 1
	}
	if p.WidgetScale <= 0 {
		p.WidgetScale = 1
	}
	return p
}

func (p prefs) save() {
	os.MkdirAll(confDir(), 0755)
	if b, err := json.MarshalIndent(p, "", "  "); err == nil {
		os.WriteFile(prefsPath(), b, 0644)
	}
}

// applyFontSize asks kitty to change its own font size. This is the one
// preference sb cannot honour by itself: a terminal's font is the terminal's
// business, and kitty is the only one here that exposes it. Silently does
// nothing anywhere else.
func (p prefs) applyFontSize() {
	if p.FontSize <= 0 || os.Getenv("KITTY_WINDOW_ID") == "" {
		return
	}
	go capture(fmt.Sprintf("kitten @ set-font-size %d", p.FontSize), 2e9)
}

// ---------------------------------------------------------------- rows

type prefRow struct {
	label string
	get   func(*prefs) string
	dec   func(*prefs)
	inc   func(*prefs)
	help  string
}

// Four settings. Every option is a decision you are asked to make, so the
// list is short on purpose: anything that only ever has one sensible value
// is a constant, not a preference.
var prefRows = []prefRow{
	{"reading width",
		func(p *prefs) string { return fmt.Sprintf("%d cols", p.ReadWidth) },
		func(p *prefs) { p.ReadWidth = maxi(30, p.ReadWidth-4) },
		func(p *prefs) { p.ReadWidth = mini(200, p.ReadWidth+4) },
		"where prose wraps in study mode"},
	{"sidebar widget",
		func(p *prefs) string {
			if !p.Widget {
				return "off"
			}
			if p.WidgetPick < 0 {
				return "random each launch"
			}
			_, name := sidebarFrame(p.WidgetPick%totalWidgets(), 8, 4, 0, 0, "#000")
			return name
		},
		func(p *prefs) { cycleWidget(p, -1) },
		func(p *prefs) { cycleWidget(p, +1) },
		"off, random, or a specific one — ctrl+w toggles from anywhere"},
	{"widget speed",
		func(p *prefs) string { return fmt.Sprintf("%.2f×", p.WidgetSpeed) },
		func(p *prefs) { p.WidgetSpeed = clampF(p.WidgetSpeed-0.25, 0.25, 4) },
		func(p *prefs) { p.WidgetSpeed = clampF(p.WidgetSpeed+0.25, 0.25, 4) },
		"how fast it animates"},
	{"terminal font size",
		func(p *prefs) string {
			if p.FontSize <= 0 {
				return "leave alone"
			}
			return fmt.Sprintf("%d pt", p.FontSize)
		},
		func(p *prefs) {
			if p.FontSize > 0 {
				p.FontSize--
			}
		},
		func(p *prefs) {
			if p.FontSize == 0 {
				p.FontSize = 12
			} else {
				p.FontSize = mini(40, p.FontSize+1)
			}
		},
		"kitty only, applied live"},
}

// cycleWidget walks one axis through off, random, then each animation, so a
// single control covers what used to be three rows.
func cycleWidget(p *prefs, d int) {
	t := totalWidgets()
	cur := -2 // off
	if p.Widget {
		cur = p.WidgetPick
	}
	cur += d
	switch {
	case cur < -2:
		cur = t - 1
	case cur >= t:
		cur = -2
	}
	p.Widget = cur != -2
	p.WidgetPick = maxi(-1, cur)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func wrapPick(n int) int {
	t := totalWidgets()
	if n < -1 {
		return t - 1
	}
	if n >= t {
		return -1
	}
	return n
}

// ---------------------------------------------------------------- view

func (m model) viewPrefs() string {
	w := mini(64, maxi(44, m.w-12))
	var b strings.Builder
	for i, r := range prefRows {
		val := r.get(&m.prefs)
		label := pad(truncate(r.label, 20), 20)
		line := " " + label + " " + pad(truncate(val, 22), 22)
		if i == m.prefSel {
			b.WriteString(selOn.Render(line) + "\n")
			b.WriteString("   " + cDim.Render(truncate(r.help, w-8)) + "\n")
		} else {
			b.WriteString(cBase.Render(line) + "\n")
		}
	}
	body := strings.TrimRight(b.String(), "\n")
	return modalBox("PREFERENCES", body,
		"jk select   hl change   r reset   esc close", w)
}
