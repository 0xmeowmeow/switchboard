// switchboard — a launcher for the commands you forget you have.
//
// Layout: a group rail on the left, items on the right, a detail pane
// underneath, a status bar at the bottom. Everything is lipgloss.
//
// Config: ~/.config/switchboard/commands.conf
// Usage:  ~/.config/switchboard/usage.json   (written automatically)
//
//	format:  group | name | description | command | optional note
//	a literal pipe inside a command is escaped:  \|
//
// Generators. A command starting with "@gen" builds its list at runtime:
//
//	@gen LIST-COMMAND >> RUN-TEMPLATE
//
// {} is the whole selected line, {1}..{9} are its whitespace fields, and
// {^} is the line you selected one level up. If RUN-TEMPLATE itself starts
// with @gen, you descend another level — that is how show → episode works.
//
// Ordering is by how often you actually run things, not alphabetical.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"switchboard/decor"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------- data

type Cmd struct {
	Group string
	Name  string
	Desc  string
	Run   string
	Note  string
}

// decorMargin is how many columns of backdrop show either side.
const decorMargin = 4

// sidebarWidth is how much room the animated widget gets. Zero on a narrow
// terminal: the interface always wins over the decoration.
// contentWidth is how many columns the interface itself may use: the
// terminal, less the backdrop margins, less the sidebar. Every mode must
// derive from this — computing width from m.w directly is what made the
// widget overlap the study pane.
func (m model) contentWidth() int {
	w := m.w - 2*decorMargin - m.sidebarW()
	if w < 40 {
		w = 40
	}
	return w
}

// sidebarW is the live width, honouring the preference. The fonts browser
// always gets the full screen — 3722 fonts need every column they can get,
// and the widget has nothing to do with what you're looking at there.
func (m model) sidebarW() int {
	if !m.prefs.Widget || m.mode == modeFonts {
		return 0
	}
	return sidebarWidth(m.w)
}

func sidebarWidth(w int) int {
	switch {
	case w >= 150:
		return 40
	case w >= 120:
		return 30
	case w >= 104:
		return 22
	}
	return 0
}

func decorLoad() []decor.Art { return decor.LoadArt(60, 14) }

const genPrefix = "@gen"

func isGen(run string) bool {
	s := strings.TrimSpace(run)
	return s == genPrefix || strings.HasPrefix(s, genPrefix+" ")
}

// "@mode:NAME" switches to a built-in screen instead of running a shell
// command — how fonts (and, later, anything else internal) gets a normal,
// findable, describable menu entry instead of a key you have to already
// know. That's the whole point of switchboard: nothing is invisible.
const modePrefix = "@mode:"

func modeTarget(run string) (string, bool) {
	s := strings.TrimSpace(run)
	if strings.HasPrefix(s, modePrefix) {
		return strings.TrimSpace(strings.TrimPrefix(s, modePrefix)), true
	}
	return "", false
}

func (m model) enterMode(name string) (tea.Model, tea.Cmd) {
	switch name {
	case "fonts":
		if m.fonts == nil {
			m.fonts = newFontState()
			m.mode = modeFonts
			m.spin.Style = cCool
			return m, tea.Batch(loadFonts(), m.spin.Tick)
		}
		m.mode = modeFonts
		return m, nil
	}
	return m, nil
}

// genParts splits on the FIRST >>, which is what makes nesting work: the
// remainder keeps its own >> for the level below.
func genParts(run string) (list, tmpl string) {
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(run), genPrefix))
	body = strings.TrimSpace(body)
	if i := strings.Index(body, ">>"); i >= 0 {
		return strings.TrimSpace(body[:i]), strings.TrimSpace(body[i+2:])
	}
	return body, "{}"
}

// An "@exec" command runs WITHOUT quitting sb. Bubbletea hands the terminal
// to the child, waits, then takes it back — so you land in the example, poke
// at it, quit it, and you are back in the menu. Everything else still quits
// sb and hands off, which is what you want for a GUI or a shell you intend
// to stay in.
const execPrefix = "@exec"

func isExec(run string) bool {
	s := strings.TrimSpace(run)
	return strings.HasPrefix(s, execPrefix+" ")
}

func execBody(run string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(run), execPrefix))
}

// substitute fills the placeholders in a generator template:
//
//	{}        the whole selected line
//	{1}..{9}  its whitespace-separated fields
//	{^}       the whole line selected one level up
//	{^1}..{^9} that line's fields
//
// The numbered parent form matters whenever the level above shows more than
// the bare value — a project list with a note count, say. Without it, {^}
// drags the count into the path.
func substitute(tmpl, line, parent string) string {
	pf := strings.Fields(parent)
	out := tmpl
	for i := 1; i <= 9; i++ {
		v := ""
		if i-1 < len(pf) {
			v = pf[i-1]
		}
		out = strings.ReplaceAll(out, fmt.Sprintf("{^%d}", i), v)
	}
	out = strings.ReplaceAll(out, "{^}", parent)
	out = strings.ReplaceAll(out, "{}", line)
	fields := strings.Fields(line)
	for i := 1; i <= 9; i++ {
		v := ""
		if i-1 < len(fields) {
			v = fields[i-1]
		}
		out = strings.ReplaceAll(out, fmt.Sprintf("{%d}", i), v)
	}
	return out
}

const configTemplate = `# switchboard — your commands, described.
#
# format:   group | name | description | command | optional note
# a literal pipe inside a command must be escaped:  \|
#
# @gen LIST >> TEMPLATE   builds the list when you open it.
#   {} whole line, {1}..{9} fields, {^} the line chosen one level up.
#   if TEMPLATE starts with @gen you descend another level.

ai | models | pick a model and chat | @gen ollama list \| tail -n +2 >> ollama run {1} | live list, never stale
`

func confDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "switchboard")
}
func configPath() string { return filepath.Join(confDir(), "commands.conf") }
func usagePath() string  { return filepath.Join(confDir(), "usage.json") }

func splitFields(line string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(line[i])
	}
	return append(out, strings.TrimSpace(cur.String()))
}

