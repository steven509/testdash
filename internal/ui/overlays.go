package ui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/style"
)

// dialog draws a framed card in the middle of the screen, with a title pill, and makes a click outside it
// close it.
func (m *Model) dialog(screen []string, content []string, width int, title string, y int) rect {
	framed := card(content, width, style.Accent...)
	overlay(framed, []string{style.BoldPill(title, style.Base, style.Primary)}, 2, 0)
	x := (m.width - width) / 2
	if y < 0 {
		y = max(1, (m.height-len(framed))/2)
	}
	m.addZone(zone{r: rect{0, 0, m.width, m.height}, click: func(int, int) tea.Cmd {
		m.popup = nil
		return nil
	}})
	overlay(screen, framed, x, y)
	area := rect{x, y, width, len(framed)}
	m.addZone(zone{r: area, click: func(int, int) tea.Cmd { return nil }})
	return area
}

// the command palette

type command struct {
	name, help string
	run        func() tea.Cmd
}

type palette struct {
	input  lineInput
	cursor int
	shown  []command
}

func (m *Model) commands() []command {
	all := []command{
		{"Run all tests", "Every test in the task", func() tea.Cmd { return m.startRun(nil) }},
		{"Run the selected tests", count(len(m.selected), "test") + " selected", func() tea.Cmd {
			return m.startRun(m.selected)
		}},
		{"Run the failed tests", "The tests that failed last time", func() tea.Cmd { return m.startRun(m.rerunnable()) }},
		{"Stop the run", "Interrupt Gradle; again kills it", func() tea.Cmd { m.stop(); return nil }},
		{"Unselect the passed tests", "Keep the failed and unrun ones selected (u)", func() tea.Cmd {
			m.unselectPassed()
			return nil
		}},
		{"Unselect everything", "Clear the selection", func() tea.Cmd { m.selected = map[string]bool{}; return nil }},
		{"Choose the test task", "Pick another of the build's test tasks", func() tea.Cmd { m.openTasks(); return nil }},
		{"List the tests again", "Rediscover the build's tests", m.discover},
		{"Show Gradle's output", "What Gradle printed", func() tea.Cmd {
			m.serviceList.cursor = 0
			m.setFocus(m.serviceList)
			return nil
		}},
	}
	for _, entry := range m.orderedServices() {
		entry := entry
		all = append(all, command{"Show " + cleanText(entry.svc.Key), entry.svc.Kind, func() tea.Cmd {
			m.serviceList.selectEntry(m, entry)
			m.setFocus(m.serviceList)
			return nil
		}})
	}
	return append(all,
		command{"Zoom the details", "Give the details the whole width (z)", func() tea.Cmd { m.zoomed = !m.zoomed; return nil }},
		command{"Simplified or full log lines", "Every log (v)", func() tea.Cmd { m.logSimple = !m.logSimple; return nil }},
		command{"Times in logs", "Every log (t)", func() tea.Cmd { m.logTime = !m.logTime; return nil }},
		command{"Runs", "The runs before, their failures, to run again (H)", func() tea.Cmd { m.openRuns(); return nil }},
		command{"Noise filters", "What the views leave out, to change or add to", func() tea.Cmd { m.openNoiseRules(); return nil }},
		command{"Settings", "Icons, colours, mouse, log defaults", func() tea.Cmd { m.openSettings(); return nil }},
		command{"Help", "Every key (?)", func() tea.Cmd { m.popup = &help{}; return nil }},
		command{"Quit", "Stop the run and leave", m.quit},
	)
}

func (m *Model) openPalette() {
	p := &palette{input: lineInput{placeholder: "type a command"}}
	p.filter(m)
	m.popup = p
}

func (p *palette) filter(m *Model) {
	query := strings.ToLower(p.input.Value())
	type scored struct {
		c     command
		score int
	}
	var found []scored
	for _, c := range m.commands() {
		if score, ok := fuzzy(query, strings.ToLower(c.name)); ok {
			found = append(found, scored{c, score})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].score > found[j].score })
	p.shown = p.shown[:0]
	for _, f := range found {
		p.shown = append(p.shown, f.c)
	}
	p.cursor = max(0, min(p.cursor, len(p.shown)-1))
}

// fuzzy matches query's characters in order in text, scoring runs of adjacent matches and word starts higher.
func fuzzy(query, text string) (int, bool) {
	score, last, position := 0, -2, 0
	for _, q := range query {
		i := strings.IndexRune(text[position:], q)
		if i < 0 {
			return 0, false
		}
		at := position + i
		if at == last+1 {
			score += 3
		}
		if at == 0 || text[at-1] == ' ' {
			score += 2
		}
		score++
		last, position = at, at+len(string(q))
	}
	return score, true
}

func (p *palette) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+p":
		m.popup = nil
	case "up", "ctrl+k":
		p.cursor = max(0, p.cursor-1)
	case "down", "ctrl+j":
		p.cursor = min(len(p.shown)-1, p.cursor+1)
	case "enter":
		return p.choose(m, p.cursor)
	case "ctrl+c", "ctrl+q":
		m.popup = &confirmQuit{}
	default:
		if handled, changed := p.input.key(k); handled && changed {
			p.cursor = 0
			p.filter(m)
		}
	}
	return nil
}

