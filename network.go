// network.go — a native menu over nmcli, in the same spirit as bluetooth.go:
// take a CLI you'd otherwise have to remember the verbs for and make it a
// live TUI screen. It lists nearby Wi-Fi, shows which one is in use and which
// are already saved, and connects / disconnects / forgets with one key.
//
// nmcli has no interactive REPL like bluetoothctl, so the pattern is a little
// different: `nmcli monitor` runs as a long-lived subprocess purely as a
// "something changed" bell, and every ring re-runs three short terse queries
// (`device wifi list`, `connection show`, `radio wifi`) to rebuild state.
package main

import (
	"bufio"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type wifiNet struct {
	ssid   string
	signal int // 0-100
	secure bool
	active bool // currently in use
	known  bool // a saved connection profile exists
}

type netState struct {
	nets     []wifiNet
	sel      int
	winStart int // see window() — persists so scrolling doesn't shift the frame
	msg      string
	radioOn  bool
	noDev    bool // no Wi-Fi device on this machine

	asking bool // password prompt is open
	pwFor  string
	pw     textinput.Model

	// connProg is the same staged-progress treatment as bluetooth.go's
	// connProg: nmcli gives no real percentage, so this jumps to a partial
	// value the moment an action starts and completes to 100% on
	// nmActionMsg — an honest "in progress / done" signal, not a fake timer.
	connProg progress.Model
	connBusy bool

	proc  *exec.Cmd
	lines chan string
}

// netProgDoneMsg clears the progress bar a moment after it reaches 100% — see
// the matching type in bluetooth.go.
type netProgDoneMsg struct{}

func netProgDone() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return netProgDoneMsg{} })
}

func (s *netState) current() *wifiNet {
	if s.sel < 0 || s.sel >= len(s.nets) {
		return nil
	}
	return &s.nets[s.sel]
}

// ---------------------------------------------------------------- nmcli plumbing

type nmLineMsg string     // one line from `nmcli monitor`
type nmEOFMsg struct{}    // the monitor exited
type nmPolledMsg struct { // a fresh snapshot from the terse queries
	nets    []wifiNet
	radioOn bool
	noDev   bool
}
type nmActionMsg struct { // an action (connect/…) finished
	summary string
	err     error
}

func nmReadLine(lines <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-lines
		if !ok {
			return nmEOFMsg{}
		}
		return nmLineMsg(line)
	}
}

// splitTerse splits one line of `nmcli -t` output on unescaped ':' and undoes
// nmcli's '\:' / '\\' escaping of field values.
func splitTerse(line string) []string {
	var fields []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			b.WriteByte(line[i+1])
			i++
			continue
		}
		if c == ':' {
			fields = append(fields, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	fields = append(fields, b.String())
	return fields
}

func nmcliOut(args ...string) []string {
	raw, err := exec.Command("nmcli", args...).Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// nmPoll rebuilds the whole picture: radio state, which devices exist, and the
// scan list folded so each SSID appears once at its strongest signal, tagged
// with whether it is active and whether a saved profile already covers it.
func nmPoll() tea.Cmd {
	return func() tea.Msg {
		radioOn := false
		for _, l := range nmcliOut("-t", "-f", "WIFI", "radio") {
			radioOn = strings.TrimSpace(l) == "enabled"
		}

		noDev := true
		for _, l := range nmcliOut("-t", "-f", "TYPE", "device") {
			if strings.TrimSpace(l) == "wifi" {
				noDev = false
			}
		}

		known := map[string]bool{}
		for _, l := range nmcliOut("-t", "-f", "NAME,TYPE", "connection", "show") {
			f := splitTerse(l)
			if len(f) >= 2 && f[1] == "802-11-wireless" {
				known[f[0]] = true
			}
		}

		byS := map[string]wifiNet{}
		for _, l := range nmcliOut("-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY", "device", "wifi", "list") {
			f := splitTerse(l)
			if len(f) < 4 || f[1] == "" {
				continue // hidden SSID or malformed
			}
			sig, _ := strconv.Atoi(f[2])
			n := wifiNet{
				ssid:   f[1],
				signal: sig,
				secure: strings.TrimSpace(f[3]) != "",
				active: strings.TrimSpace(f[0]) == "*",
				known:  known[f[1]],
			}
			if prev, ok := byS[n.ssid]; !ok || n.signal > prev.signal {
				if ok {
					n.active = n.active || prev.active
				}
				byS[n.ssid] = n
			}
		}

		nets := make([]wifiNet, 0, len(byS))
		for _, n := range byS {
			nets = append(nets, n)
		}
		sort.Slice(nets, func(i, j int) bool {
			if nets[i].active != nets[j].active {
				return nets[i].active
			}
			return nets[i].signal > nets[j].signal
		})
		return nmPolledMsg{nets: nets, radioOn: radioOn, noDev: noDev}
	}
}

// nmAction runs one nmcli command off the UI thread. Connecting with a
// password blocks until the association succeeds or fails, so this is also
// how a wrong passphrase surfaces as a real error instead of silence.
func nmAction(summary string, args ...string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command("nmcli", args...).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return nmActionMsg{summary: summary, err: fmt.Errorf("%s", msg)}
		}
		return nmActionMsg{summary: summary}
	}
}