func escapePipes(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// ---------------------------------------------------------------- usage

type usageMap map[string]int

func key(c Cmd) string { return c.Group + "/" + c.Name }

func loadUsage() usageMap {
	u := usageMap{}
	if b, err := os.ReadFile(usagePath()); err == nil {
		json.Unmarshal(b, &u)
	}
	return u
}

func (u usageMap) bump(c Cmd) {
	u[key(c)]++
	os.MkdirAll(confDir(), 0755)
	if b, err := json.MarshalIndent(u, "", "  "); err == nil {
		os.WriteFile(usagePath(), b, 0644)
	}
}

// sortCmds orders by what you actually use. Groups float up by their
// most-used member; inside a group, most-used first. Ties are alphabetical
// so a fresh install still looks sane.
func sortCmds(cmds []Cmd, u usageMap) {
	groupMax := map[string]int{}
	for _, c := range cmds {
		if n := u[key(c)]; n > groupMax[c.Group] {
			groupMax[c.Group] = n
		}
	}
	sort.SliceStable(cmds, func(i, j int) bool {
		a, b := cmds[i], cmds[j]
		if a.Group != b.Group {
			if groupMax[a.Group] != groupMax[b.Group] {
				return groupMax[a.Group] > groupMax[b.Group]
			}
			return a.Group < b.Group
		}
		if ua, ub := u[key(a)], u[key(b)]; ua != ub {
			return ua > ub
		}
		return a.Name < b.Name
	})
}

func loadCommands(u usageMap) ([]Cmd, error) {
	p := configPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		os.MkdirAll(confDir(), 0755)
		os.WriteFile(p, []byte(configTemplate), 0644)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Cmd
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := splitFields(line)
		if len(parts) < 4 {
			continue
		}
		c := Cmd{Group: parts[0], Name: parts[1], Desc: parts[2], Run: parts[3]}
		if len(parts) > 4 {
			c.Note = parts[4]
		}
		out = append(out, c)
	}
	sortCmds(out, u)
	return out, sc.Err()
}

func saveCommands(cmds []Cmd) error {
	var b strings.Builder
	b.WriteString("# switchboard — your commands, described.\n")
	b.WriteString("# format:   group | name | description | command | optional note\n")
	b.WriteString(`# a literal pipe inside a command must be escaped:  \|` + "\n\n")
	for _, c := range cmds {
		b.WriteString(fmt.Sprintf("%s | %s | %s | %s | %s\n",
			escapePipes(c.Group), escapePipes(c.Name), escapePipes(c.Desc),
			escapePipes(c.Run), escapePipes(c.Note)))
	}
	return os.WriteFile(configPath(), []byte(b.String()), 0644)
}

// ---------------------------------------------------------------- shell

func shellPath() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// interactiveArgs is ONLY safe once the TUI has exited and sb is back in the
// foreground process group. An interactive zsh does job control: it calls
// tcsetpgrp and expects to own the terminal. Start one while Bubbletea holds
// the tty and the kernel sends SIGTTIN and the whole job suspends.
//
// plainArgs is for anything run *underneath* the TUI. No job control, no
// .zshrc. Aliases will not resolve there, which is the price.
func interactiveArgs(cmd string) []string { return []string{"-i", "-c", cmd} }
func plainArgs(cmd string) []string       { return []string{"-c", cmd} }

// capture runs a command with no controlling terminal, in its own process
// group, under a deadline. Nothing it does can steal the tty or hang the UI.
func capture(cmdStr string, d time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	c := exec.CommandContext(ctx, shellPath(), plainArgs(cmdStr)...)
	if devnull, err := os.Open(os.DevNull); err == nil {
		defer devnull.Close()
		c.Stdin = devnull
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the whole group, not just the shell: a forked grandchild keeps the
	// stdout pipe open, so killing the leader alone leaves Output() blocked.
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	c.WaitDelay = 2 * time.Second
	return c.Output()
}

type execDoneMsg struct {
	label string
	err   error
}

// runExec releases the terminal, runs the command, and restores the TUI.
// The shell is interactive here for the same reason it is at hand-off time:
// bubbletea has genuinely given the terminal back, sb is still the foreground
// process group, so zsh's job control initialises cleanly and your aliases
// and functions resolve.
func runExec(label, cmdStr string) tea.Cmd {
	c := exec.Command(shellPath(), interactiveArgs(cmdStr)...)
	c.Env = append(os.Environ(), "SB_CHILD=1")
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return execDoneMsg{label: label, err: err}
	})
}

type genResultMsg struct {
	items []string
	err   error
}

func runGenerator(listCmd string) tea.Cmd {
	return func() tea.Msg {
		out, err := capture(listCmd, 20*time.Second)
		if err != nil && len(out) == 0 {
			return genResultMsg{err: err}
		}
		var items []string
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimRight(l, " \t\r"); strings.TrimSpace(l) != "" {
				items = append(items, l)
			}
		}
		return genResultMsg{items: items}
	}
}

// ---------------------------------------------------------------- style

const (
)

var (
	// Styles are rebuilt by applyTheme, never assigned at declaration —
	// that is what makes live theme switching possible.
	cBase, cDim, cFant, cCool, cWarn, cPurp lipgloss.Style
	paneOn, paneOff                         lipgloss.Style
	selOn, selOff                           lipgloss.Style
	stTag, stMid, stEnd                     lipgloss.Style
	cDeco, cCanvas                          lipgloss.Style

	gradFrom, gradTo [3]int

	// index into themes; the whole UI reads its colours through applyTheme
	curTheme int
)

// Palette is a theme. Six colours plus two neutrals is enough for the whole
// UI; anything more and themes stop being writable by hand.
type Palette struct {
	Name   string
	Accent string // focus borders, item names, the gradient start
	Second string // group headings, generator marks, the gradient end
	Hot    string // status tag, filter prompt
	Ink    string // body text
	Dim    string // descriptions
	Faint  string // unfocused borders, inert marks
	Warn   string // notes and errors
	Dark   string // text drawn ON an accent background
	Bar    string // status bar middle background
	Bg     string // the app's own background — sb stops showing your terminal's
	Glyph  string // space-separated glyphs tiled faintly behind the panes
	Deco   string // colour of that backdrop; keep it close to Bg
}

var themes = []Palette{
	{Name: "cyberpunk", Accent: "#00f0ff", Second: "#bf00ff", Hot: "#ff2f92",
		Bg: "#07070d", Glyph: "░ ▒ ░ ▓", Deco: "#15152b",
		Ink: "#d8d8e8", Dim: "#5a5a72", Faint: "#3a3a4c", Warn: "#ffb020",
		Dark: "#0a0a0f", Bar: "#1c1c28"},
	// Derived from the 80's Neon TV Obsidian theme: --accent-1 magenta,
	// --accent-2 cyan, with its Dracula-ish muted set for the quiet tones.
	{Name: "neon-tv", Accent: "#00FFFF", Second: "#FF00FF", Hot: "#FF1690",
		Bg: "#0d0a18", Glyph: "猫 咪", Deco: "#241a3d",
		Ink: "#f8f8f2", Dim: "#7a6ae6", Faint: "#44406a", Warn: "#ffd319",
		Dark: "#12101f", Bar: "#1b1730"},
	{Name: "vapor", Accent: "#FF6EC7", Second: "#8be9fd", Hot: "#ffd319",
		Bg: "#0f0b16", Glyph: "◢ ◣ ◥ ◤", Deco: "#2a1f3a",
		Ink: "#f2e9f7", Dim: "#8a7fa8", Faint: "#4a4260", Warn: "#ffb86c",
		Dark: "#14101c", Bar: "#221a2e"},
	{Name: "matrix", Accent: "#00FF00", Second: "#50fa7b", Hot: "#8be9fd",
		Bg: "#000d06", Glyph: "ｱ ｲ ｳ ｴ ｵ ﾊ ﾋ ﾌ", Deco: "#0d2b18",
		Ink: "#c8f7c8", Dim: "#3f7a3f", Faint: "#264d26", Warn: "#ffd319",
		Dark: "#00140a", Bar: "#0a1f12"},
	{Name: "amber", Accent: "#ffb000", Second: "#ff7000", Hot: "#ffd319",
		Bg: "#0f0900", Glyph: "· ˙ ·", Deco: "#2e2008",
		Ink: "#ffd9a0", Dim: "#8a6320", Faint: "#4a3510", Warn: "#ff5555",
		Dark: "#140c00", Bar: "#241a08"},
	{Name: "mono", Accent: "#e8e8e8", Second: "#a0a0a0", Hot: "#ffffff",
		Bg: "#070707", Glyph: "▚ ▞", Deco: "#1a1a1a",
		Ink: "#d0d0d0", Dim: "#6a6a6a", Faint: "#3a3a3a", Warn: "#c0c0c0",
		Dark: "#0a0a0a", Bar: "#1a1a1a"},
}

