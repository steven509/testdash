package ui

import (
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/catalog"
	"github.com/steven509/testdash/internal/style"
)

var (
	notSearched = regexp.MustCompile(`[^a-z0-9]`) // spaces and punctuation: "signup" finds "a sign-up stores"
	showModes   = []string{"all", "failed", "not passed"}
)

func searchable(text string) string {
	return notSearched.ReplaceAllString(strings.ToLower(text), "")
}

// testTree is the task's tests: what you pick, what runs, and how each did. Its first row is every test, the
// run as a whole. The tests' state lives on the Model (entries, selected, status).
type testTree struct {
	text           string
	filter         *lineInput
	filterShown    bool
	show           string
	visible        map[string]bool
	children       map[string][]string
	expanded       map[string]bool // what is open, as chosen on the whole tree
	filtered       map[string]bool // what is open under a filter or show mode, so clearing it restores the above
	cursor         string          // "" is the row for every test
	want           string          // where the cursor was before a filter hid it, to go back to
	offset         int
	height         int
	scrollToCursor bool
	flat           []treeLine
	flatDirty      bool
}

type treeLine struct {
	id         string
	depth      int
	expandable bool
}

func newTestTree() *testTree {
	return &testTree{show: "all", visible: map[string]bool{}, children: map[string][]string{},
		expanded: map[string]bool{}, filter: &lineInput{placeholder: "filter tests by name or class"}}
}

// rebuild works out which tests show; reset puts every node's expansion back to its default, as a new filter
// or task does.
func (t *testTree) rebuild(m *Model, reset bool) {
	t.visible = t.computeVisible(m)
	t.children = map[string][]string{}
	var add func(parent string, e *catalog.Entry)
	add = func(parent string, e *catalog.Entry) {
		if !t.visible[e.ID] {
			return
		}
		t.children[parent] = append(t.children[parent], e.ID)
		for _, kid := range m.kids[e.ID] {
			add(e.ID, kid)
		}
	}
	for _, top := range m.kids[""] {
		add("", top)
	}
	expand := t.filtering() || len(t.visible) <= 60
	if reset || t.filtered == nil {
		t.filtered = map[string]bool{}
	}
	if t.expanded == nil {
		t.expanded = map[string]bool{}
	}
	open := t.expansion()
	for id := range t.visible {
		if _, known := open[id]; !known && len(m.kids[id]) > 0 {
			open[id] = expand || m.entries[id].Kind == "template"
		}
	}
	t.flatDirty = true
	if t.cursor != "" && !t.visible[t.cursor] {
		t.want, t.cursor = t.cursor, ""
	}
	if t.cursor == "" && t.want != "" && t.visible[t.want] {
		t.cursor, t.want = t.want, ""
	}
	t.scrollToCursor = true
}

func (t *testTree) filtering() bool {
	return t.text != "" || t.show != "all"
}

// expansion is what is open in the tree as shown: the filter's own while one applies.
func (t *testTree) expansion() map[string]bool {
	if t.filtering() {
		return t.filtered
	}
	return t.expanded
}

