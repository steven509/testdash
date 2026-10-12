package ui

import (
	"image/color"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/databases"
	"testdash/internal/kafka"
	"testdash/internal/services"
	"testdash/internal/style"
)

// binding is a key and what it does. Shown bindings appear in the footer while they are enabled.
type binding struct {
	keys     []string
	display  string
	label    string
	show     bool
	priority bool // checked before the focused widget, as Textual's priority bindings are
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
	case key == "enter":
		return "⏎"
	}
	return key
}

// level is one step of the chain a key goes up: the focused widget, the views around it, the screen, the app.
// Only the focused widget's level gets keys raw, for typing and moving.
type level struct {
	raw      func(k tea.KeyPressMsg) (bool, tea.Cmd)
	bindings []binding
}

// zone is a part of the last frame that answers the mouse.
type zone struct {
	r     rect
	click func(x, y int) tea.Cmd // relative to the zone
	wheel func(delta int)
	hover string // a sidebar item's view id, whose tip shows on hover
}

func (m *Model) addZone(z zone) {
	m.zones = append(m.zones, z)
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
	if len(m.toasts) > 5 {
		m.toasts = m.toasts[len(m.toasts)-5:]
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

type (
	wiremockMsg struct {
		view  *wiremockView
		poll  wiremockPoll
		reset bool
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
	findMsg struct {
		view *testsView
		seq  int
	}
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.dirty = true
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.PasteMsg:
		if input := m.focusedInput(); input != nil {
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
	case tea.MouseMotionMsg:
		mouse := msg.Mouse()
		hover := ""
		for i := len(m.zones) - 1; i >= 0; i-- {
			if z := m.zones[i]; z.r.contains(mouse.X, mouse.Y) {
				hover = z.hover
				break
			}
		}
		m.dirty = hover != m.run.navHover
		m.run.navHover = hover
	case tickMsg:
		return m, tea.Batch(m.tick(), tick(), m.spin())
	case spinner.TickMsg:
		if !m.busy() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case outputMsg:
		return m, m.addOutputLines(msg.lines)
	case runDoneMsg:
		return m, m.runDone(msg.code)
	case listeningMsg:
		m.listening = msg.on
		m.updateClaims()
	case discoveredMsg:
		m.discovered(msg)
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
	case kafkaMessagesMsg:
		msg.view.received(m, msg.batch)
	case kafkaTopicsMsg:
		msg.view.polled(m, msg)
	case queriesMsg:
		msg.view.received(m, msg.batch)
	case findMsg:
		if msg.seq == msg.view.findSeq {
			msg.view.find(m)
		}
	default:
		m.dirty = false
	}
	return m, nil
}

// busy is whether something runs, which the spinners show.
func (m *Model) busy() bool {
	return m.discovering || (!m.runStarted.IsZero() && m.runFinished.IsZero())
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

func (m *Model) appLevel() level {
	return level{bindings: []binding{
		bind("ctrl+q ctrl+c", "Quit", m.quit).hidden(),
		bind("ctrl+p", "palette", func() tea.Cmd { m.openPalette(); return nil }).hidden(),
		bind("tab", "Next", func() tea.Cmd { m.moveFocus(1); return nil }).hidden(),
		bind("shift+tab", "Previous", func() tea.Cmd { m.moveFocus(-1); return nil }).hidden(),
	}}
}

func (m *Model) chain() []level {
	m.ensureFocus()
	var levels []level
	if m.screen == pickerScreen {
		levels = m.picker.chain(m)
	} else {
		levels = m.run.chain(m)
	}
	return append(levels, m.appLevel())
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	if m.palette != nil {
		return m.palette.key(m, k)
	}
	if m.screen == pickerScreen && m.picker.selectOpen {
		return m.picker.dropdownKey(m, k)
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

// footerBindings are the shown, enabled bindings of the chain, the focused widget's first, as Textual's footer
// lists them; an inner binding takes the place of an outer one with the same key.
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

func (m *Model) footer(y int) string {
	var b strings.Builder
	x := 1
	keyCap := func(key string) string { return style.BoldPill(key, style.Primary, style.Raised) }
	for _, binding := range m.footerBindings() {
		text := keyCap(binding.display) + " " + style.Muted.Render(binding.label) + "  "
		w := ansi.StringWidth(text)
		if x+w > m.width-14 {
			break
		}
		run := binding.run
		m.addZone(zone{r: rect{x, y, w, 1}, click: func(int, int) tea.Cmd { return run() }})
		b.WriteString(text)
		x += w
	}
	palette := keyCap("^p") + " " + style.Muted.Render("palette") + " "
	pw := ansi.StringWidth(palette)
	if gap := m.width - 1 - x - pw; gap > 0 {
		b.WriteString(strings.Repeat(" ", gap) + palette)
		m.addZone(zone{r: rect{m.width - pw, y, pw, 1}, click: func(int, int) tea.Cmd { m.openPalette(); return nil }})
	}
	return style.WithBg(" "+fit(b.String(), m.width-1, false), style.Bg(style.Surface))
}

// focus

func (m *Model) focusables() []any {
	if m.screen == pickerScreen {
		return m.picker.focusables(m)
	}
	return m.run.focusables(m)
}

// ensureFocus moves focus to the view's main widget when what had it is no longer on screen, such as another
// log tab's filter field.
func (m *Model) ensureFocus() {
	for _, target := range m.focusables() {
		if target == m.focused() {
			return
		}
	}
	switch {
	case m.screen == pickerScreen:
		m.picker.focus = m.picker.tests
	case m.run.current == "overview":
		m.run.focus = focusPanelTests
	default:
		m.run.focus = m.run.viewOf(m.run.currentView()).main()
	}
}

func (m *Model) focused() any {
	if m.screen == pickerScreen {
		return m.picker.focus
	}
	return m.run.focus
}

func (m *Model) setFocus(target any) {
	if m.screen == pickerScreen {
		m.picker.focus = target
	} else {
		m.run.focus = target
	}
}

func (m *Model) moveFocus(step int) {
	targets := m.focusables()
	if len(targets) == 0 {
		return
	}
	current := -1
	for i, t := range targets {
		if t == m.focused() {
			current = i
		}
	}
	next := (current + step + len(targets)) % len(targets)
	if current < 0 {
		next = 0
	}
	m.setFocus(targets[next])
}

func (m *Model) focusedInput() *lineInput {
	input, _ := m.focused().(*lineInput)
	return input
}

// inputChanged tells the view that owns an input its value changed.
func (m *Model) inputChanged(input *lineInput) tea.Cmd {
	if owner := m.inputOwner(input); owner != nil {
		return owner.inputChanged(m, input)
	}
	return nil
}

type inputOwner interface {
	inputChanged(m *Model, input *lineInput) tea.Cmd
}

func (m *Model) inputOwner(input *lineInput) inputOwner {
	if m.run.tests.findInput == input {
		return m.run.tests
	}
	for _, v := range m.run.logViews() {
		if v.input == input {
			return v
		}
	}
	return nil
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
	v.MouseMode = tea.MouseModeAllMotion
	v.BackgroundColor = style.Base
	v.WindowTitle = m.windowTitle()
	v.ProgressBar = m.terminalProgress()
	return v
}

func (m *Model) windowTitle() string {
	title := "testdash · " + baseName(m.settings.Workdir)
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
	if m.runStarted.IsZero() || !m.runFinished.IsZero() {
		return nil
	}
	s := m.runSummary()
	if s.total == 0 {
		return tea.NewProgressBar(tea.ProgressBarIndeterminate, 0)
	}
	value := (s.passed + s.failed + s.skipped) * 100 / s.total
	if s.failed > 0 {
		return tea.NewProgressBar(tea.ProgressBarError, value)
	}
	return tea.NewProgressBar(tea.ProgressBarDefault, value)
}

func (m *Model) render() string {
	m.zones = m.zones[:0]
	runningGlyph = m.spinnerFrame()
	m.ensureFocus()
	if m.width < 40 || m.height < 12 {
		return style.Muted.Render("testdash needs a bigger terminal")
	}
	var screen []string
	if m.screen == pickerScreen {
		screen = m.picker.render(m)
	} else {
		screen = m.run.render(m)
	}
	screen = block(screen, m.width, m.height-1)
	screen = append(screen, m.footer(m.height-1))
	if m.screen == pickerScreen && m.picker.selectOpen {
		m.picker.renderDropdown(m, screen)
	}
	if m.screen == runScreen {
		m.run.renderNavTip(m, screen)
	}
	m.renderToasts(screen)
	if m.palette != nil {
		m.palette.render(m, screen)
	}
	canvas := style.FgBg(style.Text, style.Base)
	for i, line := range screen {
		screen[i] = style.WithBg(line, canvas)
	}
	return strings.Join(screen, "\n")
}

// card frames lines in a rounded border on the overlay colour, for toasts, the palette and dropdowns.
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
	width := min(64, max(30, m.width/2))
	y := m.height - 1
	for i := len(m.toasts) - 1; i >= 0; i-- {
		t := m.toasts[i]
		color, mark := style.Primary, "●"
		switch t.severity {
		case "warning":
			color, mark = style.Warn, "▲"
		case "error":
			color, mark = style.Fail, "✘"
		}
		head := style.New(ansi.NewStyle().Bold().ForegroundColor(color)).Render(mark + " ")
		var body []string
		message := wrapLines(t.text, width-6)
		if t.title != "" {
			body = append(body, head+style.Bold.Render(t.title))
			for _, line := range message {
				body = append(body, "  "+line)
			}
		} else if len(message) > 0 {
			body = append(body, head+message[0])
			for _, line := range message[1:] {
				body = append(body, "  "+line)
			}
		}
		lines := card(body, width, color, lipgloss.Darken(color, 0.5))
		y -= len(lines)
		if y < 1 {
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

func (m *Model) dismissToast(id int) {
	for i, t := range m.toasts {
		if t.id == id {
			m.toasts = append(m.toasts[:i], m.toasts[i+1:]...)
			return
		}
	}
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
	var all []command
	for i, v := range m.run.views {
		id := v.id
		all = append(all, command{"Show " + v.name, "The " + v.name + " view (" + strconv.Itoa(i+1) + ")", func() tea.Cmd {
			m.screen = runScreen
			m.run.showView(m, id, true)
			return nil
		}})
	}
	return append(all,
		command{"Pick tests", "The test picker", func() tea.Cmd { m.toPicker(); return nil }},
		command{"Run selected tests", "Run the picker's selection", func() tea.Cmd { return m.startRun(nil) }},
		command{"Run failed tests", "Run the tests that failed last time", func() tea.Cmd {
			return m.startRun(m.failedIDs())
		}},
		command{"Stop", "Stop the run (x)", func() tea.Cmd { m.stop(); return nil }},
		command{"List tests again", "Rediscover the build's tests", m.discover},
		command{"Toggle simple or full log lines", "Every log (v)", func() tea.Cmd {
			m.logSimple = !m.logSimple
			return nil
		}},
		command{"Toggle log times", "Every log (t)", func() tea.Cmd { m.logTime = !m.logTime; return nil }},
		command{"Quit", "Quit testdash", m.quit},
	)
}

func (m *Model) openPalette() {
	m.palette = &palette{input: lineInput{placeholder: "Search for commands…"}}
	m.palette.filter(m)
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
	score, last := 0, -2
	position := 0
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
		m.palette = nil
	case "up":
		p.cursor = max(0, p.cursor-1)
	case "down":
		p.cursor = min(len(p.shown)-1, p.cursor+1)
	case "enter":
		return p.choose(m, p.cursor)
	case "ctrl+q", "ctrl+c":
		return m.quit()
	default:
		if handled, changed := p.input.key(k); handled && changed {
			p.filter(m)
		}
	}
	return nil
}

func (p *palette) choose(m *Model, i int) tea.Cmd {
	m.palette = nil
	if i >= 0 && i < len(p.shown) {
		return p.shown[i].run()
	}
	return nil
}

func (p *palette) render(m *Model, screen []string) {
	width := min(80, m.width-4)
	x := (m.width - width) / 2
	rows := min(len(p.shown), 12)
	var content []string
	content = append(content, style.AccentBold.Render("❯ ")+fit(paletteInput(&p.input), width-6, false))
	content = append(content, style.LineText.Render(strings.Repeat("─", width-4)))
	start := max(0, p.cursor-rows+1)
	for i := start; i < start+rows && i < len(p.shown); i++ {
		c := p.shown[i]
		if i == p.cursor {
			line := style.AccentBold.Render("▌") + style.Bold.Render(c.name) + "  " + style.Muted.Render(c.help)
			content = append(content, style.WithBg(fit(line, width-4, true), style.Bg(style.Cursor)))
		} else {
			content = append(content, " "+style.Bold.Render(c.name)+"  "+style.Dim.Render(c.help))
		}
	}
	if len(p.shown) == 0 {
		content = append(content, style.Dim.Render(" No matching commands"))
	}
	framed := card(content, width, style.Accent...)
	overlay(framed, []string{style.BoldPill("Commands", style.Base, style.Primary)}, 2, 0)
	m.addZone(zone{r: rect{0, 0, m.width, m.height}, click: func(int, int) tea.Cmd {
		m.palette = nil
		return nil
	}})
	overlay(screen, framed, x, 2)
	m.addZone(zone{r: rect{x, 2, width, len(framed)}, click: func(_, y int) tea.Cmd {
		if i := start + y - 3; y >= 3 && i < len(p.shown) {
			return p.choose(m, i)
		}
		return nil
	}})
}

func paletteInput(in *lineInput) string {
	if len(in.value) == 0 {
		return ansi.NewStyle().Reverse(true).Styled(" ") + style.Dim.Render(in.placeholder)
	}
	before := string(in.value[:in.cursor])
	after := string(in.value[in.cursor:])
	caret := " "
	if after != "" {
		caret, after = string([]rune(after)[0]), string([]rune(after)[1:])
	}
	return style.Bold.Render(before) + ansi.NewStyle().ForegroundColor(style.Base).BackgroundColor(style.Primary).Styled(caret) +
		style.Bold.Render(after)
}

func (m *Model) toPicker() {
	m.screen = pickerScreen
	m.picker.resume(m)
}

func baseName(path string) string {
	path = strings.TrimRight(path, string(os.PathSeparator))
	return path[strings.LastIndex(path, string(os.PathSeparator))+1:]
}