func hex3(h string) [3]int {
	var r, g, b int
	fmt.Sscanf(strings.TrimPrefix(h, "#"), "%02x%02x%02x", &r, &g, &b)
	return [3]int{r, g, b}
}

func fg(c string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
}

func applyTheme(p Palette) {
	cBase = fg(p.Ink)
	cDim = fg(p.Dim)
	cFant = fg(p.Faint)
	cCool = fg(p.Accent)
	cWarn = fg(p.Warn)
	cPurp = fg(p.Second)

	// BorderForeground colours the border glyphs; without a matching
	// BorderBackground lipgloss renders them with NO background at all
	// (an explicit "reset to default" SGR, not "inherit") — which is how
	// the terminal's own wallpaper was showing through every box edge.
	paneOn = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(p.Accent)).
		BorderBackground(lipgloss.Color(p.Bg)).Padding(0, 1)
	paneOff = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(p.Faint)).
		BorderBackground(lipgloss.Color(p.Bg)).Padding(0, 1)

	selOn = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Dark)).
		Background(lipgloss.Color(p.Accent)).Bold(true)
	selOff = fg(p.Accent).Bold(true)

	stTag = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Dark)).
		Background(lipgloss.Color(p.Hot)).Bold(true).Padding(0, 1)
	stMid = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Ink)).
		Background(lipgloss.Color(p.Bar)).Padding(0, 1)
	stEnd = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Dark)).
		Background(lipgloss.Color(p.Second)).Bold(true).Padding(0, 1)

	cDeco = lipgloss.NewStyle().Foreground(lipgloss.Color(p.Deco)).
		Background(lipgloss.Color(p.Bg))
	cCanvas = lipgloss.NewStyle().Background(lipgloss.Color(p.Bg))

	// every pane and label sits ON the app background, not the terminal's
	for _, st := range []*lipgloss.Style{&cBase, &cDim, &cFant, &cCool, &cWarn,
		&cPurp, &paneOn, &paneOff, &selOff} {
		*st = st.Background(lipgloss.Color(p.Bg))
	}

	gradFrom, gradTo = hex3(p.Accent), hex3(p.Second)
	saveThemeJSON(p)
}

// saveThemeJSON publishes the active palette so other tools — a Textual app,
// a script, anything — can wear the same colours. This is what makes separate
// binaries feel like one environment; a shared codebase is not required.
func saveThemeJSON(p Palette) {
	os.MkdirAll(confDir(), 0755)
	if b, err := json.MarshalIndent(p, "", "  "); err == nil {
		os.WriteFile(filepath.Join(confDir(), "theme.json"), b, 0644)
	}
}

func themePath() string { return filepath.Join(confDir(), "theme") }

// loadTheme reads the saved theme name. A user theme file at
// ~/.config/switchboard/themes/<name>.conf overrides a built-in of the same
// name; the format is one "key = #rrggbb" per line.
func loadTheme() int {
	b, err := os.ReadFile(themePath())
	if err != nil {
		return 0
	}
	want := strings.TrimSpace(string(b))
	for i, p := range themes {
		if p.Name == want {
			return i
		}
	}
	return 0
}

func saveTheme(name string) {
	os.MkdirAll(confDir(), 0755)
	os.WriteFile(themePath(), []byte(name+"\n"), 0644)
}

// loadUserThemes merges ~/.config/switchboard/themes/*.conf over the
// built-ins, so a hand-written theme appears in the same cycle.
func loadUserThemes() {
	dir := filepath.Join(confDir(), "themes")
	files, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		p := Palette{Name: strings.TrimSuffix(filepath.Base(f), ".conf")}
		base := themes[0]
		p.Accent, p.Second, p.Hot = base.Accent, base.Second, base.Hot
		p.Ink, p.Dim, p.Faint = base.Ink, base.Dim, base.Faint
		p.Warn, p.Dark, p.Bar = base.Warn, base.Dark, base.Bar
		p.Bg, p.Glyph, p.Deco = base.Bg, base.Glyph, base.Deco
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") && !strings.Contains(line, "=") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
			switch k {
			case "accent":
				p.Accent = v
			case "second":
				p.Second = v
			case "hot":
				p.Hot = v
			case "ink":
				p.Ink = v
			case "dim":
				p.Dim = v
			case "faint":
				p.Faint = v
			case "warn":
				p.Warn = v
			case "dark":
				p.Dark = v
			case "bar":
				p.Bar = v
			case "bg":
				p.Bg = v
			case "glyph", "glyphs":
				p.Glyph = v
			case "deco":
				p.Deco = v
			}
		}
		replaced := false
		for i := range themes {
			if themes[i].Name == p.Name {
				themes[i] = p
				replaced = true
			}
		}
		if !replaced {
			themes = append(themes, p)
		}
	}
}

