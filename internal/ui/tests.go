package ui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/catalog"
	"testdash/internal/logformat"
	"testdash/internal/style"
)

var (
	markPicked   = [2]string{"●", "bold green"} // distinct from a result's ✔
	markPartly   = [2]string{"◐", "bold yellow"}
	markUnpicked = [2]string{"·", "dim"}
	notSearched  = regexp.MustCompile(`[^a-z0-9]`) // spaces and punctuation: "signup" finds "a sign-up stores"
	showModes    = []string{"all", "failed", "not passed"}
)

// Nerd Font chevrons, as neo-tree and lazygit draw them; most fonts have no centred down caret otherwise.
var caretClosed, caretOpen = func() (string, string) {
	if os.Getenv("TESTDASH_NERD_FONT") != "" {
		return " ", " "
	}
	return "▸ ", "▾ "
}()

func markStyle(name string) style.Style {
	switch name {
	case "bold green":
		return style.BoldGreen
	case "bold yellow":
		return style.BoldYellow
	}
	return style.Dim
}

func searchable(text string) string {
	return notSearched.ReplaceAllString(strings.ToLower(text), "")
}

// testsView is the current task's tests as a tree. Its state lives on the Model (entries, selected, status),
// so the picker and the run screen show the same tests and selection. When picking, typing filters the tree,
// space selects and Enter moves on to the Run button; on the run screen the tree follows the run, and Enter runs
// a test again.
type testsView struct {
	picking        bool
	text           string
	findInput      *lineInput
	findShown      bool
	show           string
	visible        map[string]bool
	children       map[string][]string
	expanded       map[string]bool
	cursor         string
	offset         int
	height         int
	scrollToCursor bool
	flat           []treeLine
	flatDirty      bool
	detail         *textbox
	findSeq        int
}

type treeLine struct {
	id         string
	depth      int
	lastAt     []bool // whether each node on the way down is its parent's last child
	expandable bool
}

func newTestsView(picking bool) *testsView {
	return &testsView{picking: picking, show: "all", visible: map[string]bool{}, children: map[string][]string{},
		expanded: map[string]bool{}, detail: &textbox{},
		findInput: &lineInput{placeholder: "find tests by name or class, Esc clears"}}
}

func (v *testsView) empty() bool {
	return len(v.visible) == 0
}

func (v *testsView) main() any {
	return v
}

func (v *testsView) focusables(m *Model) []any {
	if v.picking {
		return []any{v}
	}
	if v.findShown {
		return []any{v.findInput, v, v.detail}
	}
	return []any{v, v.detail}
}

func (v *testsView) clear() {}

func (v *testsView) append(*Model, []*logformat.Line) {}

// rebuild works out which tests show; reset puts every node's expansion back to its default, as a new filter
// or task does.
func (v *testsView) rebuild(m *Model, reset bool) {
	v.visible = v.computeVisible(m)
	v.children = map[string][]string{}
	var add func(parent string, e *catalog.Entry)
	add = func(parent string, e *catalog.Entry) {
		if !v.visible[e.ID] {
			return
		}
		v.children[parent] = append(v.children[parent], e.ID)
		for _, kid := range m.kids[e.ID] {
			add(e.ID, kid)
		}
	}
	for _, top := range m.kids[""] {
		add("", top)
	}
	expand := v.text != "" || v.show != "all" || !v.picking
	if reset {
		v.expanded = map[string]bool{}
	}
	for id := range v.visible {
		if _, known := v.expanded[id]; !known && len(m.kids[id]) > 0 {
			v.expanded[id] = expand || m.entries[id].Kind == "template"
		}
	}
	if reset && len(v.visible) <= 40 {
		for id := range v.visible {
			v.expanded[id] = true
		}
	}
	v.flatDirty = true
	if !v.visible[v.cursor] {
		v.cursor = ""
		if tops := v.children[""]; len(tops) > 0 {
			v.cursor = tops[0]
		}
		v.offset = 0
	}
	v.scrollToCursor = true
}

