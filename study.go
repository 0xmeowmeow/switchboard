// study.go — a reading tracker inside sb.
//
// The markdown IS the database. Every chapter is a note under
// ~/vault/learn/<doc>/NN-*.md with YAML frontmatter and GitHub-style
// checkboxes. This mode reads those files, lets you tick boxes and change
// status, and writes the same files back — so Obsidian sees every change
// immediately and there is nothing to export.
//
// Writes are deliberately minimal: only the `status:` line and the `- [ ]`
// markers are touched, every other byte is preserved. That keeps the diffs
// readable in Obsidian Git.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------- model

type check struct {
	done bool
	text string
	line int // index into raw, so we can write back in place
}

type chapter struct {
	path     string
	doc      string // parent directory, e.g. laplacian-reader
	title    string
	source   string // the PDF, relative to the vault
	num      int
	priority int
	status   string // todo, reading, done, parked
	checks   []check
	gotcha   []string
	notes    []string
	raw      []string
	statLine int // index of the status: line in raw
}

func (c *chapter) doneCount() (int, int) {
	n := 0
	for _, k := range c.checks {
		if k.done {
			n++
		}
	}
	return n, len(c.checks)
}

// complete is the honest definition: a chapter with checks is done when they
// pass, not when it has been marked read.
func (c *chapter) complete() bool {
	if c.status == "done" {
		return true
	}
	d, t := c.doneCount()
	return t > 0 && d == t
}

var statusCycle = []string{"todo", "reading", "done", "parked"}

func (c *chapter) cycleStatus(dir int) {
	i := 0
	for j, s := range statusCycle {
		if s == c.status {
			i = j
		}
	}
	i = (i + dir + len(statusCycle)) % len(statusCycle)
	c.status = statusCycle[i]
	if c.statLine >= 0 && c.statLine < len(c.raw) {
		c.raw[c.statLine] = "status: " + c.status
	}
}

func (c *chapter) toggle(i int) {
	if i < 0 || i >= len(c.checks) {
		return
	}
	k := &c.checks[i]
	k.done = !k.done
	old, new := "- [ ]", "- [x]"
	if !k.done {
		old, new = "- [x]", "- [ ]"
	}
	if k.line >= 0 && k.line < len(c.raw) {
		c.raw[k.line] = strings.Replace(c.raw[k.line], old, new, 1)
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

func frontmatterValue(line, key string) (string, bool) {
	if !strings.HasPrefix(line, key+":") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, key+":")), true
}

func parseChapter(path string) (*chapter, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &chapter{
		path:     path,
		doc:      filepath.Base(filepath.Dir(path)),
		title:    strings.TrimSuffix(filepath.Base(path), ".md"),
		status:   "todo",
		priority: 9,
		statLine: -1,
		raw:      strings.Split(string(b), "\n"),
	}

	inFront, seenFront := false, false
	section := ""
	for i, line := range c.raw {
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" && !seenFront {
			if inFront {
				inFront, seenFront = false, true
			} else {
				inFront = true
			}
			continue
		}
		if inFront {
			if v, ok := frontmatterValue(trimmed, "title"); ok {
				c.title = v
			}
			if v, ok := frontmatterValue(trimmed, "source"); ok {
				c.source = v
			}
			if v, ok := frontmatterValue(trimmed, "status"); ok {
				c.status, c.statLine = v, i
			}
			if v, ok := frontmatterValue(trimmed, "priority"); ok {
				fmt.Sscanf(v, "%d", &c.priority)
			}
			if v, ok := frontmatterValue(trimmed, "chapter"); ok {
				fmt.Sscanf(v, "%d", &c.num)
			}
			continue
		}

		if strings.HasPrefix(trimmed, "## ") {
			section = strings.ToLower(strings.TrimPrefix(trimmed, "## "))
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "- [ ]"):
			c.checks = append(c.checks, check{false, strings.TrimSpace(trimmed[5:]), i})
		case strings.HasPrefix(trimmed, "- [x]"), strings.HasPrefix(trimmed, "- [X]"):
			c.checks = append(c.checks, check{true, strings.TrimSpace(trimmed[5:]), i})
		case strings.HasPrefix(trimmed, ">"):
			c.gotcha = append(c.gotcha, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))
		case section == "notes" && trimmed != "":
			c.notes = append(c.notes, trimmed)
		}
	}
	return c, nil
}

func loadChapters() []*chapter {
	var out []*chapter
	root := learnDir()
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
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
	// priority first — that ordering is the whole point of the reading plan
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].priority != out[j].priority {
			return out[i].priority < out[j].priority
		}
		if out[i].doc != out[j].doc {
			return out[i].doc < out[j].doc
		}
		return out[i].num < out[j].num
	})
	return out
}

// ---------------------------------------------------------------- state

type studyFocus int

const (
	focusChapters studyFocus = iota
	focusChecks
)

type studyState struct {
	chapters []*chapter
	sel      int
	checkSel int
	focus    studyFocus
	msg      string
	filter   string
	shown    []int
}

