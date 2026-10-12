package ui

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/style"
)

type focusToken string

const (
	focusSelect      focusToken = "select"
	focusButton      focusToken = "button"
	focusNav         focusToken = "nav"
	focusPanelTests  focusToken = "panel:tests"
	focusPanelWM     focusToken = "panel:wiremock"
	focusPanelKafka  focusToken = "panel:kafka"
	sideMinWidth                = 36
	selectHeight                = 3
	dropdownMaxItems            = 10
)

var pickerKeys = [][2]string{
	{"type", "filter the tests"},
	{"↑ ↓", "move"},
	{"← →", "collapse, expand"},
	{"space", "select"},
	{"^a", "select all or none"},
	{"^f", "select last run's failures"},
	{"^t", "test task"},
	{"Enter", "Run button, again to run"},
}

// pickerState is the test picker. Typing filters the test tree, so this screen's own keys are control keys.
type pickerState struct {
	tests        *testsView
	focus        any
	selectOpen   bool
	selectCursor int
	selectRect   rect
}

func newPickerState() *pickerState {
	p := &pickerState{tests: newTestsView(true)}
	p.focus = p.tests
	return p
}

func (p *pickerState) resume(m *Model) {
	p.tests.rebuild(m, false)
	p.focus = p.tests
}

func (p *pickerState) buttonState(m *Model) (label string, disabled bool, summary string) {
	total := len(m.selectable)
	picked := 0
	classes := map[string]bool{}
	for id := range m.selected {
		if m.selectable[id] {
			picked++
		}
		if e := m.entries[id]; e != nil {
			classes[e.ClassName] = true
		}
	}
	if m.testTask == nil {
		summary, label, disabled = m.noTaskReason(), "▶ Run", m.baseCommand() == nil
	} else {
		summary = fmt.Sprintf("\n%d of %s selected", picked, count(total, "test"))
		if picked > 0 {
			summary += "\nin " + count(len(classes), "class", "classes")
			label = "▶ Run " + count(picked, "test")
		} else {
			summary += "\nnothing selected runs them all"
			label = "▶ Run all " + count(total, "test")
		}
	}
	if m.runner.Running() {
		label, disabled = "Running… Esc watches, ^x stops", true
	}
	return label, disabled, summary
}

func (p *pickerState) focusables(m *Model) []any {
	targets := []any{p.tests, focusSelect}
	if _, disabled, _ := p.buttonState(m); !disabled {
		targets = append(targets, focusButton)
	}
	return targets
}

func (p *pickerState) render(m *Model) []string {
	hint := "pick the tests to run"
	if m.discovering {
		hint = m.spinnerFrame() + " listing tests…"
	}
	out := []string{m.titleBar("", hint)}
	bodyHeight := m.height - 4
	sideWidth := max(sideMinWidth, m.width/3)
	testsWidth := m.width - sideWidth
	tests := p.tests.renderPicker(m, rect{0, 1, testsWidth, bodyHeight})
	side := p.side(m, rect{testsWidth, 1, sideWidth, bodyHeight})
	for i, line := range side {
		side[i] = style.WithBg(line, style.Bg(style.Surface))
	}
	out = append(out, hjoin(tests, side)...)
	for _, line := range m.statusText() {
		out = append(out, bar(line, m.width, style.FgBg(style.Text, style.Surface)))
	}
	return out
}

// side is the column beside the tests: the task, the selection, the Run button, the keys and the test under
// the cursor.
func (p *pickerState) side(m *Model, r rect) []string {
	const padX = 2
	inner := r.w - 2*padX
	x := r.x + padX
	var out []string
	out = append(out, "", style.Dim.Render("TEST TASK"))

	focused := m.focused() == focusSelect
	value := style.Dim.Render("no test tasks yet")
	if m.testTask != nil {
		value = style.Bold.Render(m.testTask.Path) + style.Muted.Render(fmt.Sprintf("  %d tests", len(m.testTask.Leaves())))
	}
	arrow := style.AccentBold.Render("▾")
	if p.selectOpen {
		arrow = style.AccentBold.Render("▴")
	}
	content := fit(value, inner-4, true) + arrow
	p.selectRect = rect{x, r.y + len(out), inner, selectHeight}
	out = append(out, box([]string{content}, inner, selectHeight, "", "", focused)...)
	m.addZone(zone{r: p.selectRect, click: func(int, int) tea.Cmd {
		m.setFocus(focusSelect)
		p.toggleDropdown(m)
		return nil
	}})
	out = append(out, "")

	label, disabled, summary := p.buttonState(m)
	out = append(out, p.selection(m, summary)...)
	out = append(out, "")
	buttonY := r.y + len(out)
	out = append(out, button(label, inner, disabled, m.focused() == focusButton)...)
	m.addZone(zone{r: rect{x, buttonY, inner, 3}, click: func(int, int) tea.Cmd {
		if _, disabled, _ := p.buttonState(m); disabled {
			return nil
		}
		m.setFocus(focusButton)
		return m.startRun(nil)
	}})
	out = append(out, "", style.Dim.Render("KEYS"))
	for _, key := range pickerKeys {
		out = append(out, style.AccentText.Render(fmt.Sprintf("%-7s", key[0]))+style.Muted.Render(key[1]))
	}
	if p.tests.cursor != "" {
		out = append(out, "", style.LineText.Render(strings.Repeat("─", inner)))
		out = append(out, wrapLines(m.describeTest(p.tests.cursor, true), inner)...)
	}
	return padded(out, r.w, r.h, padX, padX)
}