func lerp(a, b [3]int, t float64) lipgloss.Color {
	f := func(i int) int { return a[i] + int(float64(b[i]-a[i])*t) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", f(0), f(1), f(2)))
}

func gradient(s string, bold bool) string {
	r := []rune(s)
	var b strings.Builder
	for i, ch := range r {
		t := 0.0
		if len(r) > 1 {
			t = float64(i) / float64(len(r)-1)
		}
		st := lipgloss.NewStyle().Foreground(lerp(gradFrom, gradTo, t)).
			Background(lipgloss.Color(themes[curTheme].Bg))
		if bold {
			st = st.Bold(true)
		}
		b.WriteString(st.Render(string(ch)))
	}
	return b.String()
}

// customBanner is resolved ONCE, at startup. Resolving it per frame meant a
// complete shell startup per rendered keystroke.
// customBanner renders the header once, at startup. Two sources, in order:
// a TheDraw font named in banner.conf, or SB_BANNER shelling out for anyone
// who wants something else. The font path is preferred because its gaps are
// painted; raw command output is not, so the backdrop shows through it.
func customBanner() string {
	c := loadBannerConf()
	if f := findFont(c.font); f != nil {
		if lines := bannerLines(f, c.text); len(lines) > 0 {
			return strings.Join(lines, "\n")
		}
	}
	cmd := os.Getenv("SB_BANNER")
	if cmd == "" {
		return ""
	}
	out, err := capture(cmd, 3*time.Second)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// paneTitle is a heading plus an underline rule, drawn inside the pane.
func paneTitle(s string, w int, focused bool) string {
	t := strings.ToUpper(s)
	if len(t) > w {
		t = t[:w]
	}
	style := cDim
	if focused {
		style = cCool.Bold(true)
	}
	rule := strings.Repeat("─", maxi(0, w))
	return style.Render(t) + "\n" + cFant.Render(rule)
}

// pad forces a string to exactly n visible columns.
func pad(s string, n int) string {
	w := lipgloss.Width(s)
	if w > n {
		return truncate(s, n)
	}
	return s + strings.Repeat(" ", n-w)
}

// ---------------------------------------------------------------- model

type mode int

const (
	modeList mode = iota
	modeFilter
	modeAdd
	modeConfirmDelete
	modeGen
	modeStudy
	modeFonts
)

type focus int

const (
	focusGroups focus = iota
	focusItems
)

type genLevel struct {
	title  string
	tmpl   string // raw, not yet substituted
	parent string // the line chosen one level up  ({^})
	items  []string
	shown  []int
	cursor int
}

const allGroups = "all"

type model struct {
	cmds  []Cmd
	usage usageMap

	groups     []string // "all" first, then real groups in usage order
	groupIdx   int
	items      []int // indices into cmds, for the current group + filter
	itemIdx    int
	focus      focus
	filterText string

	mode      mode
	filter    textinput.Model
	addField  int
	addBuf    [5]textinput.Model
	status    string
	quitting  bool
	chosen    *Cmd
	h, w      int
	bannerTxt string

	stack   []genLevel
	genLoad bool
	spin    spinner.Model
	art     []decor.Art
	frame   int
	widget  int     // which one this session got
	prefs   prefs
	prefSel int
	prefOpen bool
	clock   float64 // seconds since start, drives every animation

	// study mode: a reader/tracker over the vault's markdown notes
	study          *studyState
	diagram        *diagramState
	fonts          *fontState
	fontsEditing   bool

	watched map[string]bool // "<level title>/<line>" -> seen, in any generator
}

func newInput(placeholder, prompt string, limit int) textinput.Model {
	t := textinput.New()
	t.Placeholder = placeholder
	t.Prompt = prompt
	t.CharLimit = limit
	t.PromptStyle = fg(themes[curTheme].Hot)
	return t
}

func initialModel() model {
	loadUserThemes()
	curTheme = loadTheme()
	applyTheme(themes[curTheme])
	u := loadUsage()
	cmds, err := loadCommands(u)
	m := model{cmds: cmds, usage: u, bannerTxt: customBanner(), w: 80, h: 24}
	if err != nil {
		m.status = "could not read config: " + err.Error()
	}
	m.prefs = loadPrefs()
	m.prefs.applyFontSize()
	m.art = decorLoad()
	m.watched = loadWatched()
	// a different widget on every launch, seeded by the clock
	if m.prefs.WidgetPick >= 0 {
		m.widget = m.prefs.WidgetPick % totalWidgets()
	} else {
		m.widget = int(time.Now().UnixNano()/1e6) % totalWidgets()
	}
	m.spin = spinner.New()
	m.spin.Spinner = spinner.Dot
	m.filter = newInput("filter", "  / ", 40)

	labels := []string{"group", "name", "description", "command", "note (optional)"}
	for i := range m.addBuf {
		m.addBuf[i] = newInput(labels[i], fmt.Sprintf("  %-16s ", labels[i]+":"), 200)
	}
	m.rebuildGroups()
	m.rebuildItems()
	return m
}

func (m *model) rebuildGroups() {
	seen := map[string]bool{}
	m.groups = []string{allGroups}
	for _, c := range m.cmds { // cmds are already in usage order
		if !seen[c.Group] {
			seen[c.Group] = true
			m.groups = append(m.groups, c.Group)
		}
	}
	if m.groupIdx >= len(m.groups) {
		m.groupIdx = 0
	}
}

func (m *model) matches(c Cmd) bool {
	q := strings.ToLower(m.filterText)
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(c.Name), q) ||
		strings.Contains(strings.ToLower(c.Desc), q) ||
		strings.Contains(strings.ToLower(c.Group), q) ||
		strings.Contains(strings.ToLower(c.Note), q)
}

func (m *model) rebuildItems() {
	g := m.groups[m.groupIdx]
	m.items = m.items[:0]
	for i, c := range m.cmds {
		if (g == allGroups || c.Group == g) && m.matches(c) {
			m.items = append(m.items, i)
		}
	}
	if m.itemIdx >= len(m.items) {
		m.itemIdx = maxi(0, len(m.items)-1)
	}
}

func (m *model) current() (Cmd, bool) {
	if len(m.items) == 0 {
		return Cmd{}, false
	}
	return m.cmds[m.items[m.itemIdx]], true
}

func (m *model) top() *genLevel {
	if len(m.stack) == 0 {
		return nil
	}
	return &m.stack[len(m.stack)-1]
}

func (m *model) regenFilter() {
	lv := m.top()
	if lv == nil {
		return
	}
	q := strings.ToLower(m.filterText)
	lv.shown = lv.shown[:0]
	for i, s := range lv.items {
		if q == "" || strings.Contains(strings.ToLower(s), q) {
			lv.shown = append(lv.shown, i)
		}
	}
	if lv.cursor >= len(lv.shown) {
		lv.cursor = maxi(0, len(lv.shown)-1)
	}
}

func (m *model) push(title, listCmd, tmpl, parent string) tea.Cmd {
	m.stack = append(m.stack, genLevel{title: title, tmpl: tmpl, parent: parent})
	m.genLoad = true
	m.mode = modeGen
	m.filterText = ""
	m.filter.SetValue("")
	m.status = ""
	m.spin.Style = cCool
	return tea.Batch(runGenerator(listCmd), m.spin.Tick)
}

func (m *model) pop() {
	if len(m.stack) > 0 {
		m.stack = m.stack[:len(m.stack)-1]
	}
	m.filterText = ""
	m.filter.SetValue("")
	if len(m.stack) == 0 {
		m.mode = modeList
		m.rebuildItems()
	} else {
		m.regenFilter()
	}
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// decorTick drives ASCII animation in the margin. Slow on purpose: this is
// decoration, and a fast repaint of the whole canvas is not free.
type decorTickMsg struct{}

func decorTick() tea.Cmd {
	return tea.Tick(180*time.Millisecond, func(time.Time) tea.Msg {
		return decorTickMsg{}
	})
}

func (m model) hasAnimation() bool {
	for _, a := range m.art {
		if a.Animated() {
			return true
		}
	}
	return false
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, decorTick(), syncApps())
}

// ---------------------------------------------------------------- update

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case appsAddedMsg:
		switch {
		case msg.err != nil:
			m.status = "apps sync: " + msg.err.Error()
		case len(msg.names) > 0:
			cmds, err := loadCommands(m.usage)
			if err == nil {
				m.cmds = cmds
				m.rebuildGroups()
				m.rebuildItems()
			}
			plural := ""
			if len(msg.names) != 1 {
				plural = "s"
			}
			m.status = fmt.Sprintf("+%d new app%s: %s", len(msg.names), plural, strings.Join(msg.names, ", "))
		}
		return m, nil

	case fontsLoadedMsg:
		if m.fonts != nil {
			m.fonts.loading = false
			if msg.err != nil {
				m.fonts.err = msg.err.Error()
			} else {
				m.fonts.all = msg.entries
				m.fonts.groups = buildGroups(msg.entries)
				m.fonts.refilter("")
			}
		}
		return m, nil

	case decorTickMsg:
		m.frame++
		m.clock += 0.18 * m.prefs.WidgetSpeed
		return m, decorTick()

	case spinner.TickMsg:
		if m.fonts != nil && m.fonts.loading {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
		if !m.genLoad {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case execDoneMsg:
		if msg.err != nil {
			m.status = msg.label + ": " + msg.err.Error()
		} else {
			m.status = "ran " + msg.label
		}
		return m, nil

	case genResultMsg:
		m.genLoad = false
		lv := m.top()
		if msg.err != nil || lv == nil {
			name := "generator"
			if lv != nil {
				name = lv.title
			}
			m.pop()
			m.status = name + ": " + errText(msg.err)
			return m, nil
		}
		lv.items = msg.items
		lv.cursor = 0
		m.regenFilter()
		if len(lv.items) == 0 {
			name := lv.title
			m.pop()
			m.status = name + ": nothing came back"
		}
		return m, nil

	case tea.KeyMsg:
		// preferences float over whatever mode is underneath, so they are
		// handled before the mode switch rather than inside every mode
		if m.diagram != nil {
			switch msg.String() {
			case "esc", "q", "d", "ctrl+c":
				m.diagram = nil
			case "h", "left":
				m.diagram.adjust(-1)
			case "l", "right":
				m.diagram.adjust(+1)
			case "j", "down":
				m.diagram.sel = mini(len(m.diagram.d.Params)-1, m.diagram.sel+1)
			case "k", "up":
				m.diagram.sel = maxi(0, m.diagram.sel-1)
			}
			return m, nil
		}
		if m.prefOpen {
			return m.updatePrefs(msg)
		}
		switch msg.String() {
		case ",":
			m.prefOpen = true
			return m, nil
		case "ctrl+w":
			m.prefs.Widget = !m.prefs.Widget
			m.prefs.save()
			return m, nil
		case "ctrl+n":
			m.widget = (m.widget + 1) % totalWidgets()
			m.prefs.WidgetPick = m.widget
			m.prefs.save()
			return m, nil
		}
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modeAdd:
			return m.updateAdd(msg)
		case modeConfirmDelete:
			return m.updateConfirm(msg)
		case modeGen:
			return m.updateGen(msg)
		case modeStudy:
			return m.updateStudy(msg)
		case modeFonts:
			return m.updateFonts(msg)
		default:
			return m.updateList(msg)
		}
	}
	return m, nil
}

