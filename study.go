// study.go — read, answer, move on.
//
// The markdown is the database. Every lesson is a note under
// ~/vault/learn/<course>/NN-*.md; sb reads it, lets you answer its questions,
// and writes the same file back in place, so Obsidian sees every change and
// there is nothing to export.
//
// Three rules, all learned from using the previous version:
//
//   - Open where you left off. The order was decided when the course was
//     written. Making you re-choose it every session spends cognitive load on
//     a question that already has an answer.
//   - One thing on screen. Lessons on the left, the current question and your
//     answer in the middle. Nothing else.
//   - Every frame is exactly as tall as the terminal. lipgloss Height() is a
//     minimum, not a maximum: a pane taller than its allocation expands the
//     frame, the alt screen scrolls, and the display jumps as the selection
//     moves. Content is clipped before it reaches the layout.
//
// Question format in the note:
//
//	## Questions
//	### What does each factor of L = BᵀB mean?
//	A: the incidence matrix is the discrete gradient, its transpose the
//	   divergence, so L is div of grad.
//
// A `###` heading is a question. Everything after `A:` until the next heading
// is your answer.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"switchboard/decor"
)

// ---------------------------------------------------------------- model

type question struct {
	text   string
	answer string
	line   int // index in raw of the `###` heading
	end    int // last line belonging to this answer
}

func (q question) answered() bool { return strings.TrimSpace(q.answer) != "" }

type check struct {
	done bool
	text string
	line int
}

type chapter struct {
	path     string
	course   string
	title    string
	source   string
	num      int
	priority int
	status   string
	prose    []string
	checks   []check
	quest    []question
	diagram  string
	raw      []string
	statLine int
}

// toggle ticks a checkbox by rewriting that one line. The whole reason this
// is here rather than in the editor: `- [ ]` to `- [x]` is four keystrokes and
// a cursor position in vim, and one keystroke here.
func (c *chapter) toggle(i int) {
	if i < 0 || i >= len(c.checks) {
		return
	}
	k := &c.checks[i]
	k.done = !k.done
	from, to := "- [ ]", "- [x]"
	if !k.done {
		from, to = "- [x]", "- [ ]"
	}
	if k.line >= 0 && k.line < len(c.raw) {
		c.raw[k.line] = strings.Replace(c.raw[k.line], from, to, 1)
	}
}

func (c *chapter) checkCount() (int, int) {
	n := 0
	for _, k := range c.checks {
		if k.done {
			n++
		}
	}
	return n, len(c.checks)
}

// items is how many things there are to work through: checks then questions,
// in one list, because from the keyboard they are the same motion.
func (c *chapter) items() int { return len(c.checks) + len(c.quest) }

func (c *chapter) answeredCount() (int, int) {
	n := 0
	for _, q := range c.quest {
		if q.answered() {
			n++
		}
	}
	return n, len(c.quest)
}

// complete is the honest definition: answered, not merely opened.
func (c *chapter) complete() bool {
	if c.status == "done" {
		return true
	}
	a, at := c.answeredCount()
	k, kt := c.checkCount()
	if at+kt == 0 {
		return false
	}
	return a == at && k == kt
}

func (c *chapter) doneAll() bool {
	a, at := c.answeredCount()
	k, kt := c.checkCount()
	return at+kt > 0 && a == at && k == kt
}

var statusCycle = []string{"todo", "reading", "done", "parked"}

func (c *chapter) setStatus(s string) {
	c.status = s
	if c.statLine >= 0 && c.statLine < len(c.raw) {
		c.raw[c.statLine] = "status: " + s
	}
}

func (c *chapter) cycleStatus() {
	i := 0
	for j, s := range statusCycle {
		if s == c.status {
			i = j
		}
	}
	c.setStatus(statusCycle[(i+1)%len(statusCycle)])
}

