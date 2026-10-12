package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/databases"
	"testdash/internal/logformat"
	"testdash/internal/style"
	"testdash/internal/wiremock"
)

const navWidth = 28

var (
	idSegment      = regexp.MustCompile(`\d`)
	versionSegment = regexp.MustCompile(`^v\d+$`)
)

type runView struct {
	id, name string
	entry    *svcEntry // nil for the overview, the tests and the output
}

type logTab struct {
	id    string
	label string
	view  *logView
}

// runState is the run screen: a sidebar of views, the first an overview of the tests, WireMock, Kafka and the
// logs, then the tests, the application (★) and every other service the run started, and Gradle's output.
type runState struct {
	views      []runView
	current    string
	focus      any
	tests      *testsView
	outputView *logView
	ovOutput   *logView
	logTabs    []logTab
	activeTab  string
	navHover   string
}

func newRunState(m *Model) *runState {
	source := func() []*logformat.Line { return m.output.all() }
	r := &runState{current: "overview", focus: focusPanelTests, tests: newTestsView(false),
		outputView: newLogView(source, m.outputInfo, ""), ovOutput: newLogView(source, m.outputInfo, "")}
	r.views = []runView{{id: "overview", name: "Overview"}, {id: "detail-tests", name: "Tests"},
		{id: "detail-output", name: "Output"}}
	r.logTabs = []logTab{{id: "tab-output", label: "Output", view: r.ovOutput}}
	r.activeTab = "tab-output"
	return r
}

func (r *runState) indexOf(id string) int {
	for i, v := range r.views {
		if v.id == id {
			return i
		}
	}
	return -1
}

func (r *runState) addService(m *Model, entry *svcEntry) {
	index := r.indexOf("detail-output")
	apps := 0
	if entry.svc.IsApp() {
		index = r.indexOf("detail-tests") + 1
		for i, v := range r.views {
			if v.entry != nil && v.entry.svc.IsApp() {
				index = i + 1
			}
		}
	}
	r.views = append(r.views[:index], append([]runView{{id: entry.viewID, name: entry.svc.Key, entry: entry}},
		r.views[index:]...)...)

	// the overview's log tabs: applications first, the first one shown, then the others, then Output
	for _, v := range r.views {
		if v.entry != nil && v.entry != entry && v.entry.svc.IsApp() {
			apps++
		}
	}
	label := entry.svc.Key
	if entry.svc.IsApp() {
		label = "★ " + label
	}
	position := apps
	if !entry.svc.IsApp() {
		for i, t := range r.logTabs {
			if t.id == "tab-output" {
				position = i
			}
		}
	}
	tab := logTab{id: "tab-" + entry.slug, label: label, view: entry.ovLog}
	r.logTabs = append(r.logTabs[:position], append([]logTab{tab}, r.logTabs[position:]...)...)
	if entry.svc.IsApp() && apps == 0 {
		r.activeTab = tab.id
	}
}

func (r *runState) currentView() runView {
	if i := r.indexOf(r.current); i >= 0 {
		return r.views[i]
	}
	return r.views[0]
}

// viewOf is the full view behind a sidebar item.
func (r *runState) viewOf(v runView) view {
	switch {
	case v.entry != nil:
		return v.entry.full
	case v.id == "detail-tests":
		return r.tests
	case v.id == "detail-output":
		return r.outputView
	}
	return nil
}

func (r *runState) activeLog() *logView {
	for _, t := range r.logTabs {
		if t.id == r.activeTab {
			return t.view
		}
	}
	return r.ovOutput
}

// logViews are every log the run screen has, for the inputs they own.
func (r *runState) logViews() []*logView {
	all := []*logView{r.outputView}
	for _, t := range r.logTabs {
		all = append(all, t.view)
	}
	for _, v := range r.views {
		switch full := r.viewOf(v).(type) {
		case *logView:
			all = append(all, full)
		case *wiremockView:
			all = append(all, full.log)
		case *kafkaView:
			all = append(all, full.log)
		case *dbView:
			all = append(all, full.log)
		}
	}
	return all
}

func (r *runState) showView(m *Model, id string, focus bool) {
	if r.indexOf(id) < 0 {
		return
	}
	r.current = id
	if !focus {
		return
	}
	if id == "overview" {
		r.focus = focusPanelTests
	} else {
		r.focus = r.viewOf(r.currentView()).main()
	}
}

func (r *runState) wiremockEntry() *svcEntry {
	return r.firstOfKind("wiremock")
}

