// pulsar.go — the pulsar mode: an audio visualiser you play rather than
// configure. It captures the desktop's own output (cava for the spectrum,
// parec for the raw waveform), runs it through the engine in pulsarfx.go, and
// gives you two keys that matter: `m` to mutate whatever is on screen into
// something adjacent, and `s` to snapshot it the instant it looks good —
// without ever pausing the picture.
//
// Presets are plain JSON in ~/.config/switchboard/pulsar/. Four built-ins are
// written there on first run; everything else is yours.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const pulsarBars = 64

// ---------------------------------------------------------------- messages

type pulsarCavaMsg []float64
type pulsarPCMMsg []float32
type pulsarEOFMsg struct{ src string }
type pulsarTickMsg time.Time

func pulsarTick(fps int) tea.Cmd {
	if fps < 8 {
		fps = 8
	}
	return tea.Tick(time.Second/time.Duration(fps), func(t time.Time) tea.Msg {
		return pulsarTickMsg(t)
	})
}

// ---------------------------------------------------------------- state

type pulsarState struct {
	eng   *plsEngine
	names []string  // preset file slugs, sorted
	idx   int       // which of names is loaded
	work  plsPreset // the live preset, including unsaved mutations
	undo  []plsPreset
	msg   string

	naming bool
	name   textinput.Model

	cavaCh   chan []float64
	pcmCh    chan []float32
	cavaProc *exec.Cmd
	pcmProc  *exec.Cmd
	tmpConf  string
	noCava   bool

	last time.Time
	w, h int
}

func newPulsarState(w, h int) *pulsarState {
	ensurePulsarPresets()
	s := &pulsarState{
		eng:  newPlsEngine(w, maxi(4, h-1)),
		w:    w,
		h:    h,
		name: newInput("preset name", "  save as  ", 40),
	}
	s.names = listPulsarPresets()

	// land on unknown-pleasures if it is there, else the first preset
	loaded := false
	for i, n := range s.names {
		if n == "unknown-pleasures" {
			if p, err := loadPulsarPreset(n); err == nil {
				s.work, s.idx, loaded = p, i, true
			}
			break
		}
	}
	if !loaded && len(s.names) > 0 {
		if p, err := loadPulsarPreset(s.names[0]); err == nil {
			s.work, loaded = p, true
		}
	}
	if !loaded {
		s.work = builtinPresets()[0]
	}
	s.eng.setPreset(s.work)
	return s
}

func (s *pulsarState) startCmds() tea.Cmd {
	var cmds []tea.Cmd
	if proc, ch, tmp, err := startPulsarCava(); err == nil {
		s.cavaProc, s.cavaCh, s.tmpConf = proc, ch, tmp
		cmds = append(cmds, pulsarReadCava(ch))
	} else {
		s.noCava = true
		s.msg = "no cava — spectrum layers idle (apt install cava)"
	}
	if proc, ch, err := startPulsarPCM(); err == nil {
		s.pcmProc, s.pcmCh = proc, ch
		cmds = append(cmds, pulsarReadPCM(ch))
	}
	cmds = append(cmds, pulsarTick(s.work.fps()))
	return tea.Batch(cmds...)
}

func (s *pulsarState) stop() {
	if s.cavaProc != nil && s.cavaProc.Process != nil {
		s.cavaProc.Process.Kill()
	}
	if s.pcmProc != nil && s.pcmProc.Process != nil {
		s.pcmProc.Process.Kill()
	}
	if s.tmpConf != "" {
		os.Remove(s.tmpConf)
	}
}

func (s *pulsarState) pushUndo() {
	s.undo = append(s.undo, clonePreset(s.work))
	if len(s.undo) > 40 {
		s.undo = append(s.undo[:0], s.undo[len(s.undo)-40:]...)
	}
}

