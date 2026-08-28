// watched.go — a manual watched/unwatched flag for generator list items.
//
// Deliberately not tied to movies/tv specifically: the key is
// "<level title>/<line>", which works for anything browsable through a
// generator (an episode under a show, a flat movie title, or whatever else
// grows this way later). Marking is explicit — press w — never inferred
// from having pressed play, since "I opened it" and "I watched it" are not
// the same thing.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func watchedPath() string { return filepath.Join(confDir(), "watched.json") }

func loadWatched() map[string]bool {
	w := map[string]bool{}
	b, err := os.ReadFile(watchedPath())
	if err != nil {
		return w
	}
	json.Unmarshal(b, &w)
	return w
}

func (m *model) saveWatched() {
	os.MkdirAll(confDir(), 0755)
	b, _ := json.MarshalIndent(m.watched, "", "  ")
	os.WriteFile(watchedPath(), b, 0644)
}

// watchedKey identifies a generator line stably enough to survive the list
// being rebuilt (a re-run generator, a renamed file elsewhere in the list)
// without colliding across different generators sharing a line's text.
func watchedKey(levelTitle, line string) string { return levelTitle + "/" + line }

func (m *model) toggleWatched(levelTitle, line string) {
	k := watchedKey(levelTitle, line)
	if m.watched[k] {
		delete(m.watched, k)
	} else {
		m.watched[k] = true
	}
	m.saveWatched()
}