func newStudyState() *studyState {
	s := &studyState{chapters: loadChapters()}
	s.refilter()
	// open on the first thing that is not finished
	for i, idx := range s.shown {
		if !s.chapters[idx].complete() {
			s.sel = i
			break
		}
	}
	return s
}

func (s *studyState) refilter() {
	q := strings.ToLower(s.filter)
	s.shown = s.shown[:0]
	for i, c := range s.chapters {
		if q == "" ||
			strings.Contains(strings.ToLower(c.title), q) ||
			strings.Contains(strings.ToLower(c.doc), q) ||
			strings.Contains(strings.ToLower(c.status), q) {
			s.shown = append(s.shown, i)
		}
	}
	if s.sel >= len(s.shown) {
		s.sel = maxi(0, len(s.shown)-1)
	}
}

func (s *studyState) current() *chapter {
	if len(s.shown) == 0 {
		return nil
	}
	return s.chapters[s.shown[s.sel]]
}

// totals across everything, for the progress bar
func (s *studyState) totals() (chaptersDone, chapters, checksDone, checks int) {
	for _, c := range s.chapters {
		chapters++
		if c.complete() {
			chaptersDone++
		}
		d, t := c.doneCount()
		checksDone += d
		checks += t
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

	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.mode = modeList
		s.msg = ""
		return m, nil

	case "tab":
		if s.focus == focusChapters && c != nil && len(c.checks) > 0 {
			s.focus = focusChecks
			s.checkSel = 0
		} else {
			s.focus = focusChapters
		}
		return m, nil

	case "h", "left":
		s.focus = focusChapters
		return m, nil

	case "l", "right":
		if c != nil && len(c.checks) > 0 {
			s.focus = focusChecks
		}
		return m, nil

	case "j", "down":
		if s.focus == focusChapters {
			if s.sel < len(s.shown)-1 {
				s.sel++
				s.checkSel = 0
			}
		} else if c != nil && s.checkSel < len(c.checks)-1 {
			s.checkSel++
		}
		return m, nil

	case "k", "up":
		if s.focus == focusChapters {
			if s.sel > 0 {
				s.sel--
				s.checkSel = 0
			}
		} else if s.checkSel > 0 {
			s.checkSel--
		}
		return m, nil

	case "g":
		if s.focus == focusChapters {
			s.sel, s.checkSel = 0, 0
		} else {
			s.checkSel = 0
		}
		return m, nil

	case "G":
		if s.focus == focusChapters {
			s.sel = maxi(0, len(s.shown)-1)
		} else if c != nil {
			s.checkSel = maxi(0, len(c.checks)-1)
		}
		return m, nil

	case " ", "x", "enter":
		// tick the check under the cursor, or the first unticked one
		if c == nil || len(c.checks) == 0 {
			return m, nil
		}
		i := s.checkSel
		if s.focus == focusChapters {
			i = -1
			for j, k := range c.checks {
				if !k.done {
					i = j
					break
				}
			}
			if i < 0 {
				i = 0
			}
		}
		c.toggle(i)
		// ticking the last check finishes the chapter, which is the rule the
		// documents themselves state: a chapter is done when its checks pass
		if d, t := c.doneCount(); t > 0 && d == t && c.status != "done" {
			c.cycleStatusTo("done")
			s.msg = c.title + " — all checks pass, marked done"
		} else if c.status == "todo" {
			c.cycleStatusTo("reading")
		}
		if err := c.save(); err != nil {
			s.msg = "save failed: " + err.Error()
		} else if s.msg == "" {
			s.msg = "saved"
		}
		return m, nil

	case "s":
		if c != nil {
			c.cycleStatus(1)
			if err := c.save(); err != nil {
				s.msg = "save failed: " + err.Error()
			} else {
				s.msg = c.title + " → " + c.status
			}
		}
		return m, nil

	case "S":
		if c != nil {
			c.cycleStatus(-1)
			c.save()
			s.msg = c.title + " → " + c.status
		}
		return m, nil

	case "e":
		if c != nil {
			return m, runExec("note", editorCmd()+" "+shellQuote(c.path))
		}
		return m, nil

	case "p":
		if c != nil && c.source != "" {
			p := filepath.Join(vaultDir(), c.source)
			return m, runExec("pdf", "zathura "+shellQuote(p))
		}
		s.msg = "no source PDF recorded in the frontmatter"
		return m, nil

	case "r":
		m.study = newStudyState()
		m.study.msg = "reloaded from disk"
		return m, nil

	case "/":
		m.studyFiltering = true
		m.mode = modeFilter
		m.filter.SetValue(s.filter)
		m.filter.Focus()
		return m, nil
	}
	return m, nil
}

func (c *chapter) cycleStatusTo(want string) {
	for c.status != want {
		before := c.status
		c.cycleStatus(1)
		if c.status == before {
			return
		}
	}
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
	if total == 0 {
		return cFant.Render(strings.Repeat("─", width))
	}
	filled := done * width / total
	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < filled {
			b.WriteString(cCool.Render("█"))
		} else {
			b.WriteString(cFant.Render("░"))
		}
	}
	return b.String()
}