// filter mode is explicit, which is what frees j/k/h/l for navigation.
func (m model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filterText = ""
		m.filter.SetValue("")
		m.filter.Blur()
		if m.fontsEditing {
			m.fontsEditing = false
			m.mode = modeFonts
			return m, nil
		}
		m.mode = modeList
		if len(m.stack) > 0 {
			m.mode = modeGen
			m.regenFilter()
		} else {
			m.rebuildItems()
		}
		return m, nil
	case "enter":
		m.filter.Blur()
		if m.fontsEditing && m.fonts != nil {
			m.fonts.text = m.filter.Value()
			m.fonts.refilter("")
			m.fontsEditing = false
			m.mode = modeFonts
			return m, nil
		}
		m.mode = modeList
		if len(m.stack) > 0 {
			m.mode = modeGen
		} else {
			m.focus = focusItems
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.filterText = m.filter.Value()
	if len(m.stack) > 0 {
		m.regenFilter()
	} else {
		m.rebuildItems()
	}
	return m, cmd
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.quitting = true
		return m, tea.Quit

	case "/":
		m.mode = modeFilter
		m.filter.Focus()
		m.status = ""
		return m, textinput.Blink

	case "tab":
		m.focus = 1 - m.focus
		return m, nil

	case "h", "left":
		m.focus = focusGroups
		return m, nil

	case "l", "right":
		if m.focus == focusGroups {
			m.focus = focusItems
			return m, nil
		}
		if c, ok := m.current(); ok && isGen(c.Run) {
			list, tmpl := genParts(c.Run)
			m.usage.bump(c)
			return m, m.push(c.Name, list, tmpl, "")
		}
		return m, nil

	case "k", "up":
		if m.focus == focusGroups {
			if m.groupIdx > 0 {
				m.groupIdx--
				m.itemIdx = 0
				m.rebuildItems()
			}
		} else if m.itemIdx > 0 {
			m.itemIdx--
		}
		return m, nil

	case "j", "down":
		if m.focus == focusGroups {
			if m.groupIdx < len(m.groups)-1 {
				m.groupIdx++
				m.itemIdx = 0
				m.rebuildItems()
			}
		} else if m.itemIdx < len(m.items)-1 {
			m.itemIdx++
		}
		return m, nil

	case "g", "home":
		if m.focus == focusGroups {
			m.groupIdx, m.itemIdx = 0, 0
			m.rebuildItems()
		} else {
			m.itemIdx = 0
		}
		return m, nil

	case "G", "end":
		if m.focus == focusGroups {
			m.groupIdx = len(m.groups) - 1
			m.itemIdx = 0
			m.rebuildItems()
		} else {
			m.itemIdx = maxi(0, len(m.items)-1)
		}
		return m, nil

	case "enter":
		if m.focus == focusGroups {
			m.focus = focusItems
			return m, nil
		}
		c, ok := m.current()
		if !ok {
			return m, nil
		}
		if name, ok := modeTarget(c.Run); ok {
			m.usage.bump(c)
			return m.enterMode(name)
		}
		if isGen(c.Run) {
			list, tmpl := genParts(c.Run)
			m.usage.bump(c)
			return m, m.push(c.Name, list, tmpl, "")
		}
		m.usage.bump(c)
		if isExec(c.Run) {
			return m, runExec(c.Name, execBody(c.Run))
		}
		m.chosen = &c
		m.quitting = true
		return m, tea.Quit

	case "t", "T":
		if msg.String() == "t" {
			curTheme = (curTheme + 1) % len(themes)
		} else {
			curTheme = (curTheme - 1 + len(themes)) % len(themes)
		}
		applyTheme(themes[curTheme])
		m.bannerTxt = customBanner()
		m.filter.PromptStyle = fg(themes[curTheme].Hot)
		saveTheme(themes[curTheme].Name)
		m.status = "theme: " + themes[curTheme].Name
		return m, nil

	case "S":
		if m.study == nil {
			m.study = newStudyState()
		}
		m.mode = modeStudy
		m.status = ""
		return m, nil

	case "a":
		m.mode = modeAdd
		m.addField = 0
		m.addBuf[0].Focus()
		m.status = ""
		return m, textinput.Blink

	case "d":
		if _, ok := m.current(); ok {
			m.mode = modeConfirmDelete
		}
		return m, nil

	case "e":
		m.chosen = &Cmd{Name: "__edit__", Run: "${EDITOR:-nano} " + configPath()}
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m model) updateGen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	lv := m.top()
	switch msg.String() {
	case "esc", "ctrl+c", "h", "left", "q":
		m.pop()
		return m, nil
	case "/":
		m.mode = modeFilter
		m.filter.Focus()
		return m, textinput.Blink
	case "k", "up":
		if lv != nil && lv.cursor > 0 {
			lv.cursor--
		}
		return m, nil
	case "j", "down":
		if lv != nil && lv.cursor < len(lv.shown)-1 {
			lv.cursor++
		}
		return m, nil
	case "g", "home":
		if lv != nil {
			lv.cursor = 0
		}
		return m, nil
	case "G", "end":
		if lv != nil {
			lv.cursor = maxi(0, len(lv.shown)-1)
		}
		return m, nil
	case "w":
		if lv != nil && len(lv.shown) > 0 {
			m.toggleWatched(lv.title, lv.items[lv.shown[lv.cursor]])
		}
		return m, nil
	case "enter", "l", "right":
		if lv == nil || len(lv.shown) == 0 {
			return m, nil
		}
		line := lv.items[lv.shown[lv.cursor]]
		if isGen(lv.tmpl) { // a template that is itself a generator descends
			inList, inTmpl := genParts(lv.tmpl)
			return m, m.push(line, substitute(inList, line, lv.parent), inTmpl, line)
		}
		if msg.String() != "enter" {
			return m, nil
		}
		resolved := substitute(lv.tmpl, line, lv.parent)
		if isExec(resolved) {
			return m, runExec(truncate(line, 40), execBody(resolved))
		}
		c := Cmd{Name: lv.title, Run: resolved}
		m.chosen = &c
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m model) updateAdd(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		for i := range m.addBuf {
			m.addBuf[i].Blur()
		}
		m.mode = modeList
		m.status = ""
		return m, nil
	case "tab", "down":
		m.addBuf[m.addField].Blur()
		m.addField = (m.addField + 1) % len(m.addBuf)
		m.addBuf[m.addField].Focus()
		return m, nil
	case "shift+tab", "up":
		m.addBuf[m.addField].Blur()
		m.addField = (m.addField - 1 + len(m.addBuf)) % len(m.addBuf)
		m.addBuf[m.addField].Focus()
		return m, nil
	case "enter":
		g := strings.TrimSpace(m.addBuf[0].Value())
		n := strings.TrimSpace(m.addBuf[1].Value())
		r := strings.TrimSpace(m.addBuf[3].Value())
		if g == "" || n == "" || r == "" {
			m.status = "group, name and command are required"
			return m, nil
		}
		m.cmds = append(m.cmds, Cmd{
			Group: g, Name: n,
			Desc: strings.TrimSpace(m.addBuf[2].Value()),
			Run:  r,
			Note: strings.TrimSpace(m.addBuf[4].Value()),
		})
		sortCmds(m.cmds, m.usage)
		if err := saveCommands(m.cmds); err != nil {
			m.status = "save failed: " + err.Error()
		} else {
			m.status = "added " + n
		}
		for i := range m.addBuf {
			m.addBuf[i].SetValue("")
			m.addBuf[i].Blur()
		}
		m.addField = 0
		m.mode = modeList
		m.rebuildGroups()
		m.rebuildItems()
		return m, nil
	}
	var cmd tea.Cmd
	m.addBuf[m.addField], cmd = m.addBuf[m.addField].Update(msg)
	return m, cmd
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if s := msg.String(); s == "y" || s == "Y" {
		if len(m.items) > 0 {
			idx := m.items[m.itemIdx]
			name := m.cmds[idx].Name
			m.cmds = append(m.cmds[:idx], m.cmds[idx+1:]...)
			if err := saveCommands(m.cmds); err != nil {
				m.status = "save failed: " + err.Error()
			} else {
				m.status = "deleted " + name
			}
			m.rebuildGroups()
			m.rebuildItems()
		}
	} else {
		m.status = ""
	}
	m.mode = modeList
	return m, nil
}