// selection is how many tests are picked, large, and what Run does with them.
func (p *pickerState) selection(m *Model, summary string) []string {
	if m.testTask == nil {
		return lines(summary)
	}
	picked := 0
	for id := range m.selected {
		if m.selectable[id] {
			picked++
		}
	}
	count := style.Gradient(fmt.Sprint(picked), true, style.Primary, style.Pink)
	if picked == 0 {
		count = style.Bold.Render("0")
	}
	parts := lines(summary)
	out := []string{"", count + style.Muted.Render(fmt.Sprintf(" of %d tests selected", len(m.selectable)))}
	if len(parts) > 2 {
		out = append(out, style.Dim.Render(parts[2]))
	}
	return out
}

func button(label string, width int, disabled, focused bool) []string {
	pad := max(0, (width-ansi.StringWidth(label))/2)
	text := fit(strings.Repeat(" ", pad)+label, width, true)
	blank := strings.Repeat(" ", width)
	switch {
	case disabled:
		base := style.FgBg(style.SubtleText, style.Raised)
		return []string{style.WithBg(blank, base), style.WithBg(text, base), style.WithBg(blank, base)}
	case focused:
		stops := []color.Color{style.Primary, style.Pink}
		return []string{style.GradientBg(blank, style.Base, true, stops...),
			style.GradientBg(text, style.Base, true, stops...), style.GradientBg(blank, style.Base, true, stops...)}
	}
	stops := []color.Color{lipgloss.Darken(style.Pass, 0.2), lipgloss.Darken(style.Teal, 0.25)}
	return []string{style.GradientBg(blank, style.Base, true, stops...),
		style.GradientBg(text, style.Base, true, stops...), style.GradientBg(blank, style.Base, true, stops...)}
}

func (p *pickerState) chain(m *Model) []level {
	var levels []level
	switch p.focus {
	case focusSelect:
		levels = []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
			switch k.String() {
			case "enter", "space", "down", "up":
				p.toggleDropdown(m)
				return true, nil
			}
			return false, nil
		}, bindings: []binding{bind("esc", "Tests", func() tea.Cmd { p.focus = p.tests; return nil })}}}
	case focusButton:
		levels = []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
			switch k.String() {
			case "enter", "space":
				return true, m.startRun(nil)
			}
			return false, nil
		}}}
	default:
		levels = p.tests.levels(m, p.tests)
	}
	return append(levels, level{bindings: []binding{
		bind("ctrl+q", "Quit", m.quit),
		bind("ctrl+f", "Failed", func() tea.Cmd {
			failed := m.failedIDs()
			if len(failed) == 0 {
				m.notify("No failed tests in the last run", "", "information")
				return nil
			}
			m.selected = failed
			return nil
		}),
		bind("ctrl+t", "Task", func() tea.Cmd { p.focus = focusSelect; return nil }),
		bind("ctrl+x", "Stop run", func() tea.Cmd { m.stop(); return nil }).when(m.runner.Running),
		bind("esc", "Watch run", func() tea.Cmd {
			m.screen = runScreen
			return nil
		}).when(func() bool { return !m.runStarted.IsZero() }),
	}})
}

func (p *pickerState) toggleDropdown(m *Model) {
	if m.catalog == nil || len(m.catalog.Paths) == 0 {
		return
	}
	p.selectOpen = !p.selectOpen
	p.selectCursor = 0
	for i, path := range m.catalog.Paths {
		if m.testTask != nil && path == m.testTask.Path {
			p.selectCursor = i
		}
	}
}

func (p *pickerState) dropdownKey(m *Model, k tea.KeyPressMsg) tea.Cmd {
	paths := m.catalog.Paths
	switch k.String() {
	case "esc":
		p.selectOpen = false
	case "up":
		p.selectCursor = max(0, p.selectCursor-1)
	case "down":
		p.selectCursor = min(len(paths)-1, p.selectCursor+1)
	case "enter", "space":
		p.choose(m, p.selectCursor)
	case "ctrl+q", "ctrl+c":
		return m.quit()
	}
	return nil
}

// choose picks a task, then goes back to its tests, since a task is chosen to pick from its tests.
func (p *pickerState) choose(m *Model, i int) {
	p.selectOpen = false
	if i >= 0 && i < len(m.catalog.Paths) {
		m.chooseTask(m.catalog.Paths[i], false)
	}
	p.focus = p.tests
}

func (p *pickerState) renderDropdown(m *Model, screen []string) {
	paths := m.catalog.Paths
	rows := min(len(paths), dropdownMaxItems)
	start := max(0, p.selectCursor-rows+1)
	var content []string
	for i := start; i < start+rows; i++ {
		task := m.catalog.Tasks[paths[i]]
		name := style.Bold.Render(paths[i]) + style.Muted.Render(fmt.Sprintf("  %d tests", len(task.Leaves())))
		if i == p.selectCursor {
			content = append(content, style.WithBg(fit(style.AccentBold.Render("▌")+name, p.selectRect.w-4, true),
				style.Bg(style.Cursor)))
		} else {
			content = append(content, " "+name)
		}
	}
	framed := card(content, p.selectRect.w, style.Accent...)
	y := p.selectRect.y + p.selectRect.h - 1
	if y+len(framed) > m.height-1 {
		y = max(0, p.selectRect.y-len(framed)+1)
	}
	m.addZone(zone{r: rect{0, 0, m.width, m.height}, click: func(int, int) tea.Cmd {
		p.selectOpen = false
		return nil
	}})
	overlay(screen, framed, p.selectRect.x, y)
	m.addZone(zone{r: rect{p.selectRect.x, y, p.selectRect.w, len(framed)}, click: func(_, row int) tea.Cmd {
		if i := start + row - 1; row >= 1 && i < start+rows {
			p.choose(m, i)
		}
		return nil
	}, wheel: func(delta int) {
		p.selectCursor = max(0, min(len(paths)-1, p.selectCursor+delta/3))
	}})
}