func (r *runState) kafkaEntry() *svcEntry {
	return r.firstOfKind("kafka")
}

func (r *runState) firstOfKind(kind string) *svcEntry {
	for _, v := range r.views {
		if v.entry != nil && v.entry.svc.Kind == kind {
			return v.entry
		}
	}
	return nil
}

func isKind(entry *svcEntry, kind string) bool {
	switch kind {
	case "app":
		return entry.svc.IsApp()
	case "database":
		return databases.QueryKinds[entry.svc.Kind]
	}
	return entry.svc.Kind == kind
}

func (r *runState) hasService(kind string) bool {
	for _, v := range r.views {
		if v.entry != nil && isKind(v.entry, kind) {
			return true
		}
	}
	return false
}

// showDuring shows the application's log, WireMock's requests, Kafka's messages or the databases' queries from
// one test's time. Every database's view gets the time, so switching between them keeps it.
func (r *runState) showDuring(m *Model, kind string, w *window) {
	var matching []*svcEntry
	for _, v := range r.views {
		if v.entry != nil && isKind(v.entry, kind) {
			matching = append(matching, v.entry)
		}
	}
	if len(matching) == 0 {
		name := kind
		if kind == "app" {
			name = "application"
		}
		m.notify("No "+name+" in this run", "", "warning")
		return
	}
	if kind != "database" {
		matching = matching[:1]
	}
	for _, entry := range matching {
		switch full := entry.full.(type) {
		case *logView:
			full.setWindow(m, w)
		case *wiremockView:
			full.setWindow(m, w)
		case *kafkaView:
			full.setWindow(m, w)
		case *dbView:
			full.setWindow(m, w)
		}
	}
	r.showView(m, matching[0].viewID, true)
}

func (r *runState) focusables(m *Model) []any {
	targets := []any{focusNav}
	if r.current != "overview" {
		return append(targets, r.viewOf(r.currentView()).focusables(m)...)
	}
	targets = append(targets, focusPanelTests)
	if r.wiremockEntry() != nil {
		targets = append(targets, focusPanelWM)
	}
	if r.kafkaEntry() != nil {
		targets = append(targets, focusPanelKafka)
	}
	return append(targets, r.activeLog().focusables(m)...)
}

func (r *runState) chain(m *Model) []level {
	var levels []level
	switch r.focus {
	case focusNav:
		levels = []level{r.navLevel(m)}
	case focusPanelTests, focusPanelWM, focusPanelKafka:
		panel := r.focus.(focusToken)
		levels = []level{{bindings: []binding{bind("enter", "Open", func() tea.Cmd {
			r.openPanel(m, panel)
			return nil
		})}}}
	default:
		if r.current == "overview" {
			levels = r.activeLog().levels(m, r.focus)
		} else {
			levels = r.viewOf(r.currentView()).levels(m, r.focus)
		}
	}
	onOverview := func() bool { return r.current == "overview" }
	screen := level{bindings: []binding{
		bind("esc", "Back", func() tea.Cmd {
			if r.current != "overview" {
				r.showView(m, "overview", true)
			} else {
				m.toPicker()
			}
			return nil
		}),
		bind("[", "Previous log", func() tea.Cmd { r.logTab(-1); return nil }).hidden().when(onOverview),
		bind("]", "Switch log", func() tea.Cmd { r.logTab(1); return nil }).shown("[ ]").when(onOverview),
		bind("p", "Pick tests", func() tea.Cmd { m.toPicker(); return nil }),
		bind("r", "Run again", func() tea.Cmd { return m.startRun(m.lastSelection) }),
		bind("F", "Run failed", func() tea.Cmd {
			if failed := m.failedIDs(); len(failed) > 0 {
				return m.startRun(failed)
			}
			m.notify("No failed tests in the last run", "", "information")
			return nil
		}),
		bind("x", "Stop", func() tea.Cmd { m.stop(); return nil }),
		bind(":", "Jump", func() tea.Cmd { m.openPalette(); return nil }),
		bind("q", "Quit", m.quit),
	}}
	for n := 1; n <= 9; n++ {
		index := n - 1
		screen.bindings = append(screen.bindings, bind(fmt.Sprint(n), "", func() tea.Cmd {
			if index < len(r.views) {
				r.showView(m, r.views[index].id, true)
			}
			return nil
		}).hidden())
	}
	return append(levels, screen)
}

