// lastopened.go — remembers, per generator level, the last line you actually
// opened (ran) there. Deliberately separate from watched.go's manual mark:
// as that file's own comment says, "I opened it" and "I watched it" are not
// the same thing, so this is recorded automatically, on every run, with no
// key to press. Re-entering the same level — the same show's episode list,
// say — starts the cursor one row after that line, so picking up a series
// is an enter key away, not a rescan from the top every time.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func lastOpenedPath() string { return filepath.Join(confDir(), "lastopened.json") }

func loadLastOpened() map[string]string {
	lo := map[string]string{}
	b, err := os.ReadFile(lastOpenedPath())
	if err != nil {
		return lo
	}
	json.Unmarshal(b, &lo)
	return lo
}

// markOpened records line as the last one run under levelTitle.
func (m *model) markOpened(levelTitle, line string) {
	if m.lastOpened == nil {
		m.lastOpened = map[string]string{}
	}
	m.lastOpened[levelTitle] = line
	os.MkdirAll(confDir(), 0755)
	if b, err := json.MarshalIndent(m.lastOpened, "", "  "); err == nil {
		os.WriteFile(lastOpenedPath(), b, 0644)
	}
}

// resumeIndex finds where lv.items sat last time something was opened under
// this title, and returns one row past it — the row you'd want the cursor on
// to continue. ok is false when there's no history for this title, or the
// remembered line is no longer in the list (the generator's output changed).
func resumeIndex(items []string, levelTitle string, lastOpened map[string]string) (int, bool) {
	line, had := lastOpened[levelTitle]
	if !had {
		return 0, false
	}
	for i, l := range items {
		if l == line {
			if i+1 < len(items) {
				return i + 1, true
			}
			return i, true // it was the last item — stay on it, nothing after
		}
	}
	return 0, false
}