// setAnswer rewrites one answer block, leaving every other byte alone so the
// Obsidian Git diff stays small.
func (c *chapter) setAnswer(i int, text string) {
	if i < 0 || i >= len(c.quest) {
		return
	}
	q := &c.quest[i]
	var body []string
	if strings.TrimSpace(text) != "" {
		for j, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			if j == 0 {
				body = append(body, "A: "+line)
			} else {
				body = append(body, "   "+line)
			}
		}
	}

	tail := append([]string{}, c.raw[q.end+1:]...)
	head := append([]string{}, c.raw[:q.line+1]...)
	c.raw = append(head, append(body, tail...)...)
	q.answer = text

	delta := (q.line + 1 + len(body)) - (q.end + 1)
	q.end = q.line + len(body)
	for j := i + 1; j < len(c.quest); j++ {
		c.quest[j].line += delta
		c.quest[j].end += delta
	}
	if c.statLine > q.line {
		c.statLine += delta
	}
}

func (c *chapter) save() error {
	return os.WriteFile(c.path, []byte(strings.Join(c.raw, "\n")), 0644)
}

// ---------------------------------------------------------------- parse

func learnDir() string {
	if v := os.Getenv("VAULT"); v != "" {
		return filepath.Join(v, "learn")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "vault", "learn")
}

func vaultDir() string { return filepath.Dir(learnDir()) }

func parseChapter(path string) (*chapter, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &chapter{
		path: path, course: filepath.Base(filepath.Dir(path)),
		title:    strings.TrimSuffix(filepath.Base(path), ".md"),
		status:   "todo",
		priority: 9,
		statLine: -1,
		raw:      strings.Split(string(b), "\n"),
	}

	inFront, seenFront := false, false
	section := ""
	for i, line := range c.raw {
		t := strings.TrimSpace(line)

		if t == "---" && !seenFront {
			if inFront {
				inFront, seenFront = false, true
			} else {
				inFront = true
			}
			continue
		}
		if inFront {
			k, v, ok := strings.Cut(t, ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "title":
				c.title = v
			case "source":
				c.source = v
			case "status":
				c.status, c.statLine = v, i
			case "priority":
				fmt.Sscanf(v, "%d", &c.priority)
			case "chapter":
				fmt.Sscanf(v, "%d", &c.num)
			case "diagram":
				c.diagram = v
			}
			continue
		}

		switch {
		case strings.HasPrefix(t, "- [ ]"):
			c.checks = append(c.checks, check{false, strings.TrimSpace(t[5:]), i})
		case strings.HasPrefix(t, "- [x]"), strings.HasPrefix(t, "- [X]"):
			c.checks = append(c.checks, check{true, strings.TrimSpace(t[5:]), i})
		case strings.HasPrefix(t, "### "):
			c.quest = append(c.quest, question{
				text: strings.TrimSpace(t[4:]), line: i, end: i,
			})
			section = "q"
		case strings.HasPrefix(t, "## "):
			section = strings.ToLower(strings.TrimPrefix(t, "## "))
		case section == "q" && len(c.quest) > 0:
			q := &c.quest[len(c.quest)-1]
			switch {
			case strings.HasPrefix(t, "A:"):
				q.answer = strings.TrimSpace(t[2:])
				q.end = i
			case q.answer != "" && t != "":
				q.answer += "\n" + t
				q.end = i
			}
		case (section == "text" || section == "chapter") && t != "":
			c.prose = append(c.prose, t)
		}
	}
	return c, nil
}

func loadChapters() []*chapter {
	var out []*chapter
	filepath.Walk(learnDir(), func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") || strings.HasPrefix(filepath.Base(p), "00-") {
			return nil
		}
		if c, err := parseChapter(p); err == nil {
			out = append(out, c)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.priority != b.priority {
			return a.priority < b.priority
		}
		if a.course != b.course {
			return a.course < b.course
		}
		return a.num < b.num
	})
	return out
}

// ---------------------------------------------------------------- state

type studyState struct {
	chapters []*chapter
	sel      int
	qsel     int
	editing  bool
	area     textarea.Model
	msg      string

	// see window()'s own comment — these persist so scrolling either pane
	// doesn't shift the frame.
	leftWinStart int
	mainWinStart int
}