func (r *runState) navLevel(m *Model) level {
	return level{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
		i := r.indexOf(r.current)
		switch k.String() {
		case "up":
			i--
		case "down":
			i++
		case "home", "pgup":
			i = 0
		case "end", "pgdown":
			i = len(r.views) - 1
		case "enter":
			r.showView(m, r.current, true)
			return true, nil
		default:
			return false, nil
		}
		r.showView(m, r.views[max(0, min(i, len(r.views)-1))].id, false)
		return true, nil
	}}
}

func (r *runState) logTab(step int) {
	for i, t := range r.logTabs {
		if t.id == r.activeTab {
			r.activeTab = r.logTabs[(i+step+len(r.logTabs))%len(r.logTabs)].id
			return
		}
	}
	r.activeTab = r.logTabs[0].id
}

func (r *runState) openPanel(m *Model, panel focusToken) {
	switch panel {
	case focusPanelTests:
		r.showView(m, "detail-tests", true)
	case focusPanelWM:
		if e := r.wiremockEntry(); e != nil {
			r.showView(m, e.viewID, true)
		}
	case focusPanelKafka:
		if e := r.kafkaEntry(); e != nil {
			r.showView(m, e.viewID, true)
		}
	}
}

// rendering

func (r *runState) render(m *Model) []string {
	current := r.currentView()
	hint := "Esc overview"
	if current.id == "overview" {
		hint = "Tab panels · 1-9 views · Esc pick tests"
	}
	out := []string{m.titleBar(current.name, hint)}
	bodyHeight := m.height - 2
	if current.id != "overview" {
		bodyHeight -= 2
	}
	nav := r.renderNav(m, rect{0, 1, navWidth, bodyHeight})
	area := rect{navWidth + 1, 1, m.width - navWidth - 1, bodyHeight}
	var body []string
	if current.id == "overview" {
		body = r.renderOverview(m, area)
	} else {
		body = r.viewOf(current).render(m, area)
	}
	gap := make([]string, bodyHeight)
	for i := range gap {
		gap[i] = " "
	}
	out = append(out, hjoin(nav, gap, block(body, area.w, area.h))...)
	if current.id != "overview" {
		for _, line := range m.statusText() {
			out = append(out, bar(line, m.width, style.FgBg(style.Text, style.Surface)))
		}
	}
	return out
}

// navLabel is a view's name, its icons (shown right-aligned), and a hint that says what they mean.
func (r *runState) navLabel(m *Model, i int, v runView) (string, string, string) {
	number := "  "
	if i < 9 {
		number = fmt.Sprintf("%d ", i+1)
	}
	label := style.Dim.Render(number)
	switch v.id {
	case "detail-tests":
		label += "Tests"
		s := m.runSummary()
		var icons string
		var parts []string
		for _, part := range []struct {
			n           int
			key, status string
		}{{s.passed, "passed", "passed"}, {s.failed, "failed", "failed"}, {s.left, "left", "pending"}} {
			if part.n > 0 {
				mark, st := icon(part.status)
				icons += st.Render(fmt.Sprintf(" %s%d", mark, part.n))
				parts = append(parts, fmt.Sprintf("%d %s", part.n, part.key))
			}
		}
		return label, icons, strings.Join(parts, ", ")
	case "detail-output":
		label += "Output"
		if m.runner.Running() || m.discovering {
			return label, style.Cyan.Render(" ▶"), "Gradle is running"
		}
		return label, "", "Gradle's output"
	}
	if v.entry == nil {
		return label + v.name, "", ""
	}
	service := v.entry.svc
	if service.IsApp() {
		label += style.Yellow.Render("★ ")
	}
	state := service.State()
	if state == "previous" {
		label += style.Dim.Render(service.Key)
	} else {
		label += service.Key
	}
	hint := []string{"from the previous run"}
	switch state {
	case "running":
		hint[0] = "container running"
	case "stopped":
		hint[0] = "container stopped"
	}
	var icons string
	if v.entry.problems > 0 {
		icons += style.Yellow.Render(fmt.Sprintf(" ⚠%d", v.entry.problems))
		hint = append(hint, fmt.Sprintf("%d warnings or errors logged", v.entry.problems))
	}
	if state == "running" {
		icons += style.Green.Render(" ●")
	} else {
		icons += style.Dim.Render(" ■")
	}
	return label, icons, strings.Join(hint, ", ")
}