func (v *testsView) computeVisible(m *Model) map[string]bool {
	visible := map[string]bool{}
	needle := searchable(v.text)
	var walk func(e *catalog.Entry) bool
	walk = func(e *catalog.Entry) bool {
		kids := false
		for _, kid := range m.kids[e.ID] {
			if walk(kid) {
				kids = true
			}
		}
		classMatch := e.Kind == "class" && needle != "" && strings.Contains(searchable(e.ClassName), needle)
		own := e.IsLeaf() && v.wanted(m, e) && (needle == "" || strings.Contains(searchable(e.Name), needle) ||
			strings.Contains(searchable(e.ClassName), needle))
		if !own && !kids && !classMatch {
			return false
		}
		visible[e.ID] = true
		if classMatch {
			for _, id := range m.descendants(e.ID) {
				if v.wanted(m, m.entries[id]) {
					visible[id] = true
				}
			}
		}
		return true
	}
	for _, top := range m.kids[""] {
		walk(top)
	}
	return visible
}

func (v *testsView) wanted(m *Model, e *catalog.Entry) bool {
	status := m.status(e.ID)
	if !v.picking && status == "absent" {
		return false // the run screen shows the run's tests only
	}
	switch v.show {
	case "failed":
		return status == "failed" || status == "aborted"
	case "not passed":
		return status != "passed" && status != "skipped"
	}
	return true
}

// changed refreshes the details of the test under the cursor when the run changed it.
func (v *testsView) changed(m *Model, changed map[string]bool) {
	if !v.picking && changed[v.cursor] {
		v.detail.set(v.cursor, m.describeTest(v.cursor, false))
	}
}

func (v *testsView) lines() []treeLine {
	if !v.flatDirty && v.flat != nil {
		return v.flat
	}
	v.flat = v.flat[:0]
	var walk func(parent string, depth int, lastAt []bool)
	walk = func(parent string, depth int, lastAt []bool) {
		kids := v.children[parent]
		for i, id := range kids {
			path := append(append([]bool{}, lastAt...), i == len(kids)-1)
			line := treeLine{id: id, depth: depth, lastAt: path, expandable: len(v.children[id]) > 0}
			v.flat = append(v.flat, line)
			if line.expandable && v.expanded[id] {
				walk(id, depth+1, path)
			}
		}
	}
	walk("", 0, nil)
	v.flatDirty = false
	return v.flat
}

func (v *testsView) cursorIndex() int {
	for i, line := range v.lines() {
		if line.id == v.cursor {
			return i
		}
	}
	return 0
}

func (v *testsView) moveTo(i int) {
	lines := v.lines()
	if len(lines) == 0 {
		return
	}
	v.cursor = lines[max(0, min(i, len(lines)-1))].id
	v.scrollToCursor = true
}

func (v *testsView) toggle(id string) {
	v.expanded[id] = !v.expanded[id]
	v.flatDirty = true
}

func (v *testsView) label(m *Model, e *catalog.Entry) string {
	if e.Kind == "class" {
		leaves := m.leavesUnder(e.ID)
		statuses := make([]string, len(leaves))
		failed := false
		for i, leaf := range leaves {
			statuses[i] = m.status(leaf)
			failed = failed || statuses[i] == "failed" || statuses[i] == "aborted"
		}
		name := style.Bold.Render(v.className(m, e))
		if v.picking {
			picked := 0
			for _, leaf := range leaves {
				if m.selected[leaf] {
					picked++
				}
			}
			mark := markUnpicked
			if len(leaves) > 0 && picked == len(leaves) {
				mark = markPicked
			} else if picked > 0 {
				mark = markPartly
			}
			counted := fmt.Sprint(len(leaves))
			if picked > 0 && picked < len(leaves) {
				counted = fmt.Sprintf("%d/%d", picked, len(leaves))
			}
			text := markStyle(mark[1]).Render(mark[0]+" ") + name + style.Dim.Render("  "+counted)
			if failed {
				text += style.Red.Render("  ✘ last run")
			}
			return text
		}
		inRun, done, running := 0, 0, false
		for _, s := range statuses {
			if s == "absent" {
				continue
			}
			inRun++
			switch s {
			case "passed", "failed", "aborted", "skipped":
				done++
			case "running":
				running = true
			}
		}
		status := "pending"
		switch {
		case failed:
			status = "failed"
		case running:
			status = "running"
		case inRun > 0 && done == inRun:
			status = "passed"
		}
		mark, s := icon(status)
		return s.Render(mark+" ") + name + style.Dim.Render(fmt.Sprintf("  %d/%d", done, inRun))
	}
	status := m.status(e.ID)
	name := cleanText(e.Name)
	if v.picking {
		text := "  " + name
		if m.selectable[e.ID] {
			mark := markUnpicked
			if m.selected[e.ID] {
				mark = markPicked
			}
			text = markStyle(mark[1]).Render(mark[0]+" ") + name
		}
		switch status {
		case "passed", "failed", "aborted", "skipped":
			mark, s := icon(status)
			text += s.Render("  " + mark)
		}
		return text
	}
	mark, s := icon(status)
	took := ""
	if state := m.state(e.ID); state.StartedAt != 0 {
		millis, _ := state.Took(nowMillis())
		took = duration(millis)
	}
	return s.Render(mark+" ") + style.Dim.Render(fmt.Sprintf("%6s ", took)) + name
}

