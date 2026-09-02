// bluetooth.go — a native menu over bluetoothctl: scan, pair, connect,
// disconnect, forget. bluetoothctl runs as a long-lived background process
// for the life of this screen; its line-oriented event stream (a device
// appearing, a connection state changing) drives the UI live, rather than
// switchboard shelling out per action and hoping the result matches what
// you asked for.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type btDevice struct {
	mac, name                  string
	paired, connected, trusted bool
}

type btState struct {
	devices  []btDevice
	sel      int // index into visible(), not devices
	scanning bool
	msg      string
	pairing  string // mac currently being paired, "" if none in flight

	proc  *exec.Cmd
	stdin io.WriteCloser
	lines chan string
}

// visible is paired devices always, plus anything the current scan turned
// up — once scanning stops, discovered-but-unpaired noise drops out rather
// than cluttering the list permanently.
func (s *btState) visible() []int {
	var out []int
	for i, d := range s.devices {
		if d.paired || s.scanning {
			out = append(out, i)
		}
	}
	return out
}

func (s *btState) current() *btDevice {
	vis := s.visible()
	if s.sel >= len(vis) {
		return nil
	}
	return &s.devices[vis[s.sel]]
}

// cleanBtLine strips bluetoothctl's ANSI colour codes and its repeating
// "[DeviceName]> " prompt, which it interleaves into every line regardless
// of whether stdout is actually a terminal.
var btAnsiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
var btPromptRE = regexp.MustCompile(`^\[[^\]]*\]>\s*`)

func cleanBtLine(raw string) string {
	s := btAnsiRE.ReplaceAllString(raw, "")
	if i := strings.LastIndex(s, "\r"); i >= 0 {
		s = s[i+1:] // a line can be redrawn several times before its \n
	}
	s = btPromptRE.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

type btLineMsg string
type btEOFMsg struct{}

func btReadLine(lines <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-lines
		if !ok {
			return btEOFMsg{}
		}
		return btLineMsg(line)
	}
}

// seedDevices runs bluetoothctl in one-shot mode (no interactive prompt, no
// ANSI noise — unlike the persistent session) to build the initial device
// list. Paired and Connected are both live-only properties: bluetoothctl
// never emits a retroactive "Paired: yes" / "Connected: yes" event for a
// device that was already in that state before the session started, so
// asking twice — plain `devices` for what's paired, `devices Connected` for
// what's currently connected — is the only way to see accurate state for
// something like AirPods that connected before switchboard was even opened.
func seedDevices() []btDevice {
	var out []btDevice
	find := func(mac string) *btDevice {
		for i := range out {
			if out[i].mac == mac {
				return &out[i]
			}
		}
		return nil
	}
	scan := func(args ...string) []string {
		raw, err := exec.Command("bluetoothctl", args...).Output()
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	}
	for _, line := range scan("devices") {
		m := btDeviceRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		mac := strings.ToUpper(m[2])
		out = append(out, btDevice{mac: mac, name: m[3], paired: true})
	}
	for _, line := range scan("devices", "Connected") {
		m := btDeviceRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if d := find(strings.ToUpper(m[2])); d != nil {
			d.connected = true
		}
	}
	return out
}

// startBluetoothctl launches a persistent bluetoothctl session and a
// goroutine reading its output. The returned Cmd waits for one line;
// applying it re-issues the same Cmd, which is how a long-lived subprocess
// keeps feeding a bubbletea program without blocking it.
func startBluetoothctl() (*btState, tea.Cmd) {
	cmd := exec.Command("bluetoothctl")
	stdin, errIn := cmd.StdinPipe()
	stdout, errOut := cmd.StdoutPipe()
	lines := make(chan string, 128)
	s := &btState{proc: cmd, stdin: stdin, lines: lines, devices: seedDevices()}
	if errIn != nil || errOut != nil {
		s.msg = "could not start bluetoothctl"
		close(lines)
		return s, btReadLine(lines)
	}
	if err := cmd.Start(); err != nil {
		s.msg = "bluetoothctl: " + err.Error()
		close(lines)
		return s, btReadLine(lines)
	}
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			if cl := cleanBtLine(sc.Text()); cl != "" {
				lines <- cl
			}
		}
	}()
	fmt.Fprintln(stdin, "agent on")
	fmt.Fprintln(stdin, "default-agent")
	return s, btReadLine(lines)
}

