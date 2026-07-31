// switchboard — a launcher for the commands you forget you have.
//
// Reads a config at ~/.config/switchboard/commands.conf, lists everything
// with descriptions, filters as you type, runs on Enter.
//
// The whole point: you don't have to remember. You browse.
//
// Three things this build adds:
//   - commands run under an INTERACTIVE shell, so your aliases and zsh
//     functions work (tvshows, loomup, fans, trek...)
//   - "\|" is a literal pipe, so commands can contain pipelines
//   - "@gen" entries build their list at runtime from a command's output

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

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
	Note  string // optional extra line: keys, hints, reminders
}

// A generator entry looks like:
//
//	ai | models | pick a model | @gen ollama list \| tail -n +2 >> ollama run {1} |
//
// The part after @gen and before >> is run, and each line of its output
// becomes a browsable item. The part after >> is the template that runs
// when you pick one:  {} = the whole line, {1}..{9} = whitespace fields.
// With no >>, the line itself is run.
const genPrefix = "@gen "

func (c Cmd) isGen() bool {
	return strings.HasPrefix(strings.TrimSpace(c.Run), genPrefix)
}

func (c Cmd) genParts() (list, tmpl string) {
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Run), genPrefix))
	if i := strings.Index(body, ">>"); i >= 0 {
		return strings.TrimSpace(body[:i]), strings.TrimSpace(body[i+2:])
	}
	return body, "{}"
}