func (t *testTree) computeVisible(m *Model) map[string]bool {
	visible := map[string]bool{}
	needle := searchable(t.text)
	var walk func(e *catalog.Entry) bool
	walk = func(e *catalog.Entry) bool {
		kids := false
		for _, kid := range m.kids[e.ID] {
			if walk(kid) {
				kids = true
			}
		}
		classMatch := e.Kind == "class" && needle != "" && strings.Contains(searchable(e.ClassName), needle)
		own := e.IsLeaf() && t.wanted(m, e) && (needle == "" || strings.Contains(searchable(e.Name), needle) ||
			strings.Contains(searchable(e.ClassName), needle))
		if !own && !kids && !classMatch {
			return false
		}
		visible[e.ID] = true
		if classMatch {
			for _, id := range m.descendants(e.ID) {
				if t.wanted(m, m.entries[id]) {
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

func (t *testTree) wanted(m *Model, e *catalog.Entry) bool {
	status := m.status(e.ID)
	switch t.show {
	case "failed":
		return status == "failed" || status == "aborted"
	case "not passed":
		return status != "passed" && status != "skipped"
	}
	return true
}

// lines are the rows shown: every test first, then the tree as far as it is expanded.
func (t *testTree) lines() []treeLine {
	if !t.flatDirty && t.flat != nil {
		return t.flat
	}
	t.flat = append(t.flat[:0], treeLine{id: "", depth: 0, expandable: false})
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, id := range t.children[parent] {
			line := treeLine{id: id, depth: depth, expandable: len(t.children[id]) > 0}
			t.flat = append(t.flat, line)
			if line.expandable && t.expansion()[id] {
				walk(id, depth+1)
			}
		}
	}
	walk("", 1)
	t.flatDirty = false
	return t.flat
}

func (t *testTree) cursorIndex() int {
	for i, line := range t.lines() {
		if line.id == t.cursor {
			return i
		}
	}
	return 0
}

func (t *testTree) moveTo(i int) {
	lines := t.lines()
	t.cursor = lines[max(0, min(i, len(lines)-1))].id
	t.want = ""
	t.scrollToCursor = true
}

func (t *testTree) toggle(id string) {
	open := t.expansion()
	open[id] = !open[id]
	t.flatDirty = true
}

// leaves are the selectable tests under a row; "" is every test shown.
func (t *testTree) leaves(m *Model, id string) []string {
	if id == "" {
		var all []string
		for shown := range t.visible {
			if m.selectable[shown] {
				all = append(all, shown)
			}
		}
		return all
	}
	entry := m.entries[id]
	if entry == nil {
		return nil
	}
	candidates := []string{id}
	if !entry.IsLeaf() {
		candidates = m.leavesUnder(id)
	}
	var leaves []string
	for _, c := range candidates {
		if m.selectable[c] && (t.text == "" || t.visible[c]) { // under a filter, only the matching tests
			leaves = append(leaves, c)
		}
	}
	return leaves
}

// pick selects the tests under a row, or unselects them when they all are.
func (t *testTree) pick(m *Model, id string) {
	targets := t.leaves(m, id)
	all := len(targets) > 0
	for _, target := range targets {
		all = all && m.selected[target]
	}
	for _, target := range targets {
		if all {
			delete(m.selected, target)
		} else {
			m.selected[target] = true
		}
	}
}

// selection is how much of a row is selected: none, some or all.
func (t *testTree) selection(m *Model, id string) (picked, total int) {
	var leaves []string
	if id == "" {
		for leaf := range m.selectable {
			leaves = append(leaves, leaf)
		}
	} else if m.selectable[id] {
		leaves = []string{id}
	} else {
		leaves = m.leavesUnder(id)
	}
	for _, leaf := range leaves {
		if m.selected[leaf] {
			picked++
		}
	}
	return picked, len(leaves)
}

// rowStatus is a row's status: a test's own, a class's from its tests, the run's for the first row.
func (t *testTree) rowStatus(m *Model, id string) string {
	entry := m.entries[id]
	if id != "" && entry.IsLeaf() {
		return m.status(id)
	}
	var leaves []string
	if id == "" {
		for _, e := range m.runLeaves() {
			leaves = append(leaves, e.ID)
		}
		if m.runStarted.IsZero() {
			return "idle"
		}
	} else {
		leaves = m.leavesUnder(id)
	}
	inRun, done, running, failed := 0, 0, false, false
	for _, leaf := range leaves {
		s := m.status(leaf)
		if s == "absent" || s == "idle" {
			continue
		}
		inRun++
		switch s {
		case "passed", "skipped":
			done++
		case "failed", "aborted":
			done++
			failed = true
		case "running":
			running = true
		}
	}
	switch {
	case inRun == 0 && m.runStarted.IsZero():
		return "idle"
	case inRun == 0:
		return "absent"
	case failed:
		return "failed"
	case running:
		return "running"
	case done == inRun:
		return "passed"
	}
	return "pending"
}

// right is what a row shows at its end: a test's time, a class's results.
func (t *testTree) right(m *Model, id string) string {
	entry := m.entries[id]
	if id != "" && entry.IsLeaf() {
		if state := m.state(id); state.StartedAt != 0 && m.status(id) != "absent" {
			millis, _ := state.Took(nowMillis())
			return style.Dim.Render(duration(millis))
		}
		return ""
	}
	leaves := m.countedUnder(id)
	passed, failed, inRun := 0, 0, 0
	for _, leaf := range leaves {
		switch m.status(leaf) {
		case "passed", "skipped":
			passed++
			inRun++
		case "failed", "aborted":
			failed++
			inRun++
		case "pending", "running":
			inRun++
		}
	}
	if inRun == 0 {
		return style.Dim.Render(compact(len(leaves)))
	}
	text := style.Dim.Render(compact(passed+failed) + "/" + compact(inRun))
	if failed > 0 {
		text = style.Red.Render(ic.failed+" "+compact(failed)+"  ") + text
	}
	return text
}

func (t *testTree) name(m *Model, id string) string {
	if id == "" {
		return style.Bold.Render(m.overviewName())
	}
	entry := m.entries[id]
	if entry.Kind == "class" {
		if parent := m.entries[entry.Parent]; parent != nil && parent.Kind == "class" {
			return style.Bold.Render(cleanText(entry.Name))
		}
		return style.Bold.Render(entry.ShortClass())
	}
	return cleanText(entry.Name)
}

func (t *testTree) render(m *Model, r rect) []string {
	focused := m.panelOf(m.focus) == panelTests
	inner := rect{r.x + 2, r.y + 1, r.w - 4, r.h - 2}
	m.addZone(zone{r: r, click: func(int, int) tea.Cmd { m.setFocus(t); return nil }}) // under the rows' own
	var content []string
	y := inner.y
	if t.filterShown {
		content = append(content, t.filter.view(inner.w, m.focus == t.filter), "")
		input := t.filter
		m.addZone(zone{r: rect{inner.x, y, inner.w, 1}, click: func(int, int) tea.Cmd { m.setFocus(input); return nil }})
		y += 2
	}
	if m.testTask == nil {
		content = append(content, "")
		for _, line := range m.noTaskReason() {
			if line == "" {
				content = append(content, "")
			}
			content = append(content, wrapLines(line, inner.w)...)
		}
	} else {
		content = append(content, t.rows(m, rect{inner.x, y, inner.w, inner.h - len(content)}, m.focus == t)...)
	}
	var chips []string
	if t.text != "" && !t.filterShown {
		chips = append(chips, chip(ic.search+" "+t.text))
	}
	if t.show != "all" {
		chips = append(chips, chip(t.show))
	}
	if n := len(m.selected); n > 0 {
		chips = append(chips, style.Pill(number(n)+" selected", style.Primary, style.Raised))
	}
	title := "Tests"
	if m.discovering {
		title += " " + m.spinnerFrame()
	}
	return frame(content, r, numbered(1, title), strings.Join(chips, " "), "", focused)
}

// rows draws the visible part of the tree, and the mouse zones for its rows.
func (t *testTree) rows(m *Model, r rect, focused bool) []string {
	lines := t.lines()
	t.height = r.h
	cursor := t.cursorIndex()
	if t.scrollToCursor {
		if cursor < t.offset {
			t.offset = cursor
		} else if cursor >= t.offset+r.h {
			t.offset = cursor - r.h + 1
		}
		t.scrollToCursor = false
	}
	t.offset = max(0, min(t.offset, len(lines)-r.h))
	var out []string
	for i := t.offset; i < len(lines) && len(out) < r.h; i++ {
		line := lines[i]
		out = append(out, t.row(m, line, r.w, i == cursor, focused))
	}
	m.addZone(zone{r: r, click: func(x, y int) tea.Cmd {
		m.setFocus(t)
		i := t.offset + y
		if i >= len(lines) {
			return nil
		}
		line := lines[i]
		markWidth := ansi.StringWidth(ic.unpicked) + 1
		caretAt := markWidth + 2*max(0, line.depth-1)
		switch {
		case x < markWidth:
			t.pick(m, line.id)
		case line.expandable && x >= caretAt && x <= caretAt+ansi.StringWidth(ic.caretOpen):
			t.toggle(line.id)
		case line.expandable && line.id == t.cursor: // a second click on a class opens or closes it
			t.toggle(line.id)
		default:
			t.cursor, t.want = line.id, ""
		}
		return nil
	}, wheel: func(delta int) {
		t.offset = max(0, min(t.offset+delta, len(t.lines())-t.height))
	}})
	if len(lines) == 1 {
		switch {
		case t.text != "":
			out = append(out, "", style.Muted.Render("  No test matches "+t.text+"."))
		case t.show == "failed":
			out = append(out, "", style.Muted.Render("  No test has failed."), style.Dim.Render("  s shows the others"))
		case t.show != "all":
			out = append(out, "", style.Muted.Render("  Every test has passed."), style.Dim.Render("  s shows them all"))
		}
	}
	return out
}

func (t *testTree) row(m *Model, line treeLine, width int, isCursor, focused bool) string {
	picked, total := t.selection(m, line.id)
	mark := style.Dim.Render(ic.unpicked)
	switch {
	case total > 0 && picked == total:
		mark = style.AccentBold.Render(ic.picked)
	case picked > 0:
		mark = style.AccentText.Render(ic.partly)
	}
	indent := strings.Repeat("  ", max(0, line.depth-1))
	caret := strings.Repeat(" ", ansi.StringWidth(ic.caretOpen)+1)
	if line.expandable {
		caret = ic.caretClosed + " "
		if t.expansion()[line.id] {
			caret = ic.caretOpen + " "
		}
		caret = style.Dim.Render(caret)
	}
	status, statusStyle := icon(t.rowStatus(m, line.id))
	name := t.name(m, line.id)
	if picked > 0 && total > 0 && picked == total {
		name = style.AccentText.Render(ansi.Strip(name))
	}
	left := mark + " " + indent + caret + statusStyle.Render(status) + " " + name
	right := t.right(m, line.id)
	space := width - ansi.StringWidth(right)
	body := fit(left, max(0, space-1), true) + " " + right
	if isCursor {
		bg := style.Bg(style.CursorBlur)
		if focused {
			bg = style.BoldBg(style.Text, style.Cursor)
		}
		body = style.WithBg(fit(body, width, false), bg)
	}
	return body
}

func (t *testTree) levels(m *Model) []level {
	if m.focus == t.filter {
		done := func() tea.Cmd {
			if t.filter.Value() == "" {
				t.filterShown = false
			} else if t.cursor == "" && len(t.lines()) > 1 {
				t.moveTo(1) // the first match, so enter runs what the filter found rather than everything
			}
			m.setFocus(t)
			return nil
		}
		clear := func() tea.Cmd { t.clearFilter(m); return nil }
		return []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
			switch k.String() {
			case "enter", "down":
				return true, done()
			case "esc":
				return true, clear()
			}
			handled, changed := t.filter.key(k)
			if changed {
				return true, t.inputChanged(m, t.filter)
			}
			return handled, nil
		}, bindings: []binding{bind("enter", "Done", done), bind("esc", "Clear", clear)}}}
	}
	return []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) { return t.key(m, k) }, bindings: []binding{
		bind("space", "Select", func() tea.Cmd {
			t.pick(m, t.cursor)
			if m.selectable[t.cursor] { // on to the next test; a class stays, to see what it selected
				t.moveTo(t.cursorIndex() + 1)
			}
			return nil
		}),
		bind("enter", "Run "+count(len(m.selected), "selected test"), func() tea.Cmd { return m.startRun(m.selected) }).
			when(func() bool { return !m.running() && len(m.selected) > 0 }),
		bind("enter", t.runThisLabel(m), func() tea.Cmd { return m.startRun(m.selectionFor(t.cursor)) }).
			when(func() bool { return !m.running() && len(m.selected) == 0 && t.cursor != "" }),
		bind("enter", "Run all", func() tea.Cmd { return m.startRun(nil) }).hidden().
			when(func() bool { return !m.running() && len(m.selected) == 0 && t.cursor == "" }),
		bind("/", "Filter", func() tea.Cmd {
			t.filterShown = true
			t.filter.SetValue(t.text)
			m.setFocus(t.filter)
			return nil
		}),
		bind("s", "Show "+t.nextShow(), func() tea.Cmd {
			t.show = t.nextShow()
			t.rebuild(m, true)
			return nil
		}),
		bind("a", "Select all", func() tea.Cmd { t.pick(m, ""); return nil }).hidden(),
		m.copyFailure(t.cursor),
		bind("u", "Unselect passed", func() tea.Cmd { m.unselectPassed(); return nil }).when(m.selectionPassed),
		bind("esc", "Clear", func() tea.Cmd {
			if t.text != "" || t.show != "all" {
				t.clearFilter(m)
			} else {
				m.selected = map[string]bool{}
			}
			return nil
		}).when(func() bool { return t.text != "" || t.show != "all" || len(m.selected) > 0 }),
	}}}
}