// startNmMonitor launches `nmcli monitor` and a reader goroutine, and asks for
// the first state snapshot. Mirrors startBluetoothctl.
func startNmMonitor() (*netState, tea.Cmd) {
	pw := textinput.New()
	pw.Placeholder = "passphrase"
	pw.Prompt = "  password  "
	pw.EchoMode = textinput.EchoPassword
	pw.CharLimit = 128
	// Capped so the input scrolls internally instead of growing the footer
	// line wider than the pane as you type — an unbounded textinput.View()
	// would otherwise widen the screen's footprint keystroke by keystroke,
	// which shifts canvas()'s horizontal centring, i.e. the frame "jumps"
	// while you are mid-password.
	pw.Width = 24

	cmd := exec.Command("nmcli", "monitor")
	stdout, errOut := cmd.StdoutPipe()
	lines := make(chan string, 128)
	prog := progress.New(progress.WithGradient(themes[curTheme].Accent, themes[curTheme].Second))
	prog.ShowPercentage = false
	s := &netState{lines: lines, proc: cmd, pw: pw, connProg: prog}

	if errOut != nil || cmd.Start() != nil {
		s.msg = "could not start nmcli monitor"
		close(lines)
		return s, tea.Batch(nmReadLine(lines), nmPoll())
	}
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				lines <- l
			}
		}
	}()
	return s, tea.Batch(nmReadLine(lines), nmPoll())
}

func (s *netState) stop() {
	if s == nil || s.proc == nil || s.proc.Process == nil {
		return
	}
	s.proc.Process.Kill()
}

// ---------------------------------------------------------------- update

func (m model) updateNetwork(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.net
	if s == nil {
		m.mode = modeList
		return m, nil
	}

	// the password prompt owns the keyboard while it is open
	if s.asking {
		switch msg.String() {
		case "esc":
			s.asking = false
			s.pw.SetValue("")
			s.pw.Blur()
			s.msg = ""
			return m, nil
		case "enter":
			pass := s.pw.Value()
			ssid := s.pwFor
			s.asking = false
			s.pw.SetValue("")
			s.pw.Blur()
			s.msg = "connecting to " + ssid + "…"
			s.connBusy = true
			return m, tea.Batch(s.connProg.SetPercent(0.35), nmAction("connect "+ssid,
				"device", "wifi", "connect", ssid, "password", pass))
		}
		var cmd tea.Cmd
		s.pw, cmd = s.pw.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "esc", "q", "ctrl+c":
		s.stop()
		m.net = nil
		m.mode = modeList
		return m, nil
	case "j", "down":
		if s.sel < len(s.nets)-1 {
			s.sel++
		}
	case "k", "up":
		if s.sel > 0 {
			s.sel--
		}
	case "s":
		s.msg = "rescanning…"
		return m, tea.Sequence(
			nmAction("rescan", "device", "wifi", "rescan"), nmPoll())
	case "t":
		if s.radioOn {
			s.msg = "turning Wi-Fi off…"
			return m, tea.Sequence(nmAction("radio off", "radio", "wifi", "off"), nmPoll())
		}
		s.msg = "turning Wi-Fi on…"
		return m, tea.Sequence(nmAction("radio on", "radio", "wifi", "on"), nmPoll())
	case "enter":
		d := s.current()
		if d == nil {
			return m, nil
		}
		switch {
		case d.active:
			s.msg = "disconnecting " + d.ssid + "…"
			return m, tea.Sequence(
				nmAction("down "+d.ssid, "connection", "down", "id", d.ssid), nmPoll())
		case d.known:
			s.msg = "connecting to " + d.ssid + "…"
			s.connBusy = true
			return m, tea.Batch(s.connProg.SetPercent(0.5), tea.Sequence(
				nmAction("up "+d.ssid, "connection", "up", "id", d.ssid), nmPoll()))
		case d.secure:
			s.asking = true
			s.pwFor = d.ssid
			s.pw.Focus()
			s.msg = "passphrase for " + d.ssid
			return m, textinput.Blink
		default:
			s.msg = "connecting to " + d.ssid + "…"
			s.connBusy = true
			return m, tea.Batch(s.connProg.SetPercent(0.5), tea.Sequence(
				nmAction("connect "+d.ssid, "device", "wifi", "connect", d.ssid), nmPoll()))
		}
	case "r":
		if d := s.current(); d != nil && d.known {
			s.msg = "forgetting " + d.ssid
			return m, tea.Sequence(
				nmAction("delete "+d.ssid, "connection", "delete", "id", d.ssid), nmPoll())
		}
	}
	return m, nil
}

