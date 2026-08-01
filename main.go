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

const genPrefix = "@gen"

func isGen(run string) bool {
	s := strings.TrimSpace(run)
	return s == genPrefix || strings.HasPrefix(s, genPrefix+" ")
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

func substitute(tmpl, line, parent string) string {
	out := strings.ReplaceAll(tmpl, "{^}", parent)
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
	neonCyan   = "#00f0ff"
	neonPurple = "#bf00ff"
	neonPink   = "#ff2f92"
	inkDark    = "#0a0a0f"
)

var (
	cyanRGB   = [3]int{0x00, 0xf0, 0xff}
	purpleRGB = [3]int{0xbf, 0x00, 0xff}

	cBase = lipgloss.NewStyle().Foreground(lipgloss.Color("#d8d8e8"))
	cDim  = lipgloss.NewStyle().Foreground(lipgloss.Color("#5a5a72"))
	cFant = lipgloss.NewStyle().Foreground(lipgloss.Color("#3a3a4c"))
	cCool = lipgloss.NewStyle().Foreground(lipgloss.Color(neonCyan))
	cWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffb020"))
	cPurp = lipgloss.NewStyle().Foreground(lipgloss.Color(neonPurple))

	// panes: the focused one gets a cyan border, the other recedes
	paneOn = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(neonCyan)).Padding(0, 1)
	paneOff = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#2a2a38")).Padding(0, 1)

	// selection: inverse video when the pane has focus, quiet when it doesn't
	selOn = lipgloss.NewStyle().Foreground(lipgloss.Color(inkDark)).
		Background(lipgloss.Color(neonCyan)).Bold(true)
	selOff = lipgloss.NewStyle().Foreground(lipgloss.Color(neonCyan)).Bold(true)

	// status bar segments, in the style of the lipgloss demo
	stTag = lipgloss.NewStyle().Foreground(lipgloss.Color(inkDark)).
		Background(lipgloss.Color(neonPink)).Bold(true).Padding(0, 1)
	stMid = lipgloss.NewStyle().Foreground(lipgloss.Color("#d8d8e8")).
		Background(lipgloss.Color("#1c1c28")).Padding(0, 1)
	stEnd = lipgloss.NewStyle().Foreground(lipgloss.Color(inkDark)).
		Background(lipgloss.Color(neonPurple)).Bold(true).Padding(0, 1)
)

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
		st := lipgloss.NewStyle().Foreground(lerp(cyanRGB, purpleRGB, t))
		if bold {
			st = st.Bold(true)
		}
		b.WriteString(st.Render(string(ch)))
	}
	return b.String()
}

// customBanner is resolved ONCE, at startup. Resolving it per frame meant a
// complete shell startup per rendered keystroke.
func customBanner() string {
	c := os.Getenv("SB_BANNER")
	if c == "" {
		return ""
	}
	out, err := capture(c, 3*time.Second)
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
}

func newInput(placeholder, prompt string, limit int) textinput.Model {
	t := textinput.New()
	t.Placeholder = placeholder
	t.Prompt = prompt
	t.CharLimit = limit
	t.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(neonPink))
	return t
}

func initialModel() model {
	u := loadUsage()
	cmds, err := loadCommands(u)
	m := model{cmds: cmds, usage: u, bannerTxt: customBanner(), w: 80, h: 24}
	if err != nil {
		m.status = "could not read config: " + err.Error()
	}
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
	return runGenerator(listCmd)
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

func (m model) Init() tea.Cmd { return textinput.Blink }

// ---------------------------------------------------------------- update

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
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
		switch m.mode {
		case modeFilter:
			return m.updateFilter(msg)
		case modeAdd:
			return m.updateAdd(msg)
		case modeConfirmDelete:
			return m.updateConfirm(msg)
		case modeGen:
			return m.updateGen(msg)
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
		if isGen(c.Run) {
			list, tmpl := genParts(c.Run)
			m.usage.bump(c)
			return m, m.push(c.Name, list, tmpl, "")
		}
		m.chosen = &c
		m.quitting = true
		return m, tea.Quit

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
		c := Cmd{Name: lv.title, Run: substitute(lv.tmpl, line, lv.parent)}
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

func (m model) View() string {
	if m.quitting {
		return ""
	}
	switch m.mode {
	case modeAdd:
		return m.viewAdd()
	case modeConfirmDelete:
		return m.viewConfirm()
	}
	return m.viewMain()
}

// geometry derives every dimension from the terminal size, so nothing is
// hardcoded and the layout degrades instead of breaking.
func (m model) geometry() (railW, itemW, rows int) {
	w := m.w
	if w < 40 {
		w = 40
	}
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
	rows = m.h - bannerH - 8 // panes + detail(3) + status(1) + margins
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
			b.WriteString(cDim.Render("running…"))
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
			mark := " "
			if nested {
				mark = "›"
			}
			text := pad(truncate(line, w-2), w-2) + " "
			if i == lv.cursor {
				b.WriteString(selOn.Render(" "+text) + cPurp.Render(mark))
			} else {
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

// renderDetail is the pane underneath: exactly what will run, and the note.
func (m model) renderDetail(w int) string {
	inner := w - 4
	var lines []string

	if m.mode == modeGen || (m.mode == modeFilter && len(m.stack) > 0) {
		lv := m.top()
		if lv == nil || len(lv.shown) == 0 {
			lines = append(lines, cDim.Render("—"))
		} else {
			line := lv.items[lv.shown[lv.cursor]]
			if isGen(lv.tmpl) {
				inList, _ := genParts(lv.tmpl)
				lines = append(lines, cPurp.Render("list ")+
					cBase.Render(truncate(substitute(inList, line, lv.parent), inner-6)))
			} else {
				lines = append(lines, cCool.Render("$ ")+
					cBase.Render(truncate(substitute(lv.tmpl, line, lv.parent), inner-3)))
			}
		}
	} else if c, ok := m.current(); ok {
		if isGen(c.Run) {
			list, tmpl := genParts(c.Run)
			lines = append(lines,
				cPurp.Render("list ")+cBase.Render(truncate(list, inner-6)),
				cPurp.Render("run  ")+cBase.Render(truncate(tmpl, inner-6)))
		} else {
			lines = append(lines, cCool.Render("$ ")+cBase.Render(truncate(c.Run, inner-3)))
		}
		if c.Note != "" {
			lines = append(lines, cWarn.Render(truncate(c.Note, inner)))
		}
	} else {
		lines = append(lines, cDim.Render("—"))
	}
	if m.status != "" {
		lines = append(lines, cWarn.Render(truncate(m.status, inner)))
	}
	return paneOff.Width(w - 2).Render(strings.Join(lines, "\n"))
}

// renderStatus is the bar along the bottom, segmented like the lipgloss demo.
func (m model) renderStatus(w int, inGen bool) string {
	tag := "SB"
	keys := "↵ run  tab pane  / filter  a add  d del  e edit  q quit"
	if inGen {
		tag = "LIST"
		keys = "↵ run  h back  / filter  q back"
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