func (r *runState) renderNav(m *Model, area rect) []string {
	width := area.w
	focused := r.focus == focusNav
	out := []string{"", style.Dim.Render("  VIEWS")}
	top := area.y + len(out)
	for i, v := range r.views {
		if len(out) >= area.h {
			break
		}
		label, icons, _ := r.navLabel(m, i, v)
		iconsWidth := ansi.StringWidth(icons) + 1
		line := fit(" "+label, max(1, width-1-iconsWidth), true) + icons + " "
		if v.id == r.current {
			bg := style.Bg(style.CursorBlur)
			if focused {
				bg = style.BoldBg(style.Text, style.Cursor)
			}
			line = style.AccentBold.Render("▌") + style.WithBg(fit(line, width-1, false), bg)
		} else {
			line = " " + line
		}
		out = append(out, line)
		id := v.id
		m.addZone(zone{r: rect{area.x, top + i, width, 1}, hover: id, click: func(int, int) tea.Cmd {
			r.focus = focusNav
			r.showView(m, id, true)
			return nil
		}})
	}
	m.addZone(zone{r: rect{area.x, area.y + len(out), width, area.h - len(out)}, click: func(int, int) tea.Cmd {
		r.focus = focusNav
		return nil
	}})
	nav := block(out, width, area.h)
	for i := range nav {
		nav[i] = style.WithBg(nav[i], style.Bg(style.Surface))
	}
	return nav
}

// renderNavTip says what a sidebar item's icons mean, beside the sidebar, while the pointer is over it.
func (r *runState) renderNavTip(m *Model, screen []string) {
	if r.navHover == "" {
		return
	}
	i := r.indexOf(r.navHover)
	y := 3 + i
	if i < 0 || y >= m.height-1 {
		return
	}
	_, _, hint := r.navLabel(m, i, r.views[i])
	if hint == "" {
		return
	}
	tip := style.LineText.Render("◀") + style.Pill(hint, style.Text, style.Overlay)
	overlay(screen, []string{tip}, navWidth, y)
}

func (r *runState) renderOverview(m *Model, area rect) []string {
	topHeight := area.h * 2 / 5
	logsHeight := area.h - topHeight
	type panel struct {
		token   focusToken
		title   string
		content func(height, width int) []string
	}
	panels := []panel{{focusPanelTests, "Tests", r.testsPanel(m)}}
	if e := r.wiremockEntry(); e != nil {
		panels = append(panels, panel{focusPanelWM, "WireMock", r.wiremockPanel(m, e)})
	}
	if e := r.kafkaEntry(); e != nil {
		panels = append(panels, panel{focusPanelKafka, "Kafka", r.kafkaPanel(m, e)})
	}
	fractions := make([]int, len(panels))
	for i := range fractions {
		fractions[i] = 1
	}
	widths := splitWidth(area.w, fractions...)
	var row [][]string
	x := area.x
	for i, p := range panels {
		w := widths[i]
		focused := r.focus == p.token
		content := padded(blockEllipsis(p.content(max(1, topHeight-2), max(10, w-4)), max(0, w-4), max(0, topHeight-2)),
			w-2, topHeight-2, 1, 1)
		row = append(row, box(content, w, topHeight, p.title, "", focused))
		token := p.token
		m.addZone(zone{r: rect{x, area.y, w, topHeight}, click: func(int, int) tea.Cmd {
			r.focus = token
			r.openPanel(m, token)
			return nil
		}})
		x += w
	}
	out := hjoin(row...)

	logs := r.activeLog()
	focusWithin := false
	for _, f := range logs.focusables(m) {
		focusWithin = focusWithin || r.focus == f
	}
	inner := rect{area.x + 1, area.y + topHeight + 1, area.w - 2, logsHeight - 2}
	var labels []string
	active := 0
	for i, t := range r.logTabs {
		labels = append(labels, t.label)
		if t.id == r.activeTab {
			active = i
		}
	}
	tabLine, spans := tabs(labels, active, inner.w-1)
	for i, span := range spans {
		id := r.logTabs[i].id
		m.addZone(zone{r: rect{inner.x + 1 + span[0], inner.y, span[1] - span[0], 1}, click: func(int, int) tea.Cmd {
			r.activeTab = id
			return nil
		}})
	}
	content := append([]string{" " + tabLine}, logs.render(m, rect{inner.x, inner.y + 1, inner.w, max(0, inner.h-1)})...)
	out = append(out, box(content, area.w, logsHeight, "Logs", "[ ] switch · / filter · e errors · v simple/full",
		focusWithin)...)
	return out
}