// ---------------------------------------------------------------- view

func (m model) viewNetwork() string {
	s := m.net
	if s == nil {
		return ""
	}
	w := m.contentWidth()
	h := m.h
	if h < 16 {
		h = 16
	}

	head := " " + gradient("▌ W I - F I", true) + "  "
	switch {
	case s.noDev:
		head += cWarn.Render("no Wi-Fi device")
	case s.radioOn:
		head += cCool.Render("● radio on")
	default:
		head += cDim.Render("○ radio off")
	}

	var list strings.Builder
	list.WriteString(paneTitle("networks", w-4, true) + "\n")
	switch {
	case s.noDev:
		list.WriteString(cDim.Render("this machine has no wireless interface"))
	case !s.radioOn:
		list.WriteString(cDim.Render("Wi-Fi radio is off — t turns it on"))
	case len(s.nets) == 0:
		list.WriteString(cDim.Render("nothing in range — s rescans"))
	}

	rows := h - 8
	if rows < 3 {
		rows = 3
	}
	start, end := window(s.sel, len(s.nets), rows, &s.winStart)
	for i := start; i < end; i++ {
		n := s.nets[i]
		status := cFant.Render("  ")
		switch {
		case n.active:
			status = cCool.Render("● connected")
		case n.known:
			status = cDim.Render("◐ saved")
		}
		lock := " "
		if n.secure {
			lock = "🔒"
		}
		line := fmt.Sprintf(" %-28s %s %3d%%  %s",
			truncate(n.ssid, 28), lock, n.signal, status)
		line = pad(truncate(line, w-6), w-6)
		if i == s.sel {
			list.WriteString(selOn.Render(line))
		} else {
			list.WriteString(cBase.Render(line))
		}
		list.WriteString("\n")
	}

	// Fixed height regardless of how many networks are visible — see the
	// matching comment in bluetooth.go's viewBluetooth. Without this, a scan
	// that finds more or fewer SSIDs grows or shrinks the pane and the whole
	// frame jumps.
	budget := rows + 2 // paneTitle's 2 lines + up to `rows` network lines
	body := paneOn.Width(w - 2).Height(budget).Render(clampLines(list.String(), budget))

	// truncated to the pane width — see the matching comment in
	// bluetooth.go's viewBluetooth: an unbounded s.msg (a long SSID) would
	// otherwise widen this line and shift canvas()'s horizontal centring.
	// The progress bar's width is fixed, so it changes this line's length by
	// a constant amount, not a live one.
	var foot string
	switch {
	case s.asking:
		foot = " " + s.pw.View()
	case s.connBusy:
		s.connProg.Width = 20
		foot = " " + s.connProg.View() + "  " + cWarn.Render(truncate(s.msg, maxi(1, w-28)))
	default:
		foot = " " + cWarn.Render(truncate(s.msg, maxi(1, w-6)))
	}

	keys := "↵ connect/disconnect  s rescan  t radio  r forget  q back"
	if s.asking {
		keys = "↵ join  esc cancel"
	}
	status := stTag.Render("WIFI") +
		stMid.Width(maxi(4, w-12)).Render(truncate(cDim.Render(keys), maxi(4, w-16)))

	return head + "\n" + body + "\n" + foot + "\n" + status
}