func (t *testTree) nextShow() string {
	for i, mode := range showModes {
		if mode == t.show {
			return showModes[(i+1)%len(showModes)]
		}
	}
	return "all"
}

func (t *testTree) runThisLabel(m *Model) string {
	entry := m.entries[t.cursor]
	switch {
	case t.cursor == "":
		return "Run all"
	case entry != nil && entry.Kind == "class":
		return "Run class"
	}
	return "Run test"
}

func (t *testTree) clearFilter(m *Model) {
	t.text, t.show = "", "all"
	t.filter.SetValue("")
	t.filterShown = false
	t.rebuild(m, true)
	m.setFocus(t)
}

func (t *testTree) inputChanged(m *Model, input *lineInput) tea.Cmd {
	t.text = input.Value()
	t.rebuild(m, true)
	return nil
}

func (t *testTree) key(m *Model, k tea.KeyPressMsg) (bool, tea.Cmd) {
	lines := t.lines()
	i := t.cursorIndex()
	page := max(1, t.height-1)
	switch k.String() {
	case "up", "k":
		t.moveTo(i - 1)
	case "down", "j":
		t.moveTo(i + 1)
	case "pgup":
		t.moveTo(i - page)
	case "pgdown":
		t.moveTo(i + page)
	case "home", "g":
		t.moveTo(0)
	case "end", "G":
		t.moveTo(len(lines) - 1)
	case "left", "h":
		line := lines[i]
		if line.expandable && t.expansion()[line.id] {
			t.toggle(line.id)
		} else if entry := m.entries[line.id]; entry != nil {
			if t.visible[entry.Parent] {
				t.cursor = entry.Parent
			} else {
				t.cursor = ""
			}
			t.scrollToCursor = true
		}
	case "right", "l":
		if lines[i].expandable && !t.expansion()[lines[i].id] {
			t.toggle(lines[i].id)
		} else if i+1 < len(lines) && lines[i+1].depth > lines[i].depth {
			t.moveTo(i + 1)
		}
	default:
		return false, nil
	}
	return true, nil
}