func errText(err error) string {
	if err == nil {
		return "nothing came back"
	}
	return err.Error()
}

// ---------------------------------------------------------------- view

// canvas paints the app's own background, tiles the theme's glyph pattern
// faintly across it, and composites the interface on top. Gaps in the
// interface show the pattern rather than the terminal underneath.
func (m model) canvas(content string) string {
	w, h := m.w, m.h
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	p := themes[curTheme]
	glyphs := strings.Fields(p.Glyph)
	lines := strings.Split(content, "\n")

	if len(glyphs) == 0 {
		// no pattern: still paint the background so we do not show through
		out := make([]string, 0, len(lines))
		for _, l := range lines {
			pad := w - lipgloss.Width(l)
			if pad < 0 {
				pad = 0
			}
			out = append(out, cCanvas.Render(l+strings.Repeat(" ", pad)))
		}
		return strings.Join(out, "\n")
	}

	bg := decor.Tile(glyphs, w, h, lipgloss.Width)
	for i := range bg {
		bg[i] = cDeco.Render(bg[i])
	}
	// centre the interface horizontally, leaving pattern down both sides
	cw := 0
	for _, l := range lines {
		if x := lipgloss.Width(l); x > cw {
			cw = x
		}
	}
	x := (w - m.sidebarW() - cw) / 2
	if x < 0 {
		x = 0
	}
	y := 1
	if len(lines)+y > h {
		y = 0
	}
	out := decor.Overlay(bg, lines, x, y, lipgloss.Width)

	// the animated widget lives in the right-hand sidebar, if there is one
	if sw := m.sidebarW(); sw > 8 {
		sh := h - 2
		if sh > 4 {
			lines, name := sidebarFrame(m.widget%totalWidgets(),
				sw, sh, m.frame, m.clock*m.prefs.WidgetScale, p.Bg)
			lines = append(lines, cDim.Render(pad(" "+name, sw)))
			out = decor.Overlay(out, lines, w-sw-1, 1, lipgloss.Width)
		}
	}

	// a piece of art from ~/.config/switchboard/decor/, bottom-left of the
	// margin if it fits. Purely optional: no files, nothing drawn.
	if a := decor.PickArt(m.art, curTheme); a != nil {
		frame := a.Frame(m.frame)
		aw := 0
		for _, l := range frame {
			if v := lipgloss.Width(l); v > aw {
				aw = v
			}
		}
		ay := h - len(frame) - 1
		if aw <= x-2 && ay > y {
			art := make([]string, len(frame))
			for i, l := range frame {
				if a.Colour {
					art[i] = l
				} else {
					art[i] = cDeco.Render(l)
				}
			}
			out = decor.Overlay(out, art, 1, ay, lipgloss.Width)
		}
	}
	return strings.Join(out, "\n")
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	if m.diagram != nil {
		return m.canvas(overlayModal(m.baseView(), m.viewDiagram()))
	}
	if m.prefOpen {
		return m.canvas(overlayModal(m.baseView(), m.viewPrefs()))
	}
	return m.canvas(m.baseView())
}

