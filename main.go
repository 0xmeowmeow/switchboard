// switchboard — a launcher for the commands you forget you have.
//
// Reads a TOML-ish config at ~/.config/switchboard/commands.conf,
// lists everything with descriptions, filters as you type, runs on Enter.
//
// The whole point: you don't have to remember. You browse.

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

const configTemplate = `# switchboard — your commands, described.
#
# format:   group | name | description | command | optional note
# lines starting with # are ignored. blank lines are ignored.
#
# edit this file freely, or use 'a' to add and 'd' to delete from the TUI.

writing | obsidian    | the vault                          | ~/Applications/Obsidian-*.AppImage |
writing | loomboot    | start the loom web server          | cd ~/data/programming/loom && python3 -m http.server 8000 | then http://localhost:8000/loom.html
writing | loomup      | install downloaded loom files      | loomup |

work    | invoice     | generate an invoice                | python3 ~/data/computer/scripts/invoice.py |
work    | tdf-browse  | browse the TDF                     | ~/data/computer/scripts/tdf-browse | worth playing with more

making  | blender     | 3D                                 | $HOME/Applications/blender-4.3.2-linux-x64/blender |
making  | creality    | slicer                             | $HOME/Applications/CrealityPrint_Ubuntu2404-V7.0.1.4212-x86_64-Release.AppImage |
making  | vial        | keyboard config                    | ~/Applications/vial.AppImage |
making  | comfyui     | image generation                   | cd ~/data/programming/ComfyUI && source venv/bin/activate && python3 main.py |

ai      | ollama-list | which models do I have             | ollama list |
ai      | ollama-ps   | what is loaded in VRAM right now   | ollama ps |
ai      | qwen        | chat with qwen 7b                  | ollama run qwen2.5:7b-instruct-q4_K_M |

music   | cmus        | music player                       | cmus | keys: z prev, x play, c pause, v stop, b next, / search, tab switch, 1-4 views
music   | tmus        | cmus + visualiser, three panes     | tmux kill-session -t music 2>/dev/null; tmux new-session -d -s music -x 200 -y 50 && tmux send-keys -t music "cmus" Enter && tmux split-window -t music -h "cava" && tmux split-window -t music -v "man cmus" && tmux attach -t music |

system  | btconnect   | connect the bluetooth headphones   | bluetoothctl connect 88:0E:85:CC:FA:BB |
system  | hyprconf    | edit hyprland config               | nano ~/.config/hypr/hyprland.conf |
system  | kittyconf   | edit kitty config                  | nano ~/.config/kitty/kitty.conf |
system  | dotfiles    | dotfiles bare repo status          | git --git-dir=$HOME/.dotfiles --work-tree=$HOME status |

play    | twom        | This War of Mine                   | cd ~/.local/share/Steam/steamapps/common/This\ War\ of\ Mine/ && LD_LIBRARY_PATH=. ./This\ War\ of\ Mine |
play    | trek        | star trek                          | trek |
play    | tmatrix     | digital rain                       | tmatrix --fall-speed=0.1,1 -t= -C cyan |
`

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "switchboard", "commands.conf")
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
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 4 {
			continue
		}
		c := Cmd{Group: parts[0], Name: parts[1], Desc: parts[2], Run: parts[3]}
		if len(parts) > 4 {
			c.Note = parts[4]
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out, sc.Err()
}

func saveCommands(cmds []Cmd) error {
	var b strings.Builder
	b.WriteString("# switchboard — your commands, described.\n")
	b.WriteString("# format:   group | name | description | command | optional note\n\n")
	for _, c := range cmds {
		b.WriteString(fmt.Sprintf("%s | %s | %s | %s | %s\n",
			c.Group, c.Name, c.Desc, c.Run, c.Note))
	}
	return os.WriteFile(configPath(), []byte(b.String()), 0644)
}

// ---------------------------------------------------------------- style

var (
	cBase   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	cDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	cHot    = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	cCool   = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	cWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	cGroup  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)
	cSel    = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	cTitle  = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true).
		Padding(0, 1)
	cHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).Padding(0, 1)
)

// ---------------------------------------------------------------- model

type mode int

const (
	modeList mode = iota
	modeAdd
	modeConfirmDelete
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
}

func initialModel() model {
	cmds, err := loadCommands()
	m := model{cmds: cmds}
	if err != nil {
		m.status = "could not read config: " + err.Error()
	}
	ti := textinput.New()
	ti.Placeholder = "type to filter"
	ti.Prompt = "  / "
	ti.Focus()
	ti.CharLimit = 40
	m.filter = ti

	labels := []string{"group", "name", "description", "command", "note (optional)"}
	for i := range m.addBuf {
		t := textinput.New()
		t.Placeholder = labels[i]
		t.Prompt = fmt.Sprintf("  %-16s ", labels[i]+":")
		t.CharLimit = 200
		m.addBuf[i] = t
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
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func max(a, b int) int { if a > b { return a }; return b }

func (m model) Init() tea.Cmd { return textinput.Blink }

// ---------------------------------------------------------------- update

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch m.mode {

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
				sort.SliceStable(m.cmds, func(i, j int) bool {
					if m.cmds[i].Group != m.cmds[j].Group {
						return m.cmds[i].Group < m.cmds[j].Group
					}
					return m.cmds[i].Name < m.cmds[j].Name
				})
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
			case "enter":
				if len(m.filtered) > 0 {
					c := m.cmds[m.filtered[m.cursor]]
					m.chosen = &c
					m.quitting = true
					return m, tea.Quit
				}
				return m, nil
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
	}
	return m.viewList()
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
	// keep the cursor visible in a small window
	start := 0
	visible := m.h - 12
	if visible < 6 {
		visible = 6
	}
	if m.cursor >= visible {
		start = m.cursor - visible + 1
	}

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
		b.WriteString(pointer + name + " " + desc + "\n")
	}

	// detail box for the selection
	if len(m.filtered) > 0 {
		c := m.cmds[m.filtered[m.cursor]]
		detail := cDim.Render("$ ") + cBase.Render(truncate(c.Run, m.w-8))
		if c.Note != "" {
			detail += "\n" + cWarn.Render(truncate(c.Note, m.w-8))
		}
		b.WriteString("\n" + cBox.Render(detail) + "\n")
	}

	if m.status != "" {
		b.WriteString("  " + cWarn.Render(m.status) + "\n")
	}
	b.WriteString("\n" + cHelp.Render(
		"  enter run   ctrl+a add   ctrl+d delete   ctrl+e edit file   esc quit") + "\n")
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

	// hand the chosen command to the shell, replacing this process so
	// interactive TUIs (cmus, tmux, nano) work properly
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	fmt.Println(cDim.Render("$ " + m.chosen.Run))
	cmd := exec.Command(sh, "-c", m.chosen.Run)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "switchboard:", err)
	}
}