func (r *runState) testsPanel(m *Model) func(height, width int) []string {
	return func(height, width int) []string {
		if m.runStarted.IsZero() {
			return []string{m.stateBadge(), "", style.Muted.Render("Nothing has run yet."),
				style.Muted.Render("Press ") + style.AccentBold.Render("p") + style.Muted.Render(" to pick tests, or ") +
					style.AccentBold.Render("r") + style.Muted.Render(" to run them all.")}
		}
		s := m.runSummary()
		var out []string
		if m.portConflict != "" {
			out = append(out, wrapLines(m.portConflict, width)...)
		}
		head := m.stateBadge() + "  "
		for _, part := range []struct {
			n      int
			status string
		}{{s.passed, "passed"}, {s.failed, "failed"}, {s.skipped, "skipped"}, {s.left, "pending"}} {
			mark, st := icon(part.status)
			if part.n == 0 {
				st = style.Dim
			}
			head += st.Render(fmt.Sprintf("%s %d  ", mark, part.n))
		}
		out = append(out, head, "")
		done := s.passed + s.failed + s.skipped
		fraction := 0.0
		if s.total > 0 {
			fraction = float64(done) / float64(s.total)
		}
		out = append(out, style.Bar(max(10, width-12), fraction, m.barColors(s)...)+
			style.Bold.Render(fmt.Sprintf(" %d", done))+style.Dim.Render(fmt.Sprintf("/%d", s.total)), "")
		if len(s.running) > 0 {
			for _, run := range s.running {
				took, _ := run.state.Took(nowMillis())
				out = append(out, style.Cyan.Render(m.spinnerFrame()+" ")+style.Muted.Render(run.entry.ShortClass()+" › ")+
					style.BoldCyan.Render(cleanText(run.entry.Name))+style.Dim.Render("  "+duration(took)))
			}
		} else if m.runFinished.IsZero() && !m.runStarted.IsZero() {
			out = append(out, m.activity())
		}
		for _, recent := range s.recent {
			if len(out) >= height {
				break
			}
			mark, st := icon(recent.state.Status)
			failed := recent.state.Status == "failed" || recent.state.Status == "aborted"
			name := style.Dim.Render(recent.entry.ShortClass()+" › ") + cleanText(recent.entry.Name)
			if failed {
				name = style.Dim.Render(recent.entry.ShortClass()+" › ") + style.Red.Render(cleanText(recent.entry.Name))
			}
			out = append(out, st.Render(mark+" ")+name)
			if failed && recent.state.Message != "" && len(out) < height {
				first, _, _ := strings.Cut(cleanBlock(recent.state.Message), "\n")
				out = append(out, style.DimRed.Render("    "+first))
			}
		}
		return out
	}
}

func (r *runState) wiremockPanel(m *Model, entry *svcEntry) func(height, width int) []string {
	return func(height, width int) []string {
		view := entry.full.(*wiremockView)
		var unmatched, matched []wiremock.ServeEvent
		for _, e := range view.events {
			if e.Matched {
				matched = append(matched, e)
			} else {
				unmatched = append(unmatched, e)
			}
		}
		unmatchedPill := style.Pill(fmt.Sprintf("%d unmatched", len(unmatched)), style.SubtleText, style.Raised)
		if len(unmatched) > 0 {
			unmatchedPill = style.BoldPill(fmt.Sprintf("✘ %d unmatched", len(unmatched)), style.Fail, style.Tint(style.Fail))
		}
		out := []string{runningDot(entry) + style.Bold.Render(fmt.Sprintf("%d requests", len(view.events))) + "  " +
			unmatchedPill + "  " + style.Dim.Render(fmt.Sprintf("%d stubs", len(view.stubs))), ""}
		if len(view.events) == 0 {
			return append(out, style.Dim.Render("waiting for requests…"))
		}
		room := max(1, height-1)
		unmatchedRows := endpoints(unmatched, true)
		matchedRows := endpoints(matched, false)
		if len(unmatchedRows) > 0 {
			out = append(out, style.BoldRed.Render("UNMATCHED"))
			room--
			limit := room
			if len(matchedRows) > 0 {
				limit = room / 2
			}
			for _, row := range firstN(unmatchedRows, max(1, limit)) {
				out = append(out, style.Red.Render(fmt.Sprintf("  %s %s %s", statusText(row.status), row.method,
					cleanText(row.path)))+times(row.count))
				room--
			}
		}
		if len(matchedRows) > 0 && room > 1 {
			out = append(out, style.Muted.Render("MOST REQUESTED"))
			room--
			for _, row := range firstN(matchedRows, room) {
				out = append(out, fmt.Sprintf("  %s %s %s", statusText(row.status), row.method, cleanText(row.path))+
					times(row.count))
			}
		}
		return out
	}
}