func (m model) baseView() string {
	switch m.mode {
	case modeAdd:
		return m.viewAdd()
	case modeConfirmDelete:
		return m.viewConfirm()
	case modeStudy:
		return m.viewStudy()
	case modeFonts:
		return m.viewFonts()
	}
	return m.viewMain()
}

func (m model) updatePrefs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	beforeSize := m.prefs.FontSize
	switch msg.String() {
	case "esc", "q", ",", "ctrl+c":
		m.prefOpen = false
		m.prefs.save()
		m.prefs.applyFontSize()
		if m.prefs.WidgetPick >= 0 {
			m.widget = m.prefs.WidgetPick % totalWidgets()
		}
		return m, nil
	case "j", "down":
		m.prefSel = mini(len(prefRows)-1, m.prefSel+1)
	case "k", "up":
		m.prefSel = maxi(0, m.prefSel-1)
	case "h", "left":
		prefRows[m.prefSel].dec(&m.prefs)
	case "l", "right", " ", "enter":
		prefRows[m.prefSel].inc(&m.prefs)
	case "r":
		m.prefs = defaultPrefs()
	}
	// only actually ask kitty to resize when FontSize itself changed —
	// this used to fire on every keypress in this screen, including plain
	// j/k navigation and edits to unrelated rows, each one reflowing the
	// whole terminal grid for nothing.
	if m.prefs.FontSize != beforeSize {
		m.prefs.applyFontSize()
	}
	if m.prefs.WidgetPick >= 0 {
		m.widget = m.prefs.WidgetPick % totalWidgets()
	}
	return m, nil
}

// geometry derives every dimension from the terminal size, so nothing is
// hardcoded and the layout degrades instead of breaking.
func (m model) geometry() (railW, itemW, rows int) {
	// inset so the backdrop pattern shows as a margin on all sides
	w := m.contentWidth()
	railW = 14
	if w < 64 {
		railW = 10
	}
	// each pane costs 2 border + 2 padding columns
	itemW = w - railW - 4 - 4 - 1
	if itemW < 20 {
		itemW = 20
	}
	bannerH := 1
	if m.bannerTxt != "" {
		bannerH = strings.Count(m.bannerTxt, "\n") + 1
	}
	rows = m.h - bannerH - 10 // panes + detail(3) + status(1) + margins
	if rows < 3 {
		rows = 3
	}
	return railW, itemW, rows
}

func window(cursor, n, visible int) (start, end int) {
	if cursor >= visible {
		start = cursor - visible + 1
	}
	end = mini(n, start+visible)
	return start, end
}

func (m model) viewMain() string {
	railW, itemW, rows := m.geometry()
	inGen := m.mode == modeGen || (m.mode == modeFilter && len(m.stack) > 0)

	var head string
	if m.bannerTxt != "" {
		head = m.bannerTxt
	} else {
		head = " " + gradient("▌ S W I T C H B O A R D", true) + "  " +
			cDim.Render("the things you forget you have")
	}

	rail := paneOff
	items := paneOn
	if !inGen && m.focus == focusGroups {
		rail, items = paneOn, paneOff
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		rail.Height(rows).Render(m.renderRail(railW, rows, inGen)),
		items.Height(rows).Render(m.renderItems(itemW, rows, inGen)),
	)

	totalW := railW + itemW + 8
	out := head + "\n" + body + "\n" + m.renderDetail(totalW)
	if m.mode == modeFilter {
		out += "\n" + m.filter.View()
	} else {
		out += "\n" + m.renderStatus(totalW, inGen)
	}
	return out
}