func statusStyle(st string) lipgloss.Style {
	switch st {
	case "done":
		return cCool
	case "reading":
		return cWarn
	case "parked":
		return cFant
	}
	return cDim
}

func (m model) viewStudy() string {
	s := m.study
	if s == nil {
		return ""
	}
	w := m.w
	if w <= 0 {
		w = 80
	}
	listW := 34
	if w < 90 {
		listW = 26
	}
	detailW := w - listW - 10
	if detailW < 24 {
		detailW = 24
	}
	rows := m.h - 12
	if rows < 6 {
		rows = 6
	}

	cd, ct, kd, kt := s.totals()
	head := " " + gradient("▌ S T U D Y", true) + "  " +
		cDim.Render(fmt.Sprintf("%d/%d chapters   %d/%d checks", cd, ct, kd, kt)) +
		"\n " + bar(kd, kt, mini(w-4, 60))

	// ---- left: chapters
	var left strings.Builder
	left.WriteString(paneTitle("chapters", listW, s.focus == focusChapters) + "\n")
	start, end := window(s.sel, len(s.shown), rows-2)
	lastDoc := ""
	for i := start; i < end; i++ {
		c := s.chapters[s.shown[i]]
		if c.doc != lastDoc {
			left.WriteString(cPurp.Render(truncate("▌ "+c.doc, listW)) + "\n")
			lastDoc = c.doc
		}
		mark := "○"
		if c.complete() {
			mark = "●"
		} else if c.status == "reading" {
			mark = "◐"
		}
		d, t := c.doneCount()
		tail := ""
		if t > 0 {
			tail = fmt.Sprintf(" %d/%d", d, t)
		}
		label := fmt.Sprintf("%s %s", mark, truncate(c.title, listW-6-len(tail)))
		line := pad(label+tail, listW)
		if i == s.sel && s.focus == focusChapters {
			left.WriteString(selOn.Render(line))
		} else if i == s.sel {
			left.WriteString(selOff.Render(line))
		} else {
			left.WriteString(statusStyle(c.status).Render(line))
		}
		left.WriteString("\n")
	}

	// ---- right: the chapter itself
	var right strings.Builder
	c := s.current()
	if c == nil {
		right.WriteString(cDim.Render("no chapters found in " + learnDir()))
	} else {
		right.WriteString(paneTitle(c.title, detailW, s.focus == focusChecks) + "\n")
		right.WriteString(cDim.Render(pad(fmt.Sprintf("%s · ch %d · priority %d · ",
			c.doc, c.num, c.priority), detailW-8)))
		right.WriteString(statusStyle(c.status).Render(c.status) + "\n\n")

		if len(c.checks) > 0 {
			right.WriteString(cPurp.Render("CHECK IT YOURSELF") + "\n")
			right.WriteString(cFant.Render("done when these pass, not when it is read") + "\n\n")
			for i, k := range c.checks {
				box := cFant.Render("[ ]")
				if k.done {
					box = cCool.Render("[x]")
				}
				body := wrap(k.text, detailW-6)
				for j, ln := range body {
					prefix := "    "
					if j == 0 {
						prefix = box + " "
					}
					style := cBase
					if k.done {
						style = cDim
					}
					if i == s.checkSel && s.focus == focusChecks {
						style = cCool.Bold(true)
					}
					right.WriteString(prefix + style.Render(ln) + "\n")
				}
			}
			right.WriteString("\n")
		}

		if len(c.gotcha) > 0 {
			right.WriteString(cWarn.Render("THE GOTCHA") + "\n")
			for _, g := range c.gotcha {
				if strings.TrimSpace(g) == "" {
					continue
				}
				for _, ln := range wrap(g, detailW-2) {
					right.WriteString(cWarn.Render(ln) + "\n")
				}
			}
			right.WriteString("\n")
		}

		if len(c.notes) > 0 {
			right.WriteString(cPurp.Render("YOUR NOTES") + "\n")
			for _, n := range c.notes {
				for _, ln := range wrap(n, detailW-2) {
					right.WriteString(cBase.Render(ln) + "\n")
				}
			}
		}
	}

	leftPane, rightPane := paneOff, paneOn
	if s.focus == focusChapters {
		leftPane, rightPane = paneOn, paneOff
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		leftPane.Height(rows).Width(listW+2).Render(left.String()),
		rightPane.Height(rows).Width(detailW+2).Render(right.String()),
	)

	status := stTag.Render("STUDY") +
		stMid.Width(maxi(4, w-14)).Render(truncate(
			s.msg+"   "+cDim.Render("space tick  s status  e note  p pdf  / filter  r reload  q back"),
			maxi(4, w-18)))
	return head + "\n" + body + "\n" + status
}

// wrap breaks text at word boundaries to a column width.
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