func runningDot(entry *svcEntry) string {
	if entry.svc.State() == "running" {
		return style.Green.Render("● ")
	}
	return style.Dim.Render("■ ")
}

func statusText(status int) string {
	if status == 0 {
		return "None"
	}
	return fmt.Sprint(status)
}

func times(n int) string {
	if n > 1 {
		return style.Dim.Render(fmt.Sprintf("  ×%d", n))
	}
	return ""
}

func (r *runState) kafkaPanel(m *Model, entry *svcEntry) func(height, width int) []string {
	return func(height, width int) []string {
		view := entry.full.(*kafkaView)
		counts := map[string]int{}
		var topics []string
		for _, message := range view.messages {
			if counts[message.Topic] == 0 {
				topics = append(topics, message.Topic)
			}
			counts[message.Topic]++
		}
		lagging := map[string]bool{}
		for _, g := range view.groups {
			if g.Lag != nil && *g.Lag != 0 {
				lagging[g.Group] = true
			}
		}
		head := runningDot(entry) + style.Bold.Render(fmt.Sprintf("%d messages", len(view.messages))) + "  " +
			style.Dim.Render(fmt.Sprintf("%d topics", len(counts)))
		if len(lagging) > 0 {
			head += "  " + style.BoldPill(fmt.Sprintf("%d groups behind", len(lagging)), style.Fail, style.Tint(style.Fail))
		}
		out := []string{head, ""}
		if err := view.client.Error(); err != "" {
			return append(out, style.Red.Render("cannot read messages: "+cleanText(err)))
		}
		if len(view.messages) == 0 {
			return append(out, style.Dim.Render("waiting for messages…"))
		}
		room := max(1, height-1)
		sort.SliceStable(topics, func(i, j int) bool { return counts[topics[i]] > counts[topics[j]] })
		out = append(out, style.Muted.Render("BUSIEST TOPICS"))
		room--
		for _, topic := range firstN(topics, max(1, room/2-1)) {
			out = append(out, "  "+cleanText(topic)+style.Dim.Render(fmt.Sprintf("  ×%d", counts[topic])))
			room--
		}
		if room > 1 {
			out = append(out, style.Muted.Render("LATEST"))
			room--
			for _, message := range lastN(view.messages, room) {
				value := ""
				if message.Value != nil {
					flat := []rune(strings.Join(strings.Fields(cleanText(*message.Value)), " "))
					value = string(flat[:min(width, len(flat))])
				}
				out = append(out, style.Dim.Render("  "+clock(message.At)+" ")+cleanText(message.Topic)+
					style.Dim.Render("  "+value))
			}
		}
		return out
	}
}

type endpointRow struct {
	method, path string
	count        int
	status       int
	latest       time.Time
}

// endpoints groups requests by method and path, ids in the path as {id}, latest first or busiest first.
func endpoints(events []wiremock.ServeEvent, byLatest bool) []endpointRow {
	groups := map[[2]string]*endpointRow{}
	var order []*endpointRow
	for _, e := range events {
		key := [2]string{e.Method, endpoint(e.URL)}
		row := groups[key]
		if row == nil {
			row = &endpointRow{method: key[0], path: key[1]}
			groups[key] = row
			order = append(order, row)
		}
		row.count++
		if !e.At.Before(row.latest) {
			row.status, row.latest = e.Status, e.At
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if byLatest {
			return order[i].latest.After(order[j].latest)
		}
		if order[i].count != order[j].count {
			return order[i].count > order[j].count
		}
		return order[i].latest.After(order[j].latest)
	})
	rows := make([]endpointRow, len(order))
	for i, row := range order {
		rows[i] = *row
	}
	return rows
}

func endpoint(url string) string {
	path, _, _ := strings.Cut(url, "?")
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if idSegment.MatchString(part) && !versionSegment.MatchString(part) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

func sortByFinished(entries []runEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].state.FinishedAt > entries[j].state.FinishedAt })
}

func sortedByTime(events []wiremock.ServeEvent) []wiremock.ServeEvent {
	sorted := append([]wiremock.ServeEvent{}, events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	return sorted
}