// renderRail is the left column: groups normally, the generator path when
// you have descended into one.
func (m model) renderRail(w, rows int, inGen bool) string {
	var b strings.Builder
	if inGen {
		b.WriteString(paneTitle("path", w, false) + "\n")
		b.WriteString(cDim.Render(pad("sb", w)) + "\n")
		for i, lv := range m.stack {
			marker := "  "
			if i == len(m.stack)-1 {
				marker = cPurp.Render("▸ ")
			}
			b.WriteString(marker + cBase.Render(truncate(lv.title, w-2)) + "\n")
		}
		return b.String()
	}

	focused := m.focus == focusGroups
	b.WriteString(paneTitle("groups", w, focused) + "\n")
	start, end := window(m.groupIdx, len(m.groups), rows-2)
	for i := start; i < end; i++ {
		g := m.groups[i]
		label := g
		if g == allGroups {
			label = "★ all"
		}
		line := pad(truncate(label, w), w)
		switch {
		case i == m.groupIdx && focused:
			b.WriteString(selOn.Render(line))
		case i == m.groupIdx:
			b.WriteString(selOff.Render(line))
		default:
			b.WriteString(cDim.Render(line))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m model) renderItems(w, rows int, inGen bool) string {
	var b strings.Builder

	if inGen {
		lv := m.top()
		title := "…"
		if lv != nil {
			title = lv.title
		}
		b.WriteString(paneTitle(title, w, true) + "\n")
		if m.genLoad {
			b.WriteString(m.spin.View() + cDim.Render(" running the generator…"))
			return b.String()
		}
		if lv == nil || len(lv.shown) == 0 {
			b.WriteString(cDim.Render("nothing matches"))
			return b.String()
		}
		nested := isGen(lv.tmpl)
		start, end := window(lv.cursor, len(lv.shown), rows-2)
		for i := start; i < end; i++ {
			line := lv.items[lv.shown[i]]
			watched := m.watched[watchedKey(lv.title, line)]
			mark := " "
			if nested {
				mark = "›"
			}
			check := "  "
			if watched {
				check = "✓ "
			}
			text := check + pad(truncate(line, w-4), w-4) + " "
			switch {
			case i == lv.cursor:
				b.WriteString(selOn.Render(" "+text) + cPurp.Render(mark))
			case watched:
				b.WriteString(" " + cFant.Render(text) + cFant.Render(mark))
			default:
				b.WriteString(" " + cBase.Render(text) + cFant.Render(mark))
			}
			b.WriteString("\n")
		}
		return b.String()
	}

	focused := m.focus == focusItems
	b.WriteString(paneTitle(m.groups[m.groupIdx], w, focused) + "\n")
	if len(m.items) == 0 {
		b.WriteString(cDim.Render("nothing matches"))
		return b.String()
	}

	// name column sized to the longest name on screen, so descriptions line up
	nameW := 8
	for _, idx := range m.items {
		if n := lipgloss.Width(m.cmds[idx].Name); n > nameW {
			nameW = n
		}
	}
	nameW = mini(nameW, 16)

	start, end := window(m.itemIdx, len(m.items), rows-2)
	for i := start; i < end; i++ {
		c := m.cmds[m.items[i]]
		hits := ""
		if n := m.usage[key(c)]; n > 0 {
			hits = fmt.Sprintf(" ×%d", n)
		}
		mark := " "
		if isGen(c.Run) {
			mark = "›"
		} else if isExec(c.Run) {
			mark = "↺"
		}
		name := pad(truncate(c.Name, nameW), nameW)
		descW := w - nameW - lipgloss.Width(hits) - 4
		desc := pad(truncate(c.Desc, maxi(1, descW)), maxi(1, descW))

		switch {
		case i == m.itemIdx && focused:
			b.WriteString(selOn.Render(" "+name+" "+desc+hits+" ") + cPurp.Render(mark))
		case i == m.itemIdx:
			b.WriteString(" " + selOff.Render(name) + " " + cBase.Render(desc) +
				cFant.Render(hits) + " " + cPurp.Render(mark))
		default:
			b.WriteString(" " + cCool.Render(name) + " " + cDim.Render(desc) +
				cFant.Render(hits) + " " + cFant.Render(mark))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderDetail is the strip under the panes. It is a FIXED three lines —
// letting it grow with its content is what made the frame jitter as the
// selection moved between entries with and without notes.
//
// It also no longer shows the command. sb exists so you do not have to know
// the command; printing it back was using the most valuable line on screen to
// tell you the one thing the program is for hiding.
func (m model) renderDetail(w int) string {
	inner := w - 4
	line1, line2 := "", ""

	if m.mode == modeGen || (m.mode == modeFilter && len(m.stack) > 0) {
		lv := m.top()
		if lv != nil && len(lv.shown) > 0 {
			line1 = cBase.Render(truncate(lv.items[lv.shown[lv.cursor]], inner))
			if isGen(lv.tmpl) {
				line2 = cPurp.Render("› opens another list")
			} else {
				line2 = cDim.Render("↵ runs this")
			}
		}
	} else if c, ok := m.current(); ok {
		line1 = cCool.Render(c.Name) + cDim.Render("  "+truncate(c.Desc, inner-len(c.Name)-3))
		switch {
		case c.Note != "":
			line2 = cWarn.Render(truncate(c.Note, inner))
		case isGen(c.Run):
			line2 = cPurp.Render("› opens a list")
		case isExec(c.Run):
			line2 = cDim.Render("↺ runs here, returns to sb")
		default:
			line2 = cDim.Render("↵ hands off to the shell")
		}
	}
	if m.status != "" {
		line2 = cWarn.Render(truncate(m.status, inner))
	}
	return paneOff.Width(inner).Height(2).Render(
		truncateCells(line1, inner) + "\n" + truncateCells(line2, inner))
}

// truncateCells cuts a styled string by visible cells, not runes.
func truncateCells(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	return decor.Take(s, n, lipgloss.Width) + "\x1b[0m"
}

// renderStatus is the bar along the bottom, segmented like the lipgloss demo.
func (m model) renderStatus(w int, inGen bool) string {
	tag := "SB"
	keys := "↵ run  / filter  S study  F fonts  , prefs  t theme  a add  d del  e edit  q quit"
	if inGen {
		tag = "LIST"
		keys = "↵ run  w watched  h back  / filter  q back"
	}
	mid := "—"
	if inGen {
		var crumbs []string
		for _, lv := range m.stack {
			crumbs = append(crumbs, lv.title)
		}
		mid = strings.Join(crumbs, " / ")
	} else if c, ok := m.current(); ok {
		mid = c.Group + "/" + c.Name
	}

	left := stTag.Render(tag)
	right := stEnd.Render(filepath.Base(shellPath()))
	fill := w - lipgloss.Width(left) - lipgloss.Width(right)
	if fill < 4 {
		fill = 4
	}
	body := truncate(mid+"   "+cDim.Render(keys), fill-2)
	return left + stMid.Width(fill).Render(body) + right
}

func (m model) viewAdd() string {
	var b strings.Builder
	b.WriteString("\n " + gradient("▌ ADD A COMMAND", true) + "\n\n")
	for i := range m.addBuf {
		b.WriteString(m.addBuf[i].View() + "\n")
	}
	if m.status != "" {
		b.WriteString("\n  " + cWarn.Render(m.status) + "\n")
	}
	b.WriteString("\n" + cDim.Render("  tab next field   ↵ save   esc cancel") + "\n")
	return b.String()
}

func (m model) viewConfirm() string {
	c, _ := m.current()
	return "\n " + gradient("▌ DELETE", true) + "\n\n  " +
		cBase.Render("remove ") + cCool.Render(c.Name) + cBase.Render("?") +
		"\n\n" + cDim.Render("  y yes   any other key no") + "\n"
}

// truncate by runes, not bytes — otherwise multibyte text gets cut mid-character
func truncate(s string, n int) string {
	if n < 1 {
		n = 1
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// ---------------------------------------------------------------- main

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "switchboard:", err)
		os.Exit(1)
	}
	m, ok := final.(model)
	if !ok || m.chosen == nil {
		return
	}
	if m.chosen.Name != "__edit__" && len(m.stack) == 0 {
		m.usage.bump(*m.chosen)
	}

	fmt.Println(cDim.Render("$ " + m.chosen.Run))
	cmd := exec.Command(shellPath(), interactiveArgs(m.chosen.Run)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "switchboard:", err)
	}
}
