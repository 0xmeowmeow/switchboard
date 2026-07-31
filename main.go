// switchboard — a launcher for the commands you forget you have.
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

// genParts splits "@gen LIST >> TMPL" on the FIRST >>, which is what makes
// nesting work: the remainder keeps its own >> for the level below.
func genParts(run string) (list, tmpl string) {
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(run), genPrefix))
	body = strings.TrimSpace(body)
	if i := strings.Index(body, ">>"); i >= 0 {
		return strings.TrimSpace(body[:i]), strings.TrimSpace(body[i+2:])
	}
	return body, "{}"
}

// substitute fills {} {1}..{9} from line, and {^} from parent.
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

ai   | models | pick a model and chat | @gen ollama list \| tail -n +2 >> ollama run {1} | live list, never stale
`

func confDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "switchboard")
}
func configPath() string { return filepath.Join(confDir(), "commands.conf") }
func usagePath() string  { return filepath.Join(confDir(), "usage.json") }

// splitFields splits on unescaped pipes only. "\|" survives as a real pipe.
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
	b, err := os.ReadFile(usagePath())
	if err == nil {
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

// sortCmds orders by what you actually use. Groups float up by their most-used
// member; inside a group, most-used first. Ties fall back to alphabetical, so
// a fresh install still looks sane.
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

// Two ways to run something, and the difference matters a great deal.
//
// interactiveArgs is ONLY safe once the TUI has exited and sb is back in the
// foreground process group. An interactive zsh does job control: it calls
// tcsetpgrp and expects to own the terminal. Start one while Bubbletea holds
// the tty and the kernel sends SIGTTIN — the shell is reading a terminal it
// does not control — and the whole job suspends. That is the
// "suspended (tty input)" you saw.
//
// plainArgs is for anything run *underneath* the TUI. No job control, no
// .zshrc, no p10k. Aliases will not resolve here, which is the price.
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
	// Kill the whole group, not just the shell. `sh -c "sleep 10"` may fork
	// rather than exec, and the grandchild keeps the stdout pipe open — so
	// killing the leader alone leaves Output() blocked for the full duration.
	// The negative pid is the group. WaitDelay caps the pipe wait regardless.
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
)

var (
	cyanRGB   = [3]int{0x00, 0xf0, 0xff}
	purpleRGB = [3]int{0xbf, 0x00, 0xff}

	cBase   = lipgloss.NewStyle().Foreground(lipgloss.Color("#d8d8e8"))
	cDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#5a5a72"))
	cCool   = lipgloss.NewStyle().Foreground(lipgloss.Color(neonCyan))
	cWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffb020"))
	cGroup  = lipgloss.NewStyle().Foreground(lipgloss.Color(neonPurple)).Bold(true)
	cSelTxt = lipgloss.NewStyle().Foreground(lipgloss.Color("#0a0a0f")).
		Background(lipgloss.Color(neonCyan)).Bold(true)
	cSelDesc = lipgloss.NewStyle().Foreground(lipgloss.Color(neonCyan))
	cHelp    = lipgloss.NewStyle().Foreground(lipgloss.Color("#4a4a5e"))
	cCount   = lipgloss.NewStyle().Foreground(lipgloss.Color("#3f3f52"))
	cBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(neonPurple)).Padding(0, 1)
)

func lerp(a, b [3]int, t float64) lipgloss.Color {
	f := func(i int) int { return a[i] + int(float64(b[i]-a[i])*t) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", f(0), f(1), f(2)))
}

// gradient paints a string cyan → purple, one step per rune.
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

// banner is the header. Override it with anything that prints, e.g.
//
//	export SB_BANNER='tdfgo -f impossible switchboard'
//
// so your TheDraw fonts can drive it. Falls back to a gradient title.
// customBanner is resolved ONCE, at startup. Resolving it per frame was the
// other half of the bug: View() runs on every keystroke, so an SB_BANNER of
// "tdfgo ..." meant a complete interactive-zsh startup per rendered frame.
func customBanner() string {
	c := os.Getenv("SB_BANNER")
	if c == "" {
		return ""
	}
	out, err := capture(c, 3*time.Second)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return ""
	}
	return strings.TrimRight(string(out), "\n") + "\n"
}

func banner(width int, custom string) string {
	if custom != "" {
		return custom
	}
	w := width - 2
	if w < 20 {
		w = 20
	}
	if w > 76 {
		w = 76
	}
	rule := strings.Repeat("▀▄", w/2)
	return " " + gradient(rule, false) + "\n" +
		" " + gradient("▌ S W I T C H B O A R D", true) + "  " +
		cDim.Render("the things you forget you have") + "\n" +
		" " + gradient(rule, false) + "\n"
}

// ---------------------------------------------------------------- model

type mode int

const (
	modeList mode = iota
	modeAdd
	modeConfirmDelete
	modeGen
)

// a level of the generator stack
type genLevel struct {
	title  string
	tmpl   string // raw, not yet substituted
	parent string // the line chosen one level up  ({^})
	items  []string
	shown  []int
	cursor int
}

type model struct {
	cmds     []Cmd
	usage    usageMap
	filtered []int
	cursor   int
	filter   textinput.Model
	mode     mode
	addField int
	addBuf   [5]textinput.Model
	status   string
	quitting bool
	chosen   *Cmd
	h, w     int

	bannerTxt string
	stack     []genLevel
	genFilter textinput.Model
	genLoad   bool
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
	m := model{cmds: cmds, usage: u, bannerTxt: customBanner()}
	if err != nil {
		m.status = "could not read config: " + err.Error()
	}
	m.filter = newInput("type to filter", "  ▸ ", 40)
	m.filter.Focus()
	m.genFilter = newInput("type to filter", "  ▸ ", 40)

	labels := []string{"group", "name", "description", "command", "note (optional)"}
	for i := range m.addBuf {
		m.addBuf[i] = newInput(labels[i], fmt.Sprintf("  %-16s ", labels[i]+":"), 200)
	}
	m.refilter()
	return m
}

func (m *model) top() *genLevel {
	if len(m.stack) == 0 {
		return nil
	}
	return &m.stack[len(m.stack)-1]
}

func (m *model) refilter() {
	q := strings.ToLower(m.filter.Value())
	m.filtered = m.filtered[:0]
	for i, c := range m.cmds {
		if q == "" ||
			strings.Contains(strings.ToLower(c.Name), q) ||
			strings.Contains(strings.ToLower(c.Desc), q) ||
			strings.Contains(strings.ToLower(c.Group), q) ||
			strings.Contains(strings.ToLower(c.Note), q) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = maxi(0, len(m.filtered)-1)
	}
}

func (m *model) regenFilter() {
	lv := m.top()
	if lv == nil {
		return
	}
	q := strings.ToLower(m.genFilter.Value())
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

// push opens a new generator level.
func (m *model) push(title, listCmd, tmpl, parent string) tea.Cmd {
	m.stack = append(m.stack, genLevel{title: title, tmpl: tmpl, parent: parent})
	m.genLoad = true
	m.mode = modeGen
	m.filter.Blur()
	m.genFilter.SetValue("")
	m.genFilter.Focus()
	m.status = ""
	return runGenerator(listCmd)
}

func (m *model) pop() {
	if len(m.stack) > 0 {
		m.stack = m.stack[:len(m.stack)-1]
	}
	m.genFilter.SetValue("")
	if len(m.stack) == 0 {
		m.mode = modeList
		m.genFilter.Blur()
		m.filter.Focus()
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

		case modeGen:
			lv := m.top()
			switch msg.String() {
			case "esc", "ctrl+c", "left":
				m.pop()
				return m, nil
			case "up", "ctrl+p":
				if lv != nil && lv.cursor > 0 {
					lv.cursor--
				}
				return m, nil
			case "down", "ctrl+n":
				if lv != nil && lv.cursor < len(lv.shown)-1 {
					lv.cursor++
				}
				return m, nil
			case "enter", "right":
				if lv == nil || len(lv.shown) == 0 {
					return m, nil
				}
				line := lv.items[lv.shown[lv.cursor]]
				// A template that is itself a generator descends a level.
				if isGen(lv.tmpl) {
					inList, inTmpl := genParts(lv.tmpl)
					return m, m.push(line,
						substitute(inList, line, lv.parent), inTmpl, line)
				}
				if msg.String() == "right" {
					return m, nil
				}
				c := Cmd{Name: lv.title, Run: substitute(lv.tmpl, line, lv.parent)}
				m.chosen = &c
				m.quitting = true
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.genFilter, cmd = m.genFilter.Update(msg)
			m.regenFilter()
			return m, cmd

		case modeAdd:
			switch msg.String() {
			case "esc":
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
				m.refilter()
				return m, nil
			}
			var cmd tea.Cmd
			m.addBuf[m.addField], cmd = m.addBuf[m.addField].Update(msg)
			return m, cmd

		case modeConfirmDelete:
			switch msg.String() {
			case "y", "Y":
				if len(m.filtered) > 0 {
					idx := m.filtered[m.cursor]
					name := m.cmds[idx].Name
					m.cmds = append(m.cmds[:idx], m.cmds[idx+1:]...)
					if err := saveCommands(m.cmds); err != nil {
						m.status = "save failed: " + err.Error()
					} else {
						m.status = "deleted " + name
					}
					m.refilter()
				}
				m.mode = modeList
				return m, nil
			default:
				m.mode = modeList
				m.status = ""
				return m, nil
			}

		default: // modeList
			switch msg.String() {
			case "ctrl+c", "esc":
				m.quitting = true
				return m, tea.Quit
			case "up", "ctrl+p":
				if m.cursor > 0 {
					m.cursor--
				}
				return m, nil
			case "down", "ctrl+n":
				if m.cursor < len(m.filtered)-1 {
					m.cursor++
				}
				return m, nil
			case "enter", "right":
				if len(m.filtered) == 0 {
					return m, nil
				}
				c := m.cmds[m.filtered[m.cursor]]
				if isGen(c.Run) {
					list, tmpl := genParts(c.Run)
					m.usage.bump(c)
					return m, m.push(c.Name, list, tmpl, "")
				}
				if msg.String() == "right" {
					return m, nil
				}
				m.chosen = &c
				m.quitting = true
				return m, tea.Quit
			case "ctrl+a":
				m.mode = modeAdd
				m.addField = 0
				m.addBuf[0].Focus()
				m.status = ""
				return m, nil
			case "ctrl+d":
				if len(m.filtered) > 0 {
					m.mode = modeConfirmDelete
				}
				return m, nil
			case "ctrl+e":
				m.chosen = &Cmd{Name: "__edit__",
					Run: "${EDITOR:-nano} " + configPath()}
				m.quitting = true
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.refilter()
			return m, cmd
		}
	}
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
	case modeGen:
		return m.viewGen()
	}
	return m.viewList()
}

func (m model) window(cursor int) (start, visible int) {
	visible = m.h - 14
	if visible < 6 {
		visible = 6
	}
	if cursor >= visible {
		start = cursor - visible + 1
	}
	return start, visible
}

func (m model) width() int {
	if m.w <= 0 {
		return 80
	}
	return m.w
}

func (m model) viewList() string {
	var b strings.Builder
	b.WriteString("\n" + banner(m.width(), m.bannerTxt) + "\n")
	b.WriteString(m.filter.View() + "\n\n")

	if len(m.filtered) == 0 {
		b.WriteString(cDim.Render("  nothing matches\n"))
	}

	lastGroup := ""
	start, visible := m.window(m.cursor)

	for vi, idx := range m.filtered {
		if vi < start || vi >= start+visible {
			continue
		}
		c := m.cmds[idx]
		if c.Group != lastGroup {
			b.WriteString("  " + cGroup.Render("▌ "+strings.ToUpper(c.Group)) + "\n")
			lastGroup = c.Group
		}
		mark := " "
		if isGen(c.Run) {
			mark = cGroup.Render("›")
		}
		hits := ""
		if n := m.usage[key(c)]; n > 0 {
			hits = cCount.Render(fmt.Sprintf(" ×%d", n))
		}
		if vi == m.cursor {
			b.WriteString(" " + cSelTxt.Render(fmt.Sprintf(" ▶ %-13s", c.Name)) +
				" " + cSelDesc.Render(c.Desc) + hits + " " + mark + "\n")
		} else {
			b.WriteString("   " + cCool.Render(fmt.Sprintf("%-13s", c.Name)) +
				" " + cDim.Render(c.Desc) + hits + " " + mark + "\n")
		}
	}

	if len(m.filtered) > 0 {
		c := m.cmds[m.filtered[m.cursor]]
		var detail string
		if isGen(c.Run) {
			list, tmpl := genParts(c.Run)
			detail = cGroup.Render("list ") + cBase.Render(truncate(list, m.width()-12)) + "\n" +
				cGroup.Render("run  ") + cBase.Render(truncate(tmpl, m.width()-12))
		} else {
			detail = cCool.Render("$ ") + cBase.Render(truncate(c.Run, m.width()-10))
		}
		if c.Note != "" {
			detail += "\n" + cWarn.Render(truncate(c.Note, m.width()-10))
		}
		b.WriteString("\n" + cBox.Render(detail) + "\n")
	}

	if m.status != "" {
		b.WriteString("  " + cWarn.Render(m.status) + "\n")
	}
	b.WriteString(cHelp.Render(
		"  ↵ run   → open a ›list   ^a add   ^d delete   ^e edit file   esc quit") + "\n")
	return b.String()
}

func (m model) viewGen() string {
	var b strings.Builder
	var crumbs []string
	for _, lv := range m.stack {
		crumbs = append(crumbs, lv.title)
	}
	b.WriteString("\n " + gradient("▌ "+strings.ToUpper(strings.Join(crumbs, " / ")), true) + "\n\n")

	if m.genLoad {
		b.WriteString(cDim.Render("  running…\n"))
		b.WriteString("\n" + cHelp.Render("  esc back") + "\n")
		return b.String()
	}

	lv := m.top()
	if lv == nil {
		return b.String()
	}

	b.WriteString(m.genFilter.View() + "\n\n")
	if len(lv.shown) == 0 {
		b.WriteString(cDim.Render("  nothing matches\n"))
	}

	nested := isGen(lv.tmpl)
	start, visible := m.window(lv.cursor)
	for vi, idx := range lv.shown {
		if vi < start || vi >= start+visible {
			continue
		}
		line := truncate(lv.items[idx], m.width()-10)
		mark := " "
		if nested {
			mark = cGroup.Render("›")
		}
		if vi == lv.cursor {
			b.WriteString(" " + cSelTxt.Render(" ▶ "+line+" ") + " " + mark + "\n")
		} else {
			b.WriteString("   " + cBase.Render(line) + " " + mark + "\n")
		}
	}

	if len(lv.shown) > 0 {
		line := lv.items[lv.shown[lv.cursor]]
		var detail string
		if nested {
			inList, _ := genParts(lv.tmpl)
			detail = cGroup.Render("list ") +
				cBase.Render(truncate(substitute(inList, line, lv.parent), m.width()-12))
		} else {
			detail = cCool.Render("$ ") +
				cBase.Render(truncate(substitute(lv.tmpl, line, lv.parent), m.width()-12))
		}
		b.WriteString("\n" + cBox.Render(detail) + "\n")
	}
	hint := "  ↵ run   esc back"
	if nested {
		hint = "  ↵ open   esc back"
	}
	b.WriteString(cHelp.Render(hint) + "\n")
	return b.String()
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
	b.WriteString("\n" + cHelp.Render("  tab next field   ↵ save   esc cancel") + "\n")
	return b.String()
}

func (m model) viewConfirm() string {
	c := m.cmds[m.filtered[m.cursor]]
	return "\n " + gradient("▌ DELETE", true) + "\n\n  " +
		cBase.Render("remove ") + cCool.Render(c.Name) + cBase.Render("?") +
		"\n\n" + cHelp.Render("  y yes   any other key no") + "\n"
}

// truncate by runes, not bytes — otherwise multibyte text gets cut mid-character
func truncate(s string, n int) string {
	if n < 10 {
		n = 10
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ---------------------------------------------------------------- main

func main() {
	p := tea.NewProgram(initialModel())
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