// className is a nested class by its own name, since the tree already shows the class it is in.
func (v *testsView) className(m *Model, e *catalog.Entry) string {
	if parent := m.entries[e.Parent]; parent != nil && parent.Kind == "class" {
		return cleanText(e.Name)
	}
	return e.ShortClass()
}

func guides(line treeLine) string {
	if line.depth == 0 {
		return ""
	}
	var b strings.Builder
	for k := 1; k < line.depth; k++ {
		if line.lastAt[k] {
			b.WriteString("   ")
		} else {
			b.WriteString("│  ")
		}
	}
	if line.lastAt[line.depth] {
		b.WriteString("└─ ")
	} else {
		b.WriteString("├─ ")
	}
	return b.String()
}

func (v *testsView) treeView(m *Model, r rect, focused bool) []string {
	lines := v.lines()
	v.height = r.h
	cursor := v.cursorIndex()
	if v.scrollToCursor {
		if cursor < v.offset {
			v.offset = cursor
		} else if cursor >= v.offset+r.h {
			v.offset = cursor - r.h + 1
		}
		v.scrollToCursor = false
	}
	v.offset = max(0, min(v.offset, len(lines)-r.h))
	var out []string
	for i := v.offset; i < len(lines) && len(out) < r.h; i++ {
		line := lines[i]
		entry := m.entries[line.id]
		if entry == nil {
			continue
		}
		guide := style.LineText.Render(guides(line))
		caret := "  "
		if line.expandable {
			caret = caretClosed
			if v.expanded[line.id] {
				caret = caretOpen
			}
			caret = style.Dim.Render(caret)
		}
		label := caret + v.label(m, entry)
		if i == cursor && v.cursor != "" {
			width := max(0, r.w-ansi.StringWidth(guides(line)))
			highlight := style.Bg(style.CursorBlur)
			if focused {
				highlight = style.BoldBg(style.Text, style.Cursor)
			}
			label = style.WithBg(fit(label, width, true), highlight)
		}
		out = append(out, guide+label)
	}
	if len(lines) == 0 && !v.picking {
		out = append(out, style.Dim.Render("The run's tests show here once Gradle starts them"))
	}
	m.addZone(zone{r: r, click: func(x, y int) tea.Cmd {
		m.setFocus(v)
		i := v.offset + y
		if i >= len(lines) {
			return nil
		}
		line := lines[i]
		caretAt := ansi.StringWidth(guides(line))
		if line.expandable && (x >= caretAt && x < caretAt+2 || line.id == v.cursor) {
			v.toggle(line.id)
		}
		v.cursor = line.id
		return nil
	}, wheel: func(delta int) {
		v.offset = max(0, min(v.offset+delta, len(v.lines())-v.height))
	}})
	return blockEllipsis(out, r.w, r.h)
}