func (s *btState) send(cmd string) {
	if s.stdin != nil {
		fmt.Fprintln(s.stdin, cmd)
	}
}

// stop tears the session down — called on leaving the screen (or quitting
// sb from inside it) so a background bluetoothctl never outlives the UI
// that owns it.
func (s *btState) stop() {
	if s == nil || s.proc == nil || s.proc.Process == nil {
		return
	}
	s.send("scan off")
	if s.stdin != nil {
		s.stdin.Close()
	}
	s.proc.Process.Kill()
}

func (s *btState) find(mac string) *btDevice {
	for i := range s.devices {
		if s.devices[i].mac == mac {
			return &s.devices[i]
		}
	}
	return nil
}

var btDeviceRE = regexp.MustCompile(`^(?:\[(NEW|CHG|DEL)\]\s+)?Device\s+([0-9A-Fa-f:]{17})\s+(.*)$`)

var btKnownProps = map[string]bool{
	"Connected": true, "Paired": true, "Trusted": true, "Bonded": true,
	"ServicesResolved": true, "RSSI": true, "LegacyPairing": true, "Blocked": true,
}

// applyLine folds one cleaned line of bluetoothctl output into the device
// list: a name (from the initial `devices` listing or a scan discovery), a
// property change (Connected/Paired/Trusted/...), a device disappearing
// mid-scan, or a pairing outcome that chains trust+connect on success.
func (s *btState) applyLine(line string) {
	switch {
	case strings.Contains(line, "Pairing successful") && s.pairing != "":
		s.send("trust " + s.pairing)
		s.send("connect " + s.pairing)
		s.msg = "paired — trusting and connecting…"
		s.pairing = ""
		return
	case strings.HasPrefix(line, "Failed to pair") && s.pairing != "":
		s.msg = line
		s.pairing = ""
		return
	}

	m := btDeviceRE.FindStringSubmatch(line)
	if m == nil {
		return
	}
	tag, mac, rest := m[1], strings.ToUpper(m[2]), m[3]

	if tag == "DEL" {
		if d := s.find(mac); d == nil || !d.paired {
			out := s.devices[:0]
			for _, d := range s.devices {
				if d.mac != mac {
					out = append(out, d)
				}
			}
			s.devices = out
		}
		return
	}

	if i := strings.Index(rest, ": "); i >= 0 && btKnownProps[rest[:i]] {
		prop, val := rest[:i], rest[i+2:] == "yes"
		d := s.find(mac)
		if d == nil {
			s.devices = append(s.devices, btDevice{mac: mac})
			d = &s.devices[len(s.devices)-1]
		}
		switch prop {
		case "Connected":
			d.connected = val
		case "Paired", "Bonded":
			d.paired = d.paired || val
		case "Trusted":
			d.trusted = val
		}
		return
	}

	// a bare name — from the initial `devices` dump (untagged: bluetoothctl
	// only lists devices it already knows, i.e. paired) or a live scan
	// discovery (always tagged [NEW], never yet paired). No separate
	// "Paired: yes" event ever arrives for the seed listing, so this tag
	// is the only signal — miss it and every device you already own looks
	// unpaired forever.
	if d := s.find(mac); d != nil {
		if rest != "" {
			d.name = rest
		}
		return
	}
	s.devices = append(s.devices, btDevice{mac: mac, name: rest, paired: tag == ""})
}

// ---------------------------------------------------------------- update

