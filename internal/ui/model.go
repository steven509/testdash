package ui

import (
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/kafka"
	"github.com/steven509/testdash/internal/services"
	"github.com/steven509/testdash/internal/style"
)

// panel is one of the screen's three parts.
type panel int

const (
	panelTests panel = iota
	panelServices
	panelDetail
)

// binding is a key and what it does. Shown bindings appear in the footer while they are enabled.
type binding struct {
	keys     []string
	display  string
	label    string
	show     bool
	priority bool // checked before the focused widget gets the key
	enabled  func() bool
	run      func() tea.Cmd
}

func bind(keys, label string, run func() tea.Cmd) binding {
	split := strings.Split(keys, " ")
	return binding{keys: split, display: displayKey(split[0]), label: label, show: true, run: run}
}

func (b binding) hidden() binding {
	b.show = false
	return b
}

func (b binding) when(enabled func() bool) binding {
	b.enabled = enabled
	return b
}

func (b binding) shown(display string) binding {
	b.display = display
	return b
}

func (b binding) first() binding {
	b.priority = true
	return b
}

func (b binding) active() bool {
	return b.enabled == nil || b.enabled()
}

func (b binding) matches(key string) bool {
	for _, k := range b.keys {
		if k == key {
			return true
		}
	}
	return false
}

func displayKey(key string) string {
	switch {
	case strings.HasPrefix(key, "ctrl+"):
		return "^" + strings.TrimPrefix(key, "ctrl+")
	case key == "left":
		return "←"
	case key == "right":
		return "→"
	}
	return key
}

// level is one step of the chain a key goes up: the focused widget, the view around it, the app. Only the
// focused widget's level gets keys raw, for typing and moving.
type level struct {
	raw      func(k tea.KeyPressMsg) (bool, tea.Cmd)
	bindings []binding
}

// zone is a part of the last frame that answers the mouse.
type zone struct {
	r     rect
	click func(x, y int) tea.Cmd // relative to the zone
	wheel func(delta int)
}

func (m *Model) addZone(z zone) {
	m.zones = append(m.zones, z)
}

// popup is a dialog over the screen that takes every key while it is open.
type popup interface {
	key(m *Model, k tea.KeyPressMsg) tea.Cmd
	render(m *Model, screen []string)
}

type notifyMsg struct {
	text, title, severity string
}

type toast struct {
	id                    int
	text, title, severity string
	expires               time.Time
}

func (m *Model) notify(text, title, severity string) {
	m.notifyFor(text, title, severity, 5*time.Second)
}

func (m *Model) notifyFor(text, title, severity string, timeout time.Duration) {
	m.toastSeq++
	m.toasts = append(m.toasts, &toast{id: m.toastSeq, text: text, title: title, severity: severity,
		expires: time.Now().Add(timeout)})
	if len(m.toasts) > 4 {
		m.toasts = m.toasts[len(m.toasts)-4:]
	}
}

func (m *Model) expireToasts() {
	kept := m.toasts[:0]
	for _, t := range m.toasts {
		if time.Now().Before(t.expires) {
			kept = append(kept, t)
		}
	}
	m.toasts = kept
}

func (m *Model) dismissToast(id int) {
	for i, t := range m.toasts {
		if t.id == id {
			m.toasts = append(m.toasts[:i], m.toasts[i+1:]...)
			return
		}
	}
}