// renderPicker is the picker's tests: the filter line, then the tree.
func (v *testsView) renderPicker(m *Model, r rect) []string {
	var findbar string
	if v.text == "" {
		findbar = style.Dim.Render("⌕ type to filter by test or class name")
	} else {
		matching := 0
		for id := range v.visible {
			if m.selectable[id] {
				matching++
			}
		}
		findbar = style.AccentBold.Render("⌕ ") + style.Bold.Render(v.text) + style.AccentText.Render("▏") + "  " +
			style.Pill(fmt.Sprintf("%d matching", matching), style.Primary, style.Raised) + style.Dim.Render("  Esc clears")
	}
	out := []string{bar(findbar, r.w, style.FgBg(style.Text, style.Base)), ""}
	return block(append(out, v.treeView(m, rect{r.x, r.y + 2, r.w, r.h - 2}, m.focused() == v)...), r.w, r.h)
}

// render is the run screen's tests: the find field when open, the tree, and the details of the test under the
// cursor.
func (v *testsView) render(m *Model, r rect) []string {
	var out []string
	y := r.y
	if v.findShown {
		out = append(out, v.findInput.view(r.w, m.focused() == v.findInput))
		input := v.findInput
		m.addZone(zone{r: rect{r.x, y, r.w, 1}, click: func(int, int) tea.Cmd { m.setFocus(input); return nil }})
		y++
	}
	height := r.h - len(out)
	widths := splitWidth(r.w, 3, 2)
	tree := v.treeView(m, rect{r.x, y, widths[0], height}, m.focused() == v)
	if v.detail.about != v.cursor {
		v.detail.set(v.cursor, m.describeTest(v.cursor, false))
	}
	detail := leftBorder(padded(v.detail.view(widths[1]-3, height), widths[1]-1, height, 1, 1))
	box := v.detail
	m.addZone(zone{r: rect{r.x + widths[0], y, widths[1], height}, click: func(int, int) tea.Cmd {
		m.setFocus(box)
		return nil
	}, wheel: box.scroll})
	return append(out, hjoin(tree, detail)...)
}

func (v *testsView) levels(m *Model, focus any) []level {
	viewLevel := level{bindings: []binding{
		bind("/", "Find", func() tea.Cmd {
			v.findShown = true
			m.setFocus(v.findInput)
			return nil
		}).when(func() bool { return !v.picking }),
		bind("s", "Show", func() tea.Cmd {
			for i, mode := range showModes {
				if mode == v.show {
					v.show = showModes[(i+1)%len(showModes)]
					break
				}
			}
			v.rebuild(m, true)
			m.notify("Showing "+v.show+" tests", "", "information")
			return nil
		}).when(func() bool { return !v.picking }),
		v.during(m, "l", "Its log", "app"),
		v.during(m, "w", "Its WireMock", "wiremock"),
		v.during(m, "k", "Its Kafka", "kafka"),
		v.during(m, "d", "Its queries", "database"),
		bind("esc", "Clear filter", func() tea.Cmd {
			v.text = ""
			if !v.picking {
				v.findInput.SetValue("")
				v.findShown = false
			}
			v.find(m)
			m.setFocus(v)
			return nil
		}).when(func() bool { return v.text != "" }),
	}}
	switch focus {
	case v.findInput:
		return []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
			if k.String() == "enter" {
				if v.findInput.Value() == "" {
					v.findShown = false
				}
				m.setFocus(v)
				return true, nil
			}
			handled, changed := v.findInput.key(k)
			if changed {
				return true, v.inputChanged(m, v.findInput)
			}
			return handled, nil
		}}, viewLevel}
	case v.detail:
		return []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) { return v.detail.key(k), nil }}, viewLevel}
	}
	return []level{v.treeLevel(m), viewLevel}
}

func (v *testsView) during(m *Model, key, label, kind string) binding {
	return bind(key, label, func() tea.Cmd {
		w := m.windowFor(v.cursor)
		if v.cursor == "" || w == nil {
			m.notify("That has not run yet", "", "warning")
			return nil
		}
		m.run.showDuring(m, kind, w)
		return nil
	}).when(func() bool { return !v.picking && m.run.hasService(kind) })
}