func (s *pulsarState) cyclePreset(d int) {
	if len(s.names) == 0 {
		return
	}
	s.idx = (s.idx + d + len(s.names)) % len(s.names)
	if p, err := loadPulsarPreset(s.names[s.idx]); err == nil {
		s.work = p
		s.eng.setPreset(p)
		s.undo = nil
		s.msg = p.Name
	}
}

// ---------------------------------------------------------------- audio capture

const cavaConfTmpl = `[general]
mode = normal
framerate = 30
bars = %d
autosens = 1

[input]
method = pipewire
source = auto

[output]
method = raw
data_format = binary
bit_format = 16bit
channels = mono
`

func startPulsarCava() (*exec.Cmd, chan []float64, string, error) {
	if _, err := exec.LookPath("cava"); err != nil {
		return nil, nil, "", err
	}
	f, err := os.CreateTemp("", "sb-cava-*.conf")
	if err != nil {
		return nil, nil, "", err
	}
	fmt.Fprintf(f, cavaConfTmpl, pulsarBars)
	f.Close()

	cmd := exec.Command("cava", "-p", f.Name())
	out, err := cmd.StdoutPipe()
	if err != nil {
		os.Remove(f.Name())
		return nil, nil, "", err
	}
	if err := cmd.Start(); err != nil {
		os.Remove(f.Name())
		return nil, nil, "", err
	}

	ch := make(chan []float64, 8)
	go func() {
		defer close(ch)
		r := bufio.NewReader(out)
		buf := make([]byte, pulsarBars*2)
		for {
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			frame := make([]float64, pulsarBars)
			for i := 0; i < pulsarBars; i++ {
				frame[i] = float64(binary.LittleEndian.Uint16(buf[i*2:])) / 65535
			}
			select {
			case ch <- frame:
			default: // consumer is behind; drop this frame, keep the newest
			}
		}
	}()
	return cmd, ch, f.Name(), nil
}

func pulsarReadCava(ch chan []float64) tea.Cmd {
	return func() tea.Msg {
		f, ok := <-ch
		if !ok {
			return pulsarEOFMsg{"cava"}
		}
		return pulsarCavaMsg(f)
	}
}