func (p *palette) choose(m *Model, i int) tea.Cmd {
	m.popup = nil
	if i >= 0 && i < len(p.shown) {
		return p.shown[i].run()
	}
	return nil
}

func (p *palette) render(m *Model, screen []string) {
	width := min(76, m.width-4)
	rows := min(len(p.shown), 12)
	start := max(0, p.cursor-rows+1)
	content := []string{style.AccentBold.Render(ic.search+" ") + typed(&p.input), style.LineText.Render(strings.Repeat("─", width-4))}
	for i := start; i < start+rows && i < len(p.shown); i++ {
		content = append(content, menuRow(p.shown[i].name, p.shown[i].help, width-4, i == p.cursor))
	}
	if len(p.shown) == 0 {
		content = append(content, style.Dim.Render(" No command matches"))
	}
	area := m.dialog(screen, content, width, "Commands", 2)
	m.addZone(zone{r: area, click: func(_, y int) tea.Cmd {
		if i := start + y - 3; y >= 3 && i < len(p.shown) {
			return p.choose(m, i)
		}
		return nil
	}})
}

func menuRow(name, help string, width int, selected bool) string {
	if selected {
		line := style.AccentBold.Render("▌") + style.Bold.Render(name) + "  " + style.Muted.Render(help)
		return style.WithBg(fit(line, width, true), style.Bg(style.Cursor))
	}
	return " " + style.Bold.Render(name) + "  " + style.Dim.Render(help)
}

// typed is a field's text with its cursor, for dialogs.
func typed(in *lineInput) string {
	caret := ansi.NewStyle().ForegroundColor(style.Base).BackgroundColor(style.Primary)
	if len(in.value) == 0 {
		return caret.Styled(" ") + style.Dim.Render(in.placeholder)
	}
	before := string(in.value[:in.cursor])
	after := []rune(string(in.value[in.cursor:]))
	under := " "
	if len(after) > 0 {
		under, after = string(after[0]), after[1:]
	}
	return style.Bold.Render(before) + caret.Styled(under) + style.Bold.Render(string(after))
}

// the task chooser

type taskChooser struct {
	cursor int
}

func (m *Model) openTasks() {
	if m.catalog == nil || len(m.catalog.Paths) == 0 {
		m.notify("No test tasks to choose from yet", "", "information")
		return
	}
	c := &taskChooser{}
	for i, path := range m.catalog.Paths {
		if m.testTask != nil && path == m.testTask.Path {
			c.cursor = i
		}
	}
	m.popup = c
}

func (c *taskChooser) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	paths := m.catalog.Paths
	switch k.String() {
	case "esc", "q":
		m.popup = nil
	case "up", "k":
		c.cursor = max(0, c.cursor-1)
	case "down", "j":
		c.cursor = min(len(paths)-1, c.cursor+1)
	case "enter", "space":
		c.choose(m, c.cursor)
	case "ctrl+c":
		m.popup = &confirmQuit{}
	}
	return nil
}

func (c *taskChooser) choose(m *Model, i int) {
	m.popup = nil
	if i >= 0 && i < len(m.catalog.Paths) {
		m.chooseTask(m.catalog.Paths[i], false)
	}
	m.setFocus(m.tree)
}

func (c *taskChooser) render(m *Model, screen []string) {
	paths := m.catalog.Paths
	width := min(64, m.width-4)
	rows := min(len(paths), 14)
	start := max(0, c.cursor-rows+1)
	var content []string
	for i := start; i < start+rows; i++ {
		task := m.catalog.Tasks[paths[i]]
		current := ""
		if m.testTask != nil && paths[i] == m.testTask.Path {
			current = " " + ic.check
		}
		content = append(content, menuRow(paths[i]+current, count(len(task.Leaves()), "test"), width-4, i == c.cursor))
	}
	area := m.dialog(screen, content, width, "Test task", -1)
	m.addZone(zone{r: area, click: func(_, y int) tea.Cmd {
		if i := start + y - 1; y >= 1 && i < start+rows {
			c.choose(m, i)
		}
		return nil
	}})
}

// help

type help struct{}