type (
	wiremockMsg struct {
		view *wiremockView
		poll wiremockPoll
	}
	kafkaMessagesMsg struct {
		view  *kafkaView
		batch []kafka.Message
	}
	kafkaTopicsMsg struct {
		view   *kafkaView
		topics []string
		groups []kafka.GroupLag
		lag    bool
	}
	queriesMsg struct {
		view  *dbView
		batch []databases.Query
	}
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.dirty = true
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, hideCursor()
	case tea.FocusMsg:
		return m, hideCursor()
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.PasteMsg:
		if p, ok := m.popup.(*palette); ok {
			p.input.insert(msg.Content)
			p.filter(m)
		} else if input := m.focusedInput(); input != nil && m.popup == nil {
			input.insert(msg.Content)
			return m, m.inputChanged(input)
		}
	case tea.MouseClickMsg:
		return m, m.click(msg.Mouse())
	case tea.MouseWheelMsg:
		mouse := msg.Mouse()
		delta := 3
		if mouse.Button == tea.MouseWheelUp {
			delta = -3
		}
		if mouse.Button == tea.MouseWheelUp || mouse.Button == tea.MouseWheelDown {
			for i := len(m.zones) - 1; i >= 0; i-- {
				if z := m.zones[i]; z.wheel != nil && z.r.contains(mouse.X, mouse.Y) {
					z.wheel(delta)
					break
				}
			}
		}
	case tickMsg:
		return m, tea.Batch(m.tick(), tick(), m.spin(), hideCursor())
	case spinner.TickMsg:
		if !m.busy() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, tea.Batch(cmd, hideCursor())
	case outputMsg:
		return m, m.addOutputLines(msg.lines)
	case runDoneMsg:
		return m, m.runDone(msg.code)
	case listeningMsg:
		m.listening = msg.on
		m.updateClaims()
	case discoveredMsg:
		m.discovered(msg)
		cmd := m.pendingCmd
		m.pendingCmd = nil
		return m, cmd
	case notifyMsg:
		m.notify(msg.text, msg.title, msg.severity)
	case portHolderMsg:
		m.explainPort(msg)
	case services.Added:
		m.serviceAdded(msg.Service)
	case services.Changed:
		m.serviceChanged(msg.Service)
	case services.Lines:
		m.serviceLines(msg)
	case extraLinesMsg:
		m.extraLines(msg)
	case extraStoppedMsg:
		msg.entry.svc.SetState("stopped")
	case wiremockMsg:
		msg.view.polled(m, msg.poll)
		m.version++
	case kafkaMessagesMsg:
		msg.view.received(m, msg.batch)
		m.version++
	case kafkaTopicsMsg:
		msg.view.polled(m, msg)
	case queriesMsg:
		msg.view.received(m, msg.batch)
		m.version++
	default:
		m.dirty = false
	}
	return m, nil
}

// hideCursor hides the terminal's cursor again. Bubble Tea hides it once, at the start; a terminal that shows it
// again (after a focus change or a refresh, say) would leave it blinking wherever the screen last changed, which
// while a run goes is the spinner.
func hideCursor() tea.Cmd {
	return tea.Raw(ansi.ResetModeTextCursorEnable)
}

// busy is whether something runs, which the spinners show.
func (m *Model) busy() bool {
	return m.discovering || m.running()
}

// spin starts the spinners when something runs and they are still.
func (m *Model) spin() tea.Cmd {
	if m.spinning || !m.busy() {
		return nil
	}
	m.spinning = true
	return m.spinner.Tick
}

// spinnerFrame is the spinner's current frame, uncoloured.
func (m *Model) spinnerFrame() string {
	return ansi.Strip(m.spinner.View())
}

func (m *Model) quit() tea.Cmd {
	m.runner.Stop()
	m.discovery.Stop()
	return tea.Quit
}

// keys