func startPulsarPCM() (*exec.Cmd, chan []float32, error) {
	if _, err := exec.LookPath("parec"); err != nil {
		return nil, nil, err
	}
	cmd := exec.Command("parec", "-d", "@DEFAULT_MONITOR@",
		"--format=s16le", "--rate=22050", "--channels=1", "--raw")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	ch := make(chan []float32, 8)
	go func() {
		defer close(ch)
		r := bufio.NewReader(out)
		buf := make([]byte, 2048) // 1024 samples
		for {
			n, err := io.ReadFull(r, buf)
			if n >= 2 {
				win := make([]float32, n/2)
				for i := range win {
					win[i] = float32(int16(binary.LittleEndian.Uint16(buf[i*2:]))) / 32768
				}
				select {
				case ch <- win:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return cmd, ch, nil
}

func pulsarReadPCM(ch chan []float32) tea.Cmd {
	return func() tea.Msg {
		w, ok := <-ch
		if !ok {
			return pulsarEOFMsg{"pcm"}
		}
		return pulsarPCMMsg(w)
	}
}

// ---------------------------------------------------------------- preset i/o

func pulsarDir() string { return filepath.Join(confDir(), "pulsar") }

func ensurePulsarPresets() {
	dir := pulsarDir()
	os.MkdirAll(dir, 0755)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			return // already seeded
		}
	}
	for _, p := range builtinPresets() {
		savePulsarPreset(p)
	}
}

func listPulsarPresets() []string {
	entries, _ := os.ReadDir(pulsarDir())
	var names []string
	for _, e := range entries {
		if n := strings.TrimSuffix(e.Name(), ".json"); n != e.Name() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func loadPulsarPreset(slug string) (plsPreset, error) {
	b, err := os.ReadFile(filepath.Join(pulsarDir(), slug+".json"))
	if err != nil {
		return plsPreset{}, err
	}
	var p plsPreset
	if err := json.Unmarshal(b, &p); err != nil {
		return plsPreset{}, err
	}
	if p.Name == "" {
		p.Name = slug
	}
	return p, nil
}

func savePulsarPreset(p plsPreset) error {
	os.MkdirAll(pulsarDir(), 0755)
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(pulsarDir(), pulsarSlug(p.Name)+".json"), b, 0644)
}

func pulsarSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return fmt.Sprintf("pulsar-%04x", rand.Intn(0x10000))
	}
	return strings.Trim(b.String(), "-")
}

func clonePreset(p plsPreset) plsPreset {
	b, _ := json.Marshal(p)
	var out plsPreset
	json.Unmarshal(b, &out)
	return out
}

// ---------------------------------------------------------------- mutation

var numLiteralRE = regexp.MustCompile(`\b\d+(\.\d+)?\b`)

// mutatePreset returns a copy of p nudged into an adjacent region of the
// space: every numeric parameter jittered, some expression constants jittered,
// and — for a big mutation — a structural change too (reroll a scope formula,
// add/drop a warp, or repaint the palette).
func mutatePreset(p plsPreset, rng *rand.Rand, big bool) plsPreset {
	p = clonePreset(p)
	jit := func(v, frac float64) float64 { return v * (1 + rng.NormFloat64()*frac) }

	for li := range p.Layers {
		for k, v := range p.Layers[li].Params {
			switch k {
			case "points", "rows", "count":
				p.Layers[li].Params[k] = math.Round(clampf(jit(v, 0.15), 2, 4000))
			case "occlude", "logf", "dir", "layout":
				// discrete switches: flip occasionally rather than drift
				if rng.Float64() < 0.15 {
					p.Layers[li].Params[k] = math.Mod(v+1, 3)
				}
			default:
				p.Layers[li].Params[k] = jit(v, 0.12)
			}
		}
		if rng.Float64() < 0.3 {
			for k, src := range p.Layers[li].Expr {
				p.Layers[li].Expr[k] = jitterLiterals(src, rng)
			}
		}
	}
	p.FeedbackDecay = clampf(jit(p.FeedbackDecay+0.002, 0.1)+rng.NormFloat64()*0.01, 0, 0.985)
	p.BeatSensitivity = clampf(jit(p.BeatSensitivity, 0.06), 1.05, 2.2)

	if big {
		switch rng.Intn(4) {
		case 0:
			for li := range p.Layers {
				if p.Layers[li].Type == "scope" {
					t := scopeTemplates[rng.Intn(len(scopeTemplates))]
					p.Layers[li].Expr = map[string]string{"r": t[0], "th": t[1]}
				}
			}
		case 1:
			if hasLayer(p, "warp") {
				p.Layers = dropLayer(p, "warp")
			} else {
				p.Layers = append([]plsLayer{{Type: "warp", Params: map[string]float64{
					"zoom": 1.01 + rng.Float64()*0.03, "rot": rng.NormFloat64() * 0.01,
					"ripple": rng.Float64(), "rfreq": 3 + rng.Float64()*8,
				}}}, p.Layers...)
			}
		case 2:
			pals := []string{"theme", "phosphor", "amber", "ice", "fire", "mono"}
			p.Palette = pals[rng.Intn(len(pals))]
		case 3:
			p.Frame = jitterLiterals(p.Frame, rng)
		}
	}
	return p
}

func jitterLiterals(src string, rng *rand.Rand) string {
	if src == "" {
		return src
	}
	return numLiteralRE.ReplaceAllStringFunc(src, func(m string) string {
		f, err := strconv.ParseFloat(m, 64)
		if err != nil {
			return m
		}
		return strconv.FormatFloat(f*(1+rng.NormFloat64()*0.15), 'g', 4, 64)
	})
}

func hasLayer(p plsPreset, typ string) bool {
	for _, l := range p.Layers {
		if l.Type == typ {
			return true
		}
	}
	return false
}

func dropLayer(p plsPreset, typ string) []plsLayer {
	out := p.Layers[:0:0]
	for _, l := range p.Layers {
		if l.Type != typ {
			out = append(out, l)
		}
	}
	return out
}

var scopeTemplates = [][2]string{
	{"0.55 + 0.35*sin(i*tau*3 + t) + a*0.5", "i*tau + t*0.15"},
	{"0.6 + 0.3*sin(i*tau*5 - t*0.7)", "i*tau*2 + sin(t*0.3)"},
	{"0.4 + a*0.6 + 0.15*sin(i*tau*8 + t*2)", "i*tau + t*0.4"},
	{"abs(sin(i*tau*2 + t))*0.9", "i*tau*3 - t*0.2"},
	{"0.5 + 0.4*sin(i*tau + t)*cos(i*tau*4 - t*0.5)", "i*tau*1.5 + t*0.1"},
	{"0.3 + 0.6*fract(i*7 + t*0.2)", "i*tau + a*3"},
}

// ---------------------------------------------------------------- built-ins

func builtinPresets() []plsPreset {
	return []plsPreset{
		{
			Name: "unknown pleasures", Palette: "phosphor",
			FeedbackDecay: 0, BeatSensitivity: 1.3, FPS: 24, Contrast: 1, Renderer: "blocks",
			Layers: []plsLayer{{Type: "wave", Params: map[string]float64{
				"rows": 30, "gap": 1, "amp": 1.1, "occlude": 1, "squash": 0.55, "xzoom": 1, "hue": 0.55,
			}}},
		},
		{
			Name: "dialup", Palette: "amber",
			FeedbackDecay: 0.08, BeatSensitivity: 1.4, FPS: 24, Contrast: 0.85, Renderer: "blocks",
			Layers: []plsLayer{
				{Type: "spectro", Params: map[string]float64{"logf": 1, "gain": 1.4, "rate": 1, "dir": 1, "hue": 0.14}},
				{Type: "bars", Params: map[string]float64{"count": 40, "gain": 1.2, "layout": 0, "hue": 0.1}},
			},
		},
		{
			Name: "superscope", Palette: "theme",
			FeedbackDecay: 0.82, BeatSensitivity: 1.3, FPS: 30, Contrast: 1.1, Renderer: "blocks",
			Frame: "sp = sp + 0.02 + bass*0.12",
			Layers: []plsLayer{
				{Type: "warp", Params: map[string]float64{"zoom": 1.012, "rot": 0.004, "ripple": 0.4, "rfreq": 7}},
				{Type: "scope", Params: map[string]float64{"points": 260, "thick": 1, "gain": 1, "audio": 1, "hue": 0.5},
					Expr: map[string]string{
						"r":  "0.55 + 0.3*sin(i*tau*3 + sp) + a*0.5",
						"th": "i*tau + sp*0.3",
					}},
			},
		},
		{
			Name: "geiss", Palette: "ice",
			FeedbackDecay: 0.9, BeatSensitivity: 1.25, FPS: 24, Contrast: 1.2, Renderer: "blocks",
			Layers: []plsLayer{
				{Type: "warp", Params: map[string]float64{"zoom": 1.02, "rot": 0.01, "ripple": 0.6, "rfreq": 5}},
				{Type: "plasma", Params: map[string]float64{"scale": 1.2, "speed": 0.6, "bass": 1.5, "swirl": 1, "mix": 0.5, "hue": 0}},
				{Type: "strobe", Params: map[string]float64{"beatFlash": 0.35, "hueKick": 0.08}},
			},
		},
	}
}

func isBuiltinName(name string) bool {
	for _, p := range builtinPresets() {
		if p.Name == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- update

func (m model) updatePulsar(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.pulsar
	if s == nil {
		m.mode = modeList
		return m, nil
	}

	if s.naming {
		switch msg.String() {
		case "esc":
			s.naming = false
			s.name.Blur()
			s.name.SetValue("")
		case "enter":
			nm := strings.TrimSpace(s.name.Value())
			s.naming = false
			s.name.Blur()
			s.name.SetValue("")
			if nm != "" {
				s.work.Name = nm
				s.saveWorking()
			}
		default:
			var cmd tea.Cmd
			s.name, cmd = s.name.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "esc", "ctrl+c":
		s.stop()
		m.pulsar = nil
		m.mode = modeList
		return m, nil
	case " ":
		s.eng.frozen = !s.eng.frozen
		s.msg = pickStr(s.eng.frozen, "frozen", "running")
	case "m":
		s.pushUndo()
		s.work = mutatePreset(s.work, s.eng.rng, false)
		s.eng.setPreset(s.work)
		s.msg = "mutated"
	case "M":
		s.pushUndo()
		s.work = mutatePreset(s.work, s.eng.rng, true)
		s.eng.setPreset(s.work)
		s.msg = "mutated hard"
	case "u":
		if n := len(s.undo); n > 0 {
			s.work = s.undo[n-1]
			s.undo = s.undo[:n-1]
			s.eng.setPreset(s.work)
			s.msg = "undo"
		}
	case "s":
		if s.work.Name == "" || isBuiltinName(s.work.Name) {
			s.work.Name = fmt.Sprintf("pulsar-%04x", s.eng.rng.Intn(0x10000))
		}
		s.saveWorking()
	case "S":
		s.naming = true
		s.name.Focus()
		return m, textinput.Blink
	case "[", "left":
		s.cyclePreset(-1)
	case "]", "right":
		s.cyclePreset(1)
	case "c":
		pals := []string{"theme", "phosphor", "amber", "ice", "fire", "mono"}
		s.work.Palette = pals[(indexOfStr(pals, s.work.Palette)+1+len(pals))%len(pals)]
		s.eng.preset.Palette = s.work.Palette
		s.msg = "palette " + s.work.Palette
	case "r":
		if len(s.names) > 0 {
			if p, err := loadPulsarPreset(s.names[s.idx]); err == nil {
				s.work = p
				s.eng.setPreset(p)
				s.undo = nil
				s.msg = "reloaded " + p.Name
			}
		}
	}
	return m, nil
}

func (s *pulsarState) saveWorking() {
	if err := savePulsarPreset(s.work); err != nil {
		s.msg = "save failed: " + err.Error()
		return
	}
	slug := pulsarSlug(s.work.Name)
	s.names = listPulsarPresets()
	if i := indexOfStr(s.names, slug); i >= 0 {
		s.idx = i
	}
	s.msg = "saved " + slug
}

// ---------------------------------------------------------------- view

func (m model) viewPulsar() string {
	if m.pulsar == nil {
		return ""
	}
	return m.pulsar.view(m.w, m.h)
}

func (s *pulsarState) view(w, h int) string {
	if s.eng == nil || w < 8 || h < 6 {
		return ""
	}
	viz := s.eng.renderBlocks()

	if s.naming {
		return viz + "\n " + s.name.View()
	}

	beat := "·"
	if s.eng.audio.beat {
		beat = "●"
	}
	name := s.work.Name
	if name == "" {
		name = "untitled"
	}
	tail := "m mutate  M hard  u undo  s save  S name  [ ] presets  c palette  space freeze  q back"
	if s.msg != "" {
		tail = s.msg
	}
	body := fmt.Sprintf("%s  %s %dfps  %s  %s", name, beat, s.work.fps(), s.work.Palette, tail)
	// "PULSAR" tag is 8 cells wide with its padding; leave room for stMid's own
	// padding too so the bar lands exactly at w and never wraps.
	status := stTag.Render("PULSAR") +
		stMid.Width(maxi(4, w-10)).Render(truncate(body, maxi(4, w-14)))
	return viz + "\n" + status
}

// ---------------------------------------------------------------- helpers

func pickStr(b bool, a, c string) string {
	if b {
		return a
	}
	return c
}

func indexOfStr(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}