func (m model) updateBluetooth(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.bt
	if s == nil {
		m.mode = modeList
		return m, nil
	}
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		s.stop()
		m.bt = nil
		m.mode = modeList
		return m, nil
	case "j", "down":
		if s.sel < len(s.visible())-1 {
			s.sel++
		}
	case "k", "up":
		if s.sel > 0 {
			s.sel--
		}
	case "s":
		s.scanning = !s.scanning
		if s.scanning {
			s.send("scan on")
			s.msg = "scanning…"
		} else {
			s.send("scan off")
			s.msg = "scan stopped"
			s.sel = 0 // the visible set just shrank back to paired-only
		}
	case "enter":
		d := s.current()
		if d == nil {
			return m, nil
		}
		switch {
		case !d.paired:
			s.pairing = d.mac
			s.send("pair " + d.mac)
			s.msg = "pairing " + displayName(d) + "…"
		case d.connected:
			s.send("disconnect " + d.mac)
			s.msg = "disconnecting " + displayName(d) + "…"
		default:
			s.send("connect " + d.mac)
			s.msg = "connecting " + displayName(d) + "…"
		}
	case "r":
		if d := s.current(); d != nil && d.paired {
			s.msg = "forgetting " + displayName(d)
			s.send("remove " + d.mac)
		}
	}
	return m, nil
}

func displayName(d *btDevice) string {
	if d.name != "" {
		return d.name
	}
	return d.mac
}

// ---------------------------------------------------------------- view

func (m model) viewBluetooth() string {
	s := m.bt
	if s == nil {
		return ""
	}
	w := m.contentWidth()
	h := m.h
	if h < 16 {
		h = 16
	}

	head := " " + gradient("▌ B L U E T O O T H", true) + "  "
	if s.scanning {
		head += cCool.Render("● scanning")
	} else {
		head += cDim.Render("○ idle")
	}

	vis := s.visible()
	var list strings.Builder
	list.WriteString(paneTitle("devices", w-4, true) + "\n")
	if len(vis) == 0 {
		list.WriteString(cDim.Render("nothing paired yet — s scans for something to pair"))
	}
	rows := h - 8
	if rows < 3 {
		rows = 3
	}
	start, end := window(s.sel, len(vis), rows)
	for i := start; i < end; i++ {
		d := s.devices[vis[i]]
		status := cFant.Render("○ not paired")
		switch {
		case d.connected:
			status = cCool.Render("● connected")
		case d.paired:
			status = cDim.Render("◐ paired")
		}
		line := fmt.Sprintf(" %-30s %-18s %s", truncate(displayName(&d), 30), d.mac, status)
		line = pad(truncate(line, w-6), w-6)
		if i == s.sel {
			list.WriteString(selOn.Render(line))
		} else {
			list.WriteString(cBase.Render(line))
		}
		list.WriteString("\n")
	}

	// The pane must render to a FIXED height regardless of how many devices
	// are visible — lipgloss .Height() only pads a shorter block, it never
	// truncates a taller one, so without clampLines a device list longer than
	// `budget` would grow the pane past it. A body whose height tracks the
	// live device count (short with nothing paired, tall mid-scan) shifts
	// canvas()'s vertical placement on every device found or lost — the
	// frame visibly jumps, worse here than anywhere else in sb because this
	// list's length swings the most. Same fix as viewMain's rail/items panes.
	budget := rows + 2 // paneTitle's 2 lines + up to `rows` device lines
	body := paneOn.Width(w - 2).Height(budget).Render(clampLines(list.String(), budget))

	// truncated to the pane width: a long device name in s.msg (e.g. mid-pair
	// or mid-connect) would otherwise widen this line past every other row
	// and shift canvas()'s horizontal centring — a sideways jump.
	foot := " " + cWarn.Render(truncate(s.msg, maxi(1, w-6)))
	keys := "↵ pair/connect/disconnect  s scan  r forget  q back"
	status := stTag.Render("BT") +
		stMid.Width(maxi(4, w-10)).Render(truncate(cDim.Render(keys), maxi(4, w-14)))

	return head + "\n" + body + "\n" + foot + "\n" + status
}