func (m *Model) globalLevel() level {
	hasFailures := func() bool { return len(m.failedIDs()) > 0 }
	run := bind("r", m.runLabel(), func() tea.Cmd { return m.startRun(m.selected) }).when(func() bool { return !m.running() })
	if m.panelOf(m.focus) == panelTests && len(m.selected) > 0 {
		run = run.hidden() // enter says the same in the tests
	}
	return level{bindings: []binding{
		run,
		bind("F", "Run failed", func() tea.Cmd { return m.startRun(m.failedIDs()) }).
			when(func() bool { return !m.running() && hasFailures() }),
		bind("x", "Stop", func() tea.Cmd { m.stop(); return nil }).when(m.busy),
		bind("ctrl+t", "Task", func() tea.Cmd { m.openTasks(); return nil }).hidden(),
		bind("z", "Zoom", func() tea.Cmd { m.zoomed = !m.zoomed; return nil }).hidden(),
		bind("c", "Command", func() tea.Cmd { m.fullCommand = !m.fullCommand; return nil }).hidden().
			when(func() bool { return m.detailFrom == panelTests && m.tree.cursor == "" && !m.runStarted.IsZero() }),
		bind("?", "Help", func() tea.Cmd { m.popup = &help{}; return nil }).hidden(),
		bind("q ctrl+q ctrl+c", "Quit", func() tea.Cmd { m.popup = &confirmQuit{}; return nil }),
		bind("ctrl+p :", "Commands", func() tea.Cmd { m.openPalette(); return nil }).hidden(),
		bind("ctrl+s", "Settings", func() tea.Cmd { m.openSettings(); return nil }).hidden(),
		bind("tab", "Next", func() tea.Cmd { m.moveFocus(1); return nil }).hidden(),
		bind("shift+tab", "Previous", func() tea.Cmd { m.moveFocus(-1); return nil }).hidden(),
		bind("1", "Tests", func() tea.Cmd { m.zoomed = false; m.setFocus(m.tree); return nil }).hidden(),
		bind("2", "Services", func() tea.Cmd { m.zoomed = false; m.setFocus(m.serviceList); return nil }).hidden(),
		bind("3", "Details", func() tea.Cmd { m.setFocus(m.detailView().main()); return nil }).hidden(),
	}}
}

// detailLevel is what the detail pane does whatever it shows: Esc goes back to the list it shows.
func (m *Model) detailLevel() level {
	return level{bindings: []binding{
		bind("esc", "Back", func() tea.Cmd {
			if m.zoomed {
				m.zoomed = false
			} else if m.detailFrom == panelServices {
				m.setFocus(m.serviceList)
			} else {
				m.setFocus(m.tree)
			}
			return nil
		}),
	}}
}

func (m *Model) chain() []level {
	m.ensureFocus()
	var levels []level
	switch m.panelOf(m.focus) {
	case panelTests:
		levels = m.tree.levels(m)
	case panelServices:
		levels = []level{m.serviceList.level(m)}
	default:
		levels = append(m.detailView().levels(m, m.focus), m.detailLevel())
	}
	return append(levels, m.globalLevel())
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	if m.popup != nil {
		return m.popup.key(m, k)
	}
	key := k.String()
	chain := m.chain()
	if m.focusedInput() == nil {
		for _, l := range chain {
			for _, b := range l.bindings {
				if b.priority && b.matches(key) && b.active() {
					return b.run()
				}
			}
		}
	}
	if raw := chain[0].raw; raw != nil {
		if handled, cmd := raw(k); handled {
			return cmd
		}
	}
	for _, l := range chain {
		for _, b := range l.bindings {
			if !b.priority && b.matches(key) && b.active() {
				return b.run()
			}
		}
	}
	return nil
}

// footerBindings are the shown, enabled bindings of the chain, the focused widget's first; an inner binding
// takes the place of an outer one with the same key.
func (m *Model) footerBindings() []binding {
	var out []binding
	seen := map[string]bool{}
	for _, l := range m.chain() {
		for _, b := range l.bindings {
			if b.show && b.active() && !seen[b.display] {
				seen[b.display] = true
				out = append(out, b)
			}
		}
	}
	return out
}

func keyCap(key string) string {
	return style.BoldPill(key, style.Primary, style.Raised)
}

// keyCapWide is a key cap with its key centred in a fixed width, for lists of keys that line up.
func keyCapWide(key string, width int) string {
	pad := max(0, width-ansi.StringWidth(key))
	return style.BoldPill(strings.Repeat(" ", pad/2)+key+strings.Repeat(" ", pad-pad/2), style.Primary, style.Raised)
}