var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"Anywhere", [][2]string{{"tab", "next panel"}, {"1 2 3", "tests, services, details"}, {"r", "run the selection, or all"},
		{"F", "run the failed (or last run's)"}, {"x", "stop (twice kills)"}, {"z", "zoom the details"}, {"c", "the run's whole command"}, {"^t", "test task"}, {"H", "runs before this one"},
		{"^p :", "commands"}, {"^s", "settings"}, {"q", "quit (asks first)"}}},
	{"Tests", [][2]string{{"↑ ↓", "move"}, {"← →", "collapse, expand"}, {"space", "select"}, {"a", "select all shown"}, {"u", "unselect the passed"},
		{"enter", "run the selection, or this test or class"}, {"/", "filter by name"}, {"s", "all, failed, not passed, flaky"},
		{"c", "copy a failed test's failure"}, {"esc", "clear"}}},
	{"The run's details", [][2]string{{"[ ]", "overview, Live, Activity, Classes"}, {"enter", "Live: open in its service"},
		{"/", "Live: filter"}, {"t", "Live: its trace everywhere"}, {"+ −", "activity: a longer, shorter window"},
		{"esc", "back to the overview"}}},
	{"A test's details", [][2]string{{"↑ ↓", "events while it ran"}, {"enter", "open the event in its service"},
		{"c", "copy: curl, query, command, payload"}, {"t", "by trace, or in time order"}, {"tab", "the event, the stack trace"}, {"esc", "back to the tests"}}},
	{"Logs", [][2]string{{"/", "filter (regex)"}, {"e", "errors only"}, {"f", "follow"}, {"w", "wrap"},
		{"v", "simplified or full"}, {"t", "times"}, {"n", "show the noise too"}, {"N", "hide what the filter finds as noise"},
		{"esc", "clear, back"}}},
	{"Services", [][2]string{{"[ ]", "tabs"}, {"↑ ↓", "rows; details beside"}, {"u", "WireMock: unmatched only"},
		{"enter", "Kafka: a topic's messages"}, {"h", "Kafka: a message's headers"}, {"n", "show the noise too"},
		{"N", "hide things like it as noise"}, {"t", "its trace everywhere, or all"}, {"v", "SQL: formatted or raw"},
		{"c", "copy: curl, query, command, payload"}}},
}

func (h *help) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	m.popup = nil
	if k.String() == "ctrl+c" {
		m.popup = &confirmQuit{}
	}
	return nil
}

func (h *help) render(m *Model, screen []string) {
	width := min(100, m.width-4)
	columns := 2
	if width < 76 {
		columns = 1
	}
	columnWidth := (width - 4) / columns
	var blocks [][]string
	for _, s := range helpSections {
		lines := []string{style.AccentBold.Render(s.title)}
		for _, k := range s.keys {
			lines = append(lines, fit(style.Bold.Render(k[0]), 8, false)+style.Muted.Render(k[1]))
		}
		blocks = append(blocks, append(lines, ""))
	}
	var left, right []string
	for i, b := range blocks {
		if columns == 2 && i%2 == 1 {
			right = append(right, b...)
		} else {
			left = append(left, b...)
		}
	}
	height := max(len(left), len(right))
	content := hjoin(block(left, columnWidth, height), block(right, columnWidth, height))
	content = append(content, style.Dim.Render("Any key closes this. Settings are in ^s or testdash --settings."))
	m.dialog(screen, content, width, "Keys", -1)
}

// settings

type settingsOverlay struct {
	form *settingsForm
}

func (m *Model) openSettings() {
	m.popup = &settingsOverlay{form: newSettingsForm(m.prefs, m.applySettings)}
}

func (o *settingsOverlay) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", "ctrl+s":
		m.popup = nil
		return nil
	case "ctrl+c":
		m.popup = &confirmQuit{}
		return nil
	}
	o.form.key(k)
	return nil
}

func (o *settingsOverlay) render(m *Model, screen []string) {
	width := min(96, m.width-4)
	content := o.form.render(width-4, m.height-8)
	area := m.dialog(screen, content, width, ic.gear+" Settings", -1)
	o.form.zones(m, rect{area.x + 2, area.y + 1, width - 4, area.h - 2})
}

// confirmQuit asks before testdash quits, and says when that stops a run.
type confirmQuit struct{}

func (c *confirmQuit) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "y", "enter", "q", "ctrl+q", "ctrl+c":
		return m.quit()
	}
	m.popup = nil
	return nil
}

func (c *confirmQuit) render(m *Model, screen []string) {
	width := min(52, m.width-4)
	lines := []string{"", style.Bold.Render("Quit testdash?")}
	switch {
	case m.runner.Running():
		lines = append(lines, style.Muted.Render("The run in progress will be stopped."))
	case m.discovering:
		lines = append(lines, style.Muted.Render("Listing the tests will be stopped."))
	}
	quit := keyCap("y") + " " + style.Bold.Render("Quit")
	stay := keyCap("n") + " " + style.Muted.Render("Stay")
	buttons := quit + "     " + stay
	pad := max(0, (width-4-ansi.StringWidth(buttons))/2)
	lines = append(lines, "", strings.Repeat(" ", pad)+buttons, "")
	area := m.dialog(screen, lines, width, "Quit", -1)
	row := area.y + 1 + len(lines) - 2
	x := area.x + 2 + pad
	m.addZone(zone{r: rect{x, row, ansi.StringWidth(quit), 1}, click: func(int, int) tea.Cmd { return m.quit() }})
	m.addZone(zone{r: rect{x + ansi.StringWidth(quit) + 5, row, ansi.StringWidth(stay), 1}, click: func(int, int) tea.Cmd {
		m.popup = nil
		return nil
	}})
}