func newStudyState() *studyState {
	s := &studyState{chapters: loadChapters()}
	ta := textarea.New()
	ta.Placeholder = "short answer — enough to show you took it in"
	ta.ShowLineNumbers = false
	ta.SetHeight(4)
	s.area = ta

	// open where you left off: first unfinished lesson, first unfinished item
	for i, c := range s.chapters {
		if c.complete() {
			continue
		}
		s.sel = i
		s.qsel = 0
		for j, k := range c.checks {
			if !k.done {
				s.qsel = j
				break
			}
		}
		if k, kt := c.checkCount(); k == kt {
			for j, q := range c.quest {
				if !q.answered() {
					s.qsel = len(c.checks) + j
					break
				}
			}
		}
		break
	}
	return s
}

func (s *studyState) current() *chapter {
	if len(s.chapters) == 0 {
		return nil
	}
	if s.sel >= len(s.chapters) {
		s.sel = len(s.chapters) - 1
	}
	return s.chapters[s.sel]
}

func (s *studyState) totals() (done, total, answered, questions int) {
	for _, c := range s.chapters {
		total++
		if c.complete() {
			done++
		}
		a, t := c.answeredCount()
		answered += a
		questions += t
	}
	return
}

// ---------------------------------------------------------------- update

func (m model) updateStudy(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.study
	if s == nil {
		m.mode = modeList
		return m, nil
	}
	c := s.current()

	if s.editing {
		switch msg.String() {
		case "esc":
			s.editing = false
			s.area.Blur()
			s.msg = "discarded"
			return m, nil
		case "ctrl+s":
			s.editing = false
			s.area.Blur()
			if c != nil {
				c.setAnswer(s.qsel-len(c.checks), s.area.Value())
				if c.doneAll() {
					c.setStatus("done")
					s.msg = "all answered — " + c.title + " done"
				} else {
					if c.status == "todo" {
						c.setStatus("reading")
					}
					s.msg = "saved"
				}
				if err := c.save(); err != nil {
					s.msg = "save failed: " + err.Error()
				}
			}
			return m, nil
		}
		var cmd tea.Cmd
		s.area, cmd = s.area.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.mode = modeList
		return m, nil

	case "j", "down", "tab":
		if c != nil && s.qsel < c.items()-1 {
			s.qsel++
		}
		return m, nil
	case "k", "up", "shift+tab":
		if s.qsel > 0 {
			s.qsel--
		}
		return m, nil

	case "J", "ctrl+n":
		if s.sel < len(s.chapters)-1 {
			s.sel++
			s.qsel = 0
		}
		return m, nil
	case "K", "ctrl+p":
		if s.sel > 0 {
			s.sel--
			s.qsel = 0
		}
		return m, nil

	case " ", "x":
		// a check: one keystroke, no markdown
		if c == nil || s.qsel >= len(c.checks) {
			return m, nil
		}
		c.toggle(s.qsel)
		if c.doneAll() {
			c.setStatus("done")
			s.msg = c.title + " — everything done"
		} else if c.status == "todo" {
			c.setStatus("reading")
			s.msg = "ticked"
		} else {
			s.msg = "ticked"
		}
		if err := c.save(); err != nil {
			s.msg = "save failed: " + err.Error()
		}
		return m, nil

	case "enter", "a":
		if c == nil {
			return m, nil
		}
		if s.qsel < len(c.checks) { // enter on a check ticks it too
			return m.updateStudy(tea.KeyMsg{Type: tea.KeySpace})
		}
		qi := s.qsel - len(c.checks)
		if qi >= len(c.quest) {
			return m, nil
		}
		s.editing = true
		s.area.SetValue(c.quest[qi].answer)
		s.area.Focus()
		s.msg = ""
		return m, textarea.Blink

	case "d":
		if c != nil && c.diagram != "" {
			if ds := newDiagramState(c.diagram); ds != nil {
				m.diagram = ds
				return m, nil
			}
			s.msg = "no diagram called " + c.diagram
		} else if c != nil {
			s.msg = "this lesson has no diagram"
		}
		return m, nil

	case "P":
		// park a lesson you have decided not to do now. Everything else about
		// status is derived from the work, so there is nothing else to set.
		if c != nil {
			if c.status == "parked" {
				c.setStatus("todo")
				s.msg = c.title + " — back on the list"
			} else {
				c.setStatus("parked")
				s.msg = c.title + " — parked"
			}
			c.save()
		}
		return m, nil

	case "e":
		if c != nil {
			return m, runExec("note", editorCmd()+" "+shellQuote(c.path))
		}
		return m, nil

	case "p":
		// a source is a PDF in the vault or a URL. zathura on a URL prints
		// "could not open document", which reads like a broken file rather
		// than the wrong tool.
		if c == nil || c.source == "" {
			s.msg = "no source recorded in the frontmatter"
			return m, nil
		}
		if strings.HasPrefix(c.source, "http://") || strings.HasPrefix(c.source, "https://") {
			return m, runExec("source", "w3m "+shellQuote(c.source))
		}
		p := filepath.Join(vaultDir(), c.source)
		if _, err := os.Stat(p); err != nil {
			s.msg = "source not on disk: " + c.source
			return m, nil
		}
		return m, runExec("pdf", "zathura "+shellQuote(p))

	case "r":
		m.study = newStudyState()
		m.study.msg = "reloaded"
		return m, nil
	}
	return m, nil
}