func (m *Model) footer(y int) string {
	helpText := keyCap("?") + " " + style.Muted.Render("help") + "  "
	commandsText := keyCap("^p") + " " + style.Muted.Render("commands") + " "
	helpWidth, commandsWidth := ansi.StringWidth(helpText), ansi.StringWidth(commandsText)
	rightWidth := helpWidth + commandsWidth
	m.addZone(zone{r: rect{m.width - rightWidth, y, helpWidth, 1},
		click: func(int, int) tea.Cmd { m.popup = &help{}; return nil }})
	m.addZone(zone{r: rect{m.width - commandsWidth, y, commandsWidth, 1},
		click: func(int, int) tea.Cmd { m.openPalette(); return nil }})
	var b strings.Builder
	x := 1
	for _, binding := range m.footerBindings() {
		text := keyCap(binding.display) + " " + style.Muted.Render(binding.label) + "  "
		w := ansi.StringWidth(text)
		if x+w > m.width-rightWidth-1 {
			break
		}
		run := binding.run
		m.addZone(zone{r: rect{x, y, w, 1}, click: func(int, int) tea.Cmd { return run() }})
		b.WriteString(text)
		x += w
	}
	left := " " + b.String()
	gap := max(0, m.width-ansi.StringWidth(left)-rightWidth)
	return left + strings.Repeat(" ", gap) + helpText + commandsText
}

// focus

func (m *Model) panelOf(target any) panel {
	switch target {
	case any(m.tree), any(m.tree.filter):
		return panelTests
	case any(m.serviceList):
		return panelServices
	}
	return panelDetail
}

// detailView is what the detail pane shows: the test, class or run under the tree's cursor, or the service
// under the services list's.
func (m *Model) detailView() view {
	if m.detailFrom == panelServices {
		return m.serviceList.selectedView(m)
	}
	return m.inspector
}

func (m *Model) focusables() []any {
	detail := m.detailView().focusables(m)
	if m.zoomed {
		return detail
	}
	var targets []any
	if m.tree.filterShown {
		targets = append(targets, m.tree.filter)
	}
	return append(append(targets, m.tree, m.serviceList), detail...)
}

func (m *Model) setFocus(target any) {
	m.focus = target
	if p := m.panelOf(target); p != panelDetail {
		m.detailFrom = p
	}
}

func (m *Model) moveFocus(step int) {
	targets := m.focusables()
	if len(targets) == 0 {
		return
	}
	current := -1
	for i, t := range targets {
		if t == m.focus {
			current = i
		}
	}
	next := (current + step + len(targets)) % len(targets)
	if current < 0 {
		next = 0
	}
	m.setFocus(targets[next])
}

// ensureFocus moves focus to something on screen when what had it went away, such as a filter field that
// closed or a tab that changed.
func (m *Model) ensureFocus() {
	for _, target := range m.focusables() {
		if target == m.focus {
			return
		}
	}
	switch {
	case m.zoomed:
		m.focus = m.detailView().main()
	case m.detailFrom == panelServices:
		m.focus = m.serviceList
	default:
		m.focus = m.tree
	}
}

func (m *Model) focusedInput() *lineInput {
	input, _ := m.focus.(*lineInput)
	return input
}

// inputChanged tells whatever owns an input that its value changed.
func (m *Model) inputChanged(input *lineInput) tea.Cmd {
	if input == m.tree.filter {
		return m.tree.inputChanged(m, input)
	}
	for _, v := range m.logViews() {
		if v.input == input {
			return v.inputChanged(m, input)
		}
	}
	return nil
}

// logViews are every log, for the filter fields they own.
func (m *Model) logViews() []*logView {
	all := []*logView{m.outputView}
	for _, entry := range m.svcs {
		switch v := entry.view.(type) {
		case *logView:
			all = append(all, v)
		case *wiremockView:
			all = append(all, v.log)
		case *kafkaView:
			all = append(all, v.log)
		case *dbView:
			all = append(all, v.log)
		}
	}
	return all
}

// mouse

func (m *Model) click(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	for i := len(m.zones) - 1; i >= 0; i-- {
		if z := m.zones[i]; z.click != nil && z.r.contains(mouse.X, mouse.Y) {
			return z.click(mouse.X-z.r.x, mouse.Y-z.r.y)
		}
	}
	return nil
}

// view