// substitute fills {} and {1}..{9} from a generated line.
func substitute(tmpl, line string) string {
	fields := strings.Fields(line)
	out := strings.ReplaceAll(tmpl, "{}", line)
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
# lines starting with # are ignored. blank lines are ignored.
#
# a literal pipe inside a command must be escaped:  \|
#
# a "@gen" command builds its list when you open it:
#   group | name | desc | @gen LIST-COMMAND >> RUN-TEMPLATE | note
#   {} is the whole output line, {1}..{9} are whitespace-separated fields.

ai      | models      | pick a model and chat              | @gen ollama list \| tail -n +2 >> ollama run {1} | list is live, no config to maintain
ai      | ollama-ps   | what is loaded in VRAM right now   | ollama ps |
`

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "switchboard", "commands.conf")
}

// splitFields splits on unescaped pipes. "\|" becomes a literal pipe,
// which is what lets a command contain a pipeline.
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

func sortCmds(c []Cmd) {
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].Group != c[j].Group {
			return c[i].Group < c[j].Group
		}
		return c[i].Name < c[j].Name
	})
}

func loadCommands() ([]Cmd, error) {
	p := configPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		os.MkdirAll(filepath.Dir(p), 0755)
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
	sortCmds(out)
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

// Interactive so that aliases and shell functions defined in .zshrc resolve.
// A non-interactive zsh reads .zshenv only, which is why bare aliases
// (tvshows, loomup, fans) used to fail with "command not found".
func shellArgs(cmd string) []string { return []string{"-i", "-c", cmd} }

type genResultMsg struct {
	items []string
	err   error
}

func runGenerator(listCmd string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command(shellPath(), shellArgs(listCmd)...).Output()
		if err != nil && len(out) == 0 {
			// fall back to a non-interactive shell in case -i misbehaves
			out, err = exec.Command(shellPath(), "-c", listCmd).Output()
			if err != nil && len(out) == 0 {
				return genResultMsg{err: err}
			}
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

var (
	cBase  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	cDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	cHot   = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	cCool  = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	cWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	cGroup = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)
	cSel   = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	cTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true).
		Padding(0, 1)
	cHelp = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cBox  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).Padding(0, 1)
)

// ---------------------------------------------------------------- model

type mode int

const (
	modeList mode = iota
	modeAdd
	modeConfirmDelete
	modeGen
)

type model struct {
	cmds     []Cmd
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

	// generator sub-view
	genParent Cmd
	genTmpl   string
	genAll    []string
	genShown  []int
	genCursor int
	genFilter textinput.Model
	genLoad   bool
}

func newInput(placeholder, prompt string, limit int) textinput.Model {
	t := textinput.New()
	t.Placeholder = placeholder
	t.Prompt = prompt
	t.CharLimit = limit
	return t
}

func initialModel() model {
	cmds, err := loadCommands()
	m := model{cmds: cmds}
	if err != nil {
		m.status = "could not read config: " + err.Error()
	}
	m.filter = newInput("type to filter", "  / ", 40)
	m.filter.Focus()
	m.genFilter = newInput("type to filter", "  / ", 40)

	labels := []string{"group", "name", "description", "command", "note (optional)"}
	for i := range m.addBuf {
		m.addBuf[i] = newInput(labels[i], fmt.Sprintf("  %-16s ", labels[i]+":"), 200)
	}
	m.refilter()
	return m
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
	q := strings.ToLower(m.genFilter.Value())
	m.genShown = m.genShown[:0]
	for i, s := range m.genAll {
		if q == "" || strings.Contains(strings.ToLower(s), q) {
			m.genShown = append(m.genShown, i)
		}
	}
	if m.genCursor >= len(m.genShown) {
		m.genCursor = maxi(0, len(m.genShown)-1)
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
		if msg.err != nil {
			m.mode = modeList
			m.filter.Focus()
			m.status = "generator failed: " + msg.err.Error()
			return m, nil
		}
		m.genAll = msg.items
		m.genCursor = 0
		m.regenFilter()
		if len(m.genAll) == 0 {
			m.mode = modeList
			m.filter.Focus()
			m.status = m.genParent.Name + ": generator returned nothing"
		}
		return m, nil

	case tea.KeyMsg:
		switch m.mode {

		case modeGen:
			switch msg.String() {
			case "esc", "ctrl+c", "left":
				m.mode = modeList
				m.genFilter.SetValue("")
				m.genFilter.Blur()
				m.filter.Focus()
				m.status = ""
				return m, nil
			case "up", "ctrl+p":
				if m.genCursor > 0 {
					m.genCursor--
				}
				return m, nil
			case "down", "ctrl+n":
				if m.genCursor < len(m.genShown)-1 {
					m.genCursor++
				}
				return m, nil
			case "enter":
				if len(m.genShown) > 0 {
					line := m.genAll[m.genShown[m.genCursor]]
					c := Cmd{Name: m.genParent.Name, Run: substitute(m.genTmpl, line)}
					m.chosen = &c
					m.quitting = true
					return m, tea.Quit
				}
				return m, nil
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
				sortCmds(m.cmds)
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
				if c.isGen() {
					list, tmpl := c.genParts()
					m.genParent, m.genTmpl = c, tmpl
					m.genAll, m.genShown, m.genCursor = nil, nil, 0
					m.genLoad = true
					m.mode = modeGen
					m.filter.Blur()
					m.genFilter.SetValue("")
					m.genFilter.Focus()
					m.status = ""
					return m, runGenerator(list)
				}
				if msg.String() == "right" {
					return m, nil // right only descends into generators
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

// window returns the slice bounds that keep the cursor visible.
func (m model) window(cursor int) (start, visible int) {
	visible = m.h - 12
	if visible < 6 {
		visible = 6
	}
	if cursor >= visible {
		start = cursor - visible + 1
	}
	return start, visible
}

func (m model) viewList() string {
	var b strings.Builder
	b.WriteString("\n" + cTitle.Render("SWITCHBOARD") + "  " +
		cDim.Render("the things you forget you have") + "\n\n")
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
			b.WriteString("  " + cGroup.Render(strings.ToUpper(c.Group)) + "\n")
			lastGroup = c.Group
		}
		pointer := "   "
		name := cCool.Render(fmt.Sprintf("%-12s", c.Name))
		desc := cDim.Render(c.Desc)
		if vi == m.cursor {
			pointer = cHot.Render(" > ")
			name = cSel.Render(fmt.Sprintf("%-12s", c.Name))
			desc = cBase.Render(c.Desc)
		}
		tail := ""
		if c.isGen() {
			tail = " " + cWarn.Render("›")
		}
		b.WriteString(pointer + name + " " + desc + tail + "\n")
	}

	if len(m.filtered) > 0 {
		c := m.cmds[m.filtered[m.cursor]]
		var detail string
		if c.isGen() {
			list, tmpl := c.genParts()
			detail = cWarn.Render("list  ") + cBase.Render(truncate(list, m.w-10)) + "\n" +
				cWarn.Render("run   ") + cBase.Render(truncate(tmpl, m.w-10))
		} else {
			detail = cDim.Render("$ ") + cBase.Render(truncate(c.Run, m.w-8))
		}
		if c.Note != "" {
			detail += "\n" + cWarn.Render(truncate(c.Note, m.w-8))
		}
		b.WriteString("\n" + cBox.Render(detail) + "\n")
	}

	if m.status != "" {
		b.WriteString("  " + cWarn.Render(m.status) + "\n")
	}
	b.WriteString("\n" + cHelp.Render(
		"  enter run   › opens a list   ctrl+a add   ctrl+d delete   ctrl+e edit file   esc quit") + "\n")
	return b.String()
}

func (m model) viewGen() string {
	var b strings.Builder
	b.WriteString("\n" + cTitle.Render(strings.ToUpper(m.genParent.Name)) + "  " +
		cDim.Render(m.genParent.Desc) + "\n\n")

	if m.genLoad {
		b.WriteString(cDim.Render("  running the generator…\n"))
		b.WriteString("\n" + cHelp.Render("  esc back") + "\n")
		return b.String()
	}

	b.WriteString(m.genFilter.View() + "\n\n")
	if len(m.genShown) == 0 {
		b.WriteString(cDim.Render("  nothing matches\n"))
	}

	start, visible := m.window(m.genCursor)
	for vi, idx := range m.genShown {
		if vi < start || vi >= start+visible {
			continue
		}
		line := truncate(m.genAll[idx], m.w-6)
		if vi == m.genCursor {
			b.WriteString(cHot.Render(" > ") + cSel.Render(line) + "\n")
		} else {
			b.WriteString("   " + cBase.Render(line) + "\n")
		}
	}

	if len(m.genShown) > 0 {
		resolved := substitute(m.genTmpl, m.genAll[m.genShown[m.genCursor]])
		b.WriteString("\n" + cBox.Render(
			cDim.Render("$ ")+cBase.Render(truncate(resolved, m.w-8))) + "\n")
	}
	b.WriteString("\n" + cHelp.Render("  enter run   esc back") + "\n")
	return b.String()
}

func (m model) viewAdd() string {
	var b strings.Builder
	b.WriteString("\n" + cTitle.Render("ADD A COMMAND") + "\n\n")
	for i := range m.addBuf {
		b.WriteString(m.addBuf[i].View() + "\n")
	}
	if m.status != "" {
		b.WriteString("\n  " + cWarn.Render(m.status) + "\n")
	}
	b.WriteString("\n" + cHelp.Render(
		"  tab next field   enter save   esc cancel") + "\n")
	return b.String()
}

func (m model) viewConfirm() string {
	c := m.cmds[m.filtered[m.cursor]]
	return "\n" + cTitle.Render("DELETE") + "\n\n  " +
		cBase.Render("remove ") + cHot.Render(c.Name) + cBase.Render("?") +
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

	// Hand the chosen command to an interactive shell, so aliases and
	// functions from .zshrc resolve, and so interactive TUIs work.
	fmt.Println(cDim.Render("$ " + m.chosen.Run))
	cmd := exec.Command(shellPath(), shellArgs(m.chosen.Run)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "switchboard:", err)
	}
}