func editorCmd() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	return "nvim"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---------------------------------------------------------------- view

func bar(done, total, width int) string {
	if total == 0 || width <= 0 {
		return cFant.Render(strings.Repeat("─", maxi(0, width)))
	}
	filled := done * width / total
	return cCool.Render(strings.Repeat("█", filled)) +
		cFant.Render(strings.Repeat("░", width-filled))
}

// clip forces a block to exactly n lines of at most w visible cells. This is
// the whole fix for the jumping frame: nothing downstream may grow.
//
// Note it does NOT use truncate(): these lines are already styled, and
// truncate counts runes. Escape sequences are runes, so it would cut inside
// one and leave the line as three visible characters and a broken colour.
func clip(lines []string, n, w int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if i >= len(lines) {
			out = append(out, "")
			continue
		}
		l := lines[i]
		if lipgloss.Width(l) > w {
			l = decor.Take(l, w, lipgloss.Width) + "\x1b[0m"
		}
		out = append(out, l)
	}
	return out
}

func (m model) viewStudy() string {
	s := m.study
	if s == nil {
		return ""
	}
	w := m.contentWidth()
	listW := 30
	if w < 90 {
		listW = 22
	}
	mainW := w - listW - 8
	if mainW < 30 {
		mainW = 30
	}
	if m.prefs.ReadWidth > 0 && m.prefs.ReadWidth < mainW {
		mainW = m.prefs.ReadWidth
	}
	rows := m.h - 8
	if rows < 6 {
		rows = 6
	}

	cd, ct, qa, qt := s.totals()
	head := " " + gradient("▌ S T U D Y", true) + "  " +
		cDim.Render(fmt.Sprintf("%d/%d lessons · %d/%d answers", cd, ct, qa, qt)) +
		"\n " + bar(qa, qt, mini(w-4, 56))

	// ---- left: where you are
	var left []string
	selRow := 0
	lastCourse := ""
	for i, c := range s.chapters {
		if c.course != lastCourse {
			left = append(left, cPurp.Render(truncate("▌ "+c.course, listW)))
			lastCourse = c.course
		}
		mark := "○"
		if c.complete() {
			mark = "●"
		} else if c.status == "reading" {
			mark = "◐"
		}
		a, at := c.answeredCount()
		k, kt := c.checkCount()
		tail := ""
		if at+kt > 0 {
			tail = fmt.Sprintf(" %d/%d", a+k, at+kt)
		}
		line := pad(truncate(mark+" "+c.title, listW-len(tail)-1)+tail, listW)
		if i == s.sel {
			selRow = len(left)
			left = append(left, selOn.Render(line))
		} else {
			left = append(left, cDim.Render(line))
		}
	}
	lstart, _ := window(selRow, len(left), rows-2, &s.leftWinStart)
	left = clip(left[mini(lstart, len(left)):], rows-2, listW)

	// ---- centre: the lesson
	c := s.current()
	var main []string
	qRow := 0
	if c == nil {
		main = []string{cDim.Render("no lessons in " + learnDir())}
	} else {
		main = append(main, cCool.Bold(true).Render(truncate(c.title, mainW)))
		main = append(main, cFant.Render(strings.Repeat("─", mainW)))
		for _, p := range c.prose {
			for _, ln := range wrap(p, mainW) {
				main = append(main, cBase.Render(ln))
			}
			main = append(main, "")
		}
		if c.diagram != "" {
			main = append(main, cWarn.Render("◈ press d — interactive diagram: "+c.diagram))
			main = append(main, "")
		}

		if len(c.checks) > 0 {
			main = append(main, cPurp.Render("DO THESE"))
			main = append(main, "")
		}
		for i, k := range c.checks {
			if i == s.qsel {
				qRow = len(main)
			}
			box := cFant.Render("[ ]")
			if k.done {
				box = cCool.Render("[x]")
			}
			for j, ln := range wrap(k.text, mainW-5) {
				prefix := "    "
				if j == 0 {
					prefix = box + " "
				}
				st := cBase
				if k.done {
					st = cDim
				}
				if i == s.qsel {
					st = cCool.Bold(true)
				}
				main = append(main, prefix+st.Render(ln))
			}
			main = append(main, "")
		}

		if len(c.quest) > 0 {
			main = append(main, cPurp.Render("ANSWER THESE"))
			main = append(main, "")
		}
		if c.items() == 0 {
			main = append(main, cDim.Render("nothing to do in this lesson yet"))
		}
		for i, q := range c.quest {
			if len(c.checks)+i == s.qsel {
				qRow = len(main)
			}
			mark := cFant.Render("○")
			if q.answered() {
				mark = cCool.Render("●")
			}
			num := fmt.Sprintf("%d. ", i+1)
			for j, ln := range wrap(q.text, mainW-5) {
				prefix := "   "
				if j == 0 {
					prefix = mark + " " + num
				}
				st := cBase
				if len(c.checks)+i == s.qsel {
					st = cCool.Bold(true)
				}
				main = append(main, prefix+st.Render(ln))
			}
			if len(c.checks)+i == s.qsel && s.editing {
				for _, ln := range strings.Split(s.area.View(), "\n") {
					main = append(main, "    "+ln)
				}
			} else if q.answered() {
				for _, ln := range wrap(q.answer, mainW-6) {
					main = append(main, "     "+cDim.Render(ln))
				}
			}
			main = append(main, "")
		}
	}
	mstart, _ := window(qRow, len(main), rows-2, &s.mainWinStart)
	main = clip(main[mini(mstart, len(main)):], rows-2, mainW)

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		paneOff.Height(rows).Width(listW+2).Render(strings.Join(left, "\n")),
		paneOn.Height(rows).Width(mainW+2).Render(strings.Join(main, "\n")),
	)

	keys := "space tick  ↵ answer  jk item  JK lesson  d diagram  p source  e edit  P park  q back"
	if s.editing {
		keys = "ctrl+s save    esc discard"
	}
	status := stTag.Render("STUDY") +
		stMid.Width(maxi(4, w-14)).Render(truncate(
			s.msg+"   "+cDim.Render(keys), maxi(4, w-18)))
	return head + "\n" + body + "\n" + status
}

func wrap(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			out = append(out, line)
			line = w
		} else {
			line += " " + w
		}
	}
	return append(out, line)
}