func (v *testsView) treeLevel(m *Model) level {
	raw := func(k tea.KeyPressMsg) (bool, tea.Cmd) {
		lines := v.lines()
		i := v.cursorIndex()
		page := max(1, v.height-1)
		switch k.String() {
		case "up":
			v.moveTo(i - 1)
		case "down":
			v.moveTo(i + 1)
		case "pgup":
			v.moveTo(i - page)
		case "pgdown":
			v.moveTo(i + page)
		case "home":
			v.moveTo(0)
		case "end":
			v.moveTo(len(lines) - 1)
		case "left":
			if len(lines) == 0 {
				return true, nil
			}
			line := lines[i]
			if line.expandable && v.expanded[line.id] {
				v.toggle(line.id)
			} else if parent := m.entries[line.id].Parent; v.visible[parent] {
				v.cursor = parent
				v.scrollToCursor = true
			}
		case "right":
			if len(lines) == 0 || !lines[i].expandable {
				return true, nil
			}
			if !v.expanded[lines[i].id] {
				v.toggle(lines[i].id)
			} else {
				v.moveTo(i + 1)
			}
		case "backspace":
			if !v.picking {
				return false, nil
			}
			return true, v.typed(m, "")
		case "space":
			if v.picking {
				return false, nil
			}
			if len(lines) > 0 && lines[i].expandable {
				v.toggle(lines[i].id)
			}
		default:
			mod := k.Mod
			if v.picking && k.Text != "" && k.Text != " " && !mod.Contains(tea.ModCtrl) && !mod.Contains(tea.ModAlt) {
				return true, v.typed(m, k.Text) // a typed letter filters; it is not a shortcut
			}
			return false, nil
		}
		return true, nil
	}
	if v.picking {
		return level{raw: raw, bindings: []binding{
			bind("space", "Select", func() tea.Cmd { v.pick(m, v.cursor, false); return nil }),
			bind("ctrl+a", "All/none", func() tea.Cmd { v.pick(m, "", true); return nil }),
			bind("enter", "Run…", func() tea.Cmd { m.picker.focus = focusButton; return nil }),
		}}
	}
	return level{raw: raw, bindings: []binding{
		bind("enter", "Run this", func() tea.Cmd {
			if v.cursor == "" {
				return nil
			}
			return m.startRun(m.selectionFor(v.cursor))
		}),
	}}
}

// pick selects or unselects the tests under a node, or every test shown.
func (v *testsView) pick(m *Model, id string, all bool) {
	var targets []string
	if all {
		for shown := range v.visible {
			if m.selectable[shown] {
				targets = append(targets, shown)
			}
		}
	} else if entry := m.entries[id]; entry != nil {
		candidates := []string{id}
		if !entry.IsLeaf() {
			candidates = m.leavesUnder(id)
		}
		for _, c := range candidates {
			if m.selectable[c] && (v.text == "" || v.visible[c]) { // a class under a filter selects only its matching tests
				targets = append(targets, c)
			}
		}
	} else {
		return
	}
	allPicked := len(targets) > 0
	for _, t := range targets {
		allPicked = allPicked && m.selected[t]
	}
	for _, t := range targets {
		if allPicked {
			delete(m.selected, t)
		} else {
			m.selected[t] = true
		}
	}
	if !all && m.selectable[id] {
		v.moveTo(v.cursorIndex() + 1)
	}
}

// typed adds a character to the picker's filter, or removes the last one for "", and filters once typing pauses.
func (v *testsView) typed(m *Model, character string) tea.Cmd {
	if character == "" {
		if runes := []rune(v.text); len(runes) > 0 {
			v.text = string(runes[:len(runes)-1])
		}
	} else {
		v.text += character
	}
	v.findSeq++
	seq := v.findSeq
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return findMsg{view: v, seq: seq} })
}

func (v *testsView) find(m *Model) {
	v.rebuild(m, true)
}

func (v *testsView) inputChanged(m *Model, input *lineInput) tea.Cmd {
	v.text = input.Value()
	v.rebuild(m, true)
	return nil
}