func (m *Model) View() tea.View {
	if m.dirty || m.frame == "" {
		m.frame = m.render()
		m.dirty = false
	}
	v := tea.NewView(m.frame)
	v.AltScreen = true
	v.ReportFocus = true // to hide the cursor again when the terminal comes back
	if m.prefs.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	v.BackgroundColor = style.Base
	v.WindowTitle = m.windowTitle()
	v.ProgressBar = m.terminalProgress()
	return v
}

func (m *Model) windowTitle() string {
	title := "testdash · " + baseName(m.opts.Workdir)
	switch {
	case m.runStarted.IsZero():
	case m.runFinished.IsZero():
		title += " · running"
	case m.exitCode == 0:
		title += " · ✔ passed"
	default:
		title += " · ✘ failed"
	}
	return title
}

// terminalProgress is the run's progress for terminals that show one in their tab or taskbar.
func (m *Model) terminalProgress() *tea.ProgressBar {
	if !m.running() {
		return nil
	}
	s := m.runSummary()
	if s.total == 0 {
		return tea.NewProgressBar(tea.ProgressBarIndeterminate, 0)
	}
	value := s.done() * 100 / s.total
	if s.failed > 0 {
		return tea.NewProgressBar(tea.ProgressBarError, value)
	}
	return tea.NewProgressBar(tea.ProgressBarDefault, value)
}

func (m *Model) render() string {
	m.zones = m.zones[:0]
	if m.width < 50 || m.height < 14 {
		return style.Muted.Render("testdash needs a terminal of at least 50×14")
	}
	runningGlyph = m.spinnerFrame()
	m.ensureFocus()
	screen := m.renderScreen()
	m.renderToasts(screen)
	if m.popup != nil {
		m.popup.render(m, screen)
	}
	canvas := style.FgBg(style.Text, style.Base)
	for i, line := range screen {
		screen[i] = style.WithBg(line, canvas)
	}
	return strings.Join(screen, "\n")
}

// card frames lines in a rounded border on the overlay colour, for toasts and dialogs.
func card(lines []string, width int, border ...color.Color) []string {
	inner := make([]string, len(lines))
	for i, line := range lines {
		inner[i] = style.WithBg(" "+fit(line, width-4, true)+" ", style.FgBg(style.Text, style.Overlay))
	}
	frame := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderBackground(style.Overlay)
	if len(border) > 1 {
		frame = frame.BorderForegroundBlend(border...)
	} else {
		frame = frame.BorderForeground(border[0])
	}
	return strings.Split(frame.Render(strings.Join(inner, "\n")), "\n")
}

func (m *Model) renderToasts(screen []string) {
	width := min(56, max(30, m.width/2))
	y := m.height - 2
	for i := len(m.toasts) - 1; i >= 0; i-- {
		t := m.toasts[i]
		color, mark := style.Primary, "●"
		switch t.severity {
		case "success":
			color, mark = style.Pass, ic.passed
		case "warning":
			color, mark = style.Warn, ic.warning
		case "error":
			color, mark = style.Fail, ic.failed
		}
		head := style.New(ansi.NewStyle().Bold().ForegroundColor(color)).Render(mark + " ")
		var body []string
		message := wrapLines(t.text, width-6)
		if t.title != "" {
			body = append(body, head+style.Bold.Render(t.title))
			for _, line := range message {
				body = append(body, "  "+style.Muted.Render(line))
			}
		} else if len(message) > 0 {
			body = append(body, head+message[0])
			for _, line := range message[1:] {
				body = append(body, "  "+line)
			}
		}
		lines := card(body, width, color, lipgloss.Darken(color, 0.5))
		y -= len(lines)
		if y < 2 {
			break
		}
		x := m.width - width - 1
		overlay(screen, lines, x, y)
		id := t.id
		m.addZone(zone{r: rect{x, y, width, len(lines)}, click: func(int, int) tea.Cmd {
			m.dismissToast(id)
			return nil
		}})
	}
}

func baseName(path string) string {
	path = strings.TrimRight(path, "/")
	return path[strings.LastIndex(path, "/")+1:]
}
