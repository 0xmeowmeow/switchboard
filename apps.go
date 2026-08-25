// apps.go — installed applications, discovered from .desktop files rather
// than dpkg's manually-installed list.
//
// The dpkg-based "apps" generator (system | apps in commands.conf) still
// works as a manual browse-and-add, but it filters on apt-mark showmanual,
// which marks hundreds of base-system packages "manual" too — coreutils,
// systemd, tzdata, none of them real applications. .desktop files are the
// signal wofi --show drun already uses for exactly this reason: NoDisplay
// and Hidden mark the entries that were never meant to appear in a
// launcher, and apt has no equivalent concept at all.
package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type desktopApp struct {
	name, exec, comment, categories string
}

// hasCategory checks the ;-delimited Categories= list for an exact token —
// a substring match would also catch "AudioVideoEditing" while looking for
// "Editing", which is not what any of these filters mean.
func hasCategory(categories, want string) bool {
	for _, c := range strings.Split(categories, ";") {
		if strings.EqualFold(strings.TrimSpace(c), want) {
			return true
		}
	}
	return false
}

func desktopDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{
		"/usr/share/applications",
		"/usr/local/share/applications",
		filepath.Join(home, ".local", "share", "applications"),
	}
}

// execFieldCode strips the placeholders a desktop environment would
// normally substitute with a file path (%f, %F, %u, %U) or fill in from
// the entry itself (%i, %c, %k) — meaningless typed straight into a shell.
var execFieldCode = regexp.MustCompile(`%[fFuUick]`)

// parseDesktopFile reads only the [Desktop Entry] section — a later
// [Desktop Action …] block is a submenu, not a second application.
func parseDesktopFile(path string) (desktopApp, bool) {
	f, err := os.Open(path)
	if err != nil {
		return desktopApp{}, false
	}
	defer f.Close()

	var a desktopApp
	inEntry := false
	noDisplay, hidden, isApp := false, false, true
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !inEntry && line == "[Desktop Entry]" {
				inEntry = true
				continue
			}
			if inEntry {
				break // left [Desktop Entry] for whatever comes next
			}
			continue
		}
		if !inEntry {
			continue
		}
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		key, val := line[:i], strings.TrimSpace(line[i+1:])
		switch key {
		case "Name": // the bare key only — Name[xx] is a translation
			a.name = val
		case "Exec":
			a.exec = strings.TrimSpace(execFieldCode.ReplaceAllString(val, ""))
		case "Comment":
			a.comment = val
		case "Categories":
			a.categories = val
		case "NoDisplay":
			noDisplay = val == "true"
		case "Hidden":
			hidden = val == "true"
		case "Type":
			isApp = val == "Application"
		}
	}
	// Steam stamps out a launcher entry per installed game — Steam already
	// has a library UI for those, so they'd just double the menu.
	isSteamGame := strings.Contains(a.exec, "steam steam://")
	// "Settings" is the standard freedesktop category for control-panel
	// screens (network config, IBus prefs, KDE Connect settings, …) —
	// real applications, just not what a launcher is for. Deliberately
	// narrower than excluding "System" outright, which would also take
	// Htop and GNOME System Monitor with it.
	isSettingsPanel := hasCategory(a.categories, "Settings")

	if a.name == "" || a.exec == "" || noDisplay || hidden || !isApp || isSteamGame || isSettingsPanel {
		return desktopApp{}, false
	}
	return a, true
}

func scanDesktopApps() []desktopApp {
	seen := map[string]bool{}
	var out []desktopApp
	for _, dir := range desktopDirs() {
		files, _ := filepath.Glob(filepath.Join(dir, "*.desktop"))
		for _, p := range files {
			a, ok := parseDesktopFile(p)
			if !ok || seen[strings.ToLower(a.name)] {
				continue
			}
			seen[strings.ToLower(a.name)] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

type appsAddedMsg struct {
	names []string
	err   error
}

// syncApps runs once at startup: anything with a .desktop file that isn't
// already in the menu gets appended under "apps", automatically. That's
// what makes it "live" — no picking, no sb-add, a fresh install just shows
// up the next time sb opens.
func syncApps() tea.Cmd {
	return func() tea.Msg {
		existing := map[string]bool{}
		if b, err := os.ReadFile(configPath()); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				f := splitFields(line)
				if len(f) >= 2 {
					existing[strings.ToLower(strings.TrimSpace(f[1]))] = true
				}
			}
		}

		var newOnes []desktopApp
		for _, a := range scanDesktopApps() {
			if !existing[strings.ToLower(a.name)] {
				newOnes = append(newOnes, a)
			}
		}
		if len(newOnes) == 0 {
			return appsAddedMsg{}
		}

		f, err := os.OpenFile(configPath(), os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return appsAddedMsg{err: err}
		}
		defer f.Close()

		var names []string
		for _, a := range newOnes {
			desc := a.comment
			if desc == "" {
				desc = a.exec
			}
			line := strings.Join([]string{
				"apps", escapePipes(a.name), escapePipes(desc), escapePipes(a.exec), "",
			}, " | ")
			if _, err := f.WriteString(line + "\n"); err != nil {
				return appsAddedMsg{err: err}
			}
			names = append(names, a.name)
		}
		return appsAddedMsg{names: names}
	}
}
