package ui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/catalog"
	"testdash/internal/logformat"
	"testdash/internal/progress"
	"testdash/internal/style"
)

const maxTimeline = 500

// inspector is the detail pane for the tests: the whole run, a class, or one test with what happened while it
// ran.
type inspector struct {
	card     *textbox
	events   *table
	event    *textbox
	timeline []timelineEvent
	builtFor string
	builtAt  int // the model's version the timeline was built at
	window   *window
	layout   inspectorLayout
}

type inspectorLayout struct {
	cardHeight   int
	hasTimeline  bool
	tableHeight  int
	detailHeight int
}

// timelineEvent is something a service saw while a test ran.
type timelineEvent struct {
	at      time.Time
	source  string
	color   color.Color
	summary string
	detail  func() string
	service *svcEntry
	reveal  func() // selects the event in its service's view
}

func newInspector() *inspector {
	return &inspector{card: &textbox{}, event: &textbox{}, events: newTable("Time", "Source", "What")}
}

func (in *inspector) clear()                           {}
func (in *inspector) append(*Model, []*logformat.Line) {}

func (in *inspector) main() any {
	if in.layout.hasTimeline && len(in.timeline) > 0 {
		return in.events
	}
	return in.card
}

func (in *inspector) focusables(m *Model) []any {
	if in.layout.hasTimeline && len(in.timeline) > 0 {
		return []any{in.events, in.event, in.card}
	}
	return []any{in.card}
}

func (in *inspector) title(m *Model) (string, string) {
	id := m.tree.cursor
	if id == "" {
		if m.testTask != nil {
			return "Run · " + m.testTask.Path, ""
		}
		return "Run", ""
	}
	entry := m.entries[id]
	if entry == nil {
		return "", ""
	}
	name := cleanText(entry.Name)
	if entry.Kind == "class" {
		name = entry.ShortClass()
	}
	return name, statusPill(m.tree.rowStatus(m, id))
}

func statusPill(status string) string {
	mark, _ := icon(status)
	switch status {
	case "passed":
		return style.Pill(mark+" passed", style.Pass, style.Tint(style.Pass))
	case "failed", "aborted":
		return style.Pill(mark+" "+status, style.Fail, style.Tint(style.Fail))
	case "skipped":
		return style.Pill(mark+" skipped", style.Warn, style.Tint(style.Warn))
	case "running":
		return style.Pill(mark+" running", style.Run, style.Tint(style.Run))
	case "pending":
		return style.Pill(mark+" waiting", style.MutedText, style.Raised)
	case "absent":
		return style.Pill("not in the last run", style.SubtleText, style.Raised)
	}
	return style.Pill("not run yet", style.SubtleText, style.Raised)
}

func (in *inspector) render(m *Model, r rect) []string {
	id := m.tree.cursor
	entry := m.entries[id]
	in.layout = inspectorLayout{cardHeight: r.h}
	switch {
	case id == "" || entry == nil:
		in.card.set("run", m.dashboard(r.w, r.h))
		if in.card.offset == 0 && !m.runStarted.IsZero() { // a click on the command shows or hides all of it
			defer m.addZone(zone{r: rect{r.x, r.y, r.w, 1}, click: func(int, int) tea.Cmd {
				m.fullCommand = !m.fullCommand
				return nil
			}})
		}
	case entry.Kind == "class":
		in.card.set(id, m.classSummary(id, r.w))
	default:
		return in.renderTest(m, entry, r)
	}
	m.addZone(zone{r: r, click: func(int, int) tea.Cmd { m.setFocus(in.card); return nil }, wheel: in.card.scroll})
	return in.card.view(r.w, r.h)
}

func (in *inspector) renderTest(m *Model, entry *catalog.Entry, r rect) []string {
	in.card.set(entry.ID, m.testCard(entry, r.w))
	w := m.windowFor(entry.ID)
	in.window = w
	if w == nil {
		m.addZone(zone{r: r, click: func(int, int) tea.Cmd { m.setFocus(in.card); return nil }, wheel: in.card.scroll})
		return in.card.view(r.w, r.h)
	}
	if in.builtFor != entry.ID || in.builtAt != m.version {
		if in.builtFor != entry.ID {
			in.events.cursor, in.events.offset = 0, 0
		}
		in.timeline = m.timeline(w)
		in.builtFor, in.builtAt = entry.ID, m.version
		in.events.clear()
		for i, ev := range in.timeline {
			in.events.addRow(fmt.Sprint(i), style.Dim.Render(clockMillis(ev.at)),
				style.Pill(ev.source, ev.color, style.Tint(ev.color)), ev.summary)
		}
	}
	cardLines := len(in.card.lines(r.w))
	cardHeight := min(cardLines, max(6, r.h*2/5))
	in.layout = inspectorLayout{cardHeight: cardHeight, hasTimeline: true}
	out := in.card.view(r.w, cardHeight)
	m.addZone(zone{r: rect{r.x, r.y, r.w, cardHeight}, click: func(int, int) tea.Cmd {
		m.setFocus(in.card)
		return nil
	}, wheel: in.card.scroll})
	out = append(out, "")
	rest := r.h - cardHeight - 1
	if len(in.timeline) == 0 {
		return append(out, section("During this test", r.w, ""), "",
			style.Dim.Render("Nothing reached the services while it ran."))
	}
	out = append(out, section("During this test", r.w, count(len(in.timeline), "event")))
	rest--
	tableHeight := max(3, min(len(in.timeline)+1, (rest-1)/2))
	in.layout.tableHeight = tableHeight
	tableArea := rect{r.x, r.y + len(out), r.w, tableHeight}
	out = append(out, in.events.view(r.w, tableHeight, m.focus == in.events)...)
	m.addZone(tableZone(m, tableArea, in.events))
	detailHeight := rest - tableHeight - 1
	in.layout.detailHeight = detailHeight
	if detailHeight < 2 {
		return out
	}
	selected := in.events.cursor
	if selected >= 0 && selected < len(in.timeline) {
		in.event.set(fmt.Sprintf("%s/%d", entry.ID, selected), in.timeline[selected].detail())
	}
	out = append(out, style.LineText.Render(strings.Repeat("─", r.w)))
	area := rect{r.x, r.y + len(out), r.w, detailHeight}
	out = append(out, in.event.view(r.w, detailHeight)...)
	m.addZone(zone{r: area, click: func(int, int) tea.Cmd { m.setFocus(in.event); return nil }, wheel: in.event.scroll})
	return out
}

func (in *inspector) levels(m *Model, focus any) []level {
	switch focus {
	case in.events:
		return []level{tableLevel(in.events, func() tea.Cmd { in.open(m); return nil }), {bindings: []binding{
			bind("enter", "Open in its service", func() tea.Cmd { in.open(m); return nil }),
		}}}
	case in.event:
		return []level{detailLevel(in.event)}
	}
	bindings := []binding{}
	if id := m.tree.cursor; id != "" {
		bindings = append(bindings, bind("enter", "Run "+map[bool]string{true: "class", false: "test"}[m.entries[id] != nil &&
			m.entries[id].Kind == "class"], func() tea.Cmd { return m.startRun(m.selectionFor(id)) }).
			when(func() bool { return !m.running() }))
	}
	return []level{detailLevel(in.card), {bindings: bindings}}
}

// open shows the selected event in its service's view, narrowed to the test's time.
func (in *inspector) open(m *Model) {
	i := in.events.cursor
	if i < 0 || i >= len(in.timeline) {
		return
	}
	m.openEvent(in.timeline[i], in.window)
}

func (m *Model) openEvent(ev timelineEvent, w *window) {
	entry := ev.service
	m.serviceList.selectEntry(m, entry)
	m.detailFrom = panelServices
	switch v := entry.view.(type) {
	case *logView:
		v.setWindow(m, w)
	case *wiremockView:
		v.setWindow(m, w)
	case *kafkaView:
		v.setWindow(m, w)
	case *dbView:
		v.setWindow(m, w)
	}
	if ev.reveal != nil {
		ev.reveal()
	}
	m.focus = entry.view.main()
}

// timeline is everything the services saw in a window, in time order: requests, messages, queries, and the
// application's warnings and errors.
func (m *Model) timeline(w *window) []timelineEvent {
	var events []timelineEvent
	for _, entry := range m.svcs {
		entry := entry
		switch v := entry.view.(type) {
		case *wiremockView:
			for _, e := range v.eventsBetween(w) {
				e := e
				summary := style.Bold.Render(cleanText(e.Method)) + " " + cleanText(e.URL) + " " + httpStatus(e.Status)
				if !e.Matched {
					summary += style.Red.Render("  no stub")
				}
				events = append(events, timelineEvent{at: e.At, source: "HTTP", color: style.Teal, summary: summary,
					service: entry, reveal: func() { v.requests.selectKey("r:" + e.ID) },
					detail: func() string { return describeRequest(e, nil) }})
			}
		case *kafkaView:
			for _, message := range v.messagesBetween(w) {
				message := message
				summary := style.Bold.Render(cleanText(message.Topic)) + "  " + style.Dim.Render(preview(message.Value, 80))
				events = append(events, timelineEvent{at: message.At, source: "KAFKA", color: style.Pink, summary: summary,
					service: entry, reveal: func() { v.table.selectKey(messageKey(message)) },
					detail: func() string { return describeMessage(message) }})
			}
		case *dbView:
			source := "SQL"
			if v.mongo {
				source = "MONGO"
			}
			for _, q := range v.queriesBetween(w) {
				q := q
				text := cleanText(q.Text)
				summary := preview(&text, 100)
				if q.Target != "" {
					summary = style.Bold.Render(q.Kind+" "+q.Target) + "  " + style.Dim.Render(preview(&text, 80))
				}
				events = append(events, timelineEvent{at: q.At, source: source, color: style.Orange, summary: summary,
					service: entry, reveal: func() { v.selectQuery(q) }, detail: func() string { return describeQuery(q) }})
			}
		default:
			if !entry.svc.IsApp() {
				continue
			}
			lines := entry.lines.all()
			for i, line := range lines {
				if line.At.IsZero() || !w.holds(line.At) || !line.IsLoggedProblem() {
					continue
				}
				record := line.Record()
				level, message := "WARN", cleanText(line.Text)
				if record != nil {
					level, message = record.Level, cleanText(strings.SplitN(record.Message, "\n", 2)[0])
				}
				color := style.Warn
				if level == "ERROR" || level == "FATAL" {
					color = style.Fail
				}
				follow := continuation(lines, i)
				events = append(events, timelineEvent{at: line.At, source: "LOG", color: color,
					summary: style.Fg(color).Render(level) + " " + message, service: entry,
					detail: func() string {
						var b strings.Builder
						b.WriteString(line.Render(m.logSimple, true))
						for _, more := range follow {
							b.WriteString("\n" + more.Render(m.logSimple, false))
						}
						return b.String()
					}})
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	return lastN(events, maxTimeline)
}

// continuation is the stack trace lines after a log line, up to the next record.
func continuation(lines []*logformat.Line, at int) []*logformat.Line {
	var follow []*logformat.Line
	for i := at + 1; i < len(lines) && len(follow) < 40; i++ {
		if lines[i].Record() != nil {
			break
		}
		follow = append(follow, lines[i])
	}
	return follow
}

func httpStatus(status int) string {
	switch {
	case status == 0:
		return style.Dim.Render("—")
	case status < 400:
		return style.Green.Render(fmt.Sprint(status))
	}
	return style.Red.Render(fmt.Sprint(status))
}

// testCard is a test's name, where it sits, how it did, and its failure with its stack trace.
func (m *Model) testCard(entry *catalog.Entry, width int) string {
	var b strings.Builder
	pkg, _ := splitClass(entry.ClassName)
	if pkg != "" {
		b.WriteString(style.Dim.Render(pkg + " › "))
	}
	b.WriteString(style.Muted.Render(strings.Join(m.groupOf(entry), " › ")))
	if entry.Method != "" {
		b.WriteString(style.Dim.Render(" · " + entry.Method + "()"))
	}
	b.WriteString("\n\n")
	status, state := m.status(entry.ID), m.state(entry.ID)
	b.WriteString(statusPill(status))
	if state.StartedAt != 0 && status != "absent" {
		took, _ := state.Took(nowMillis())
		b.WriteString("  " + style.Bold.Render(duration(took)) + style.Dim.Render("  started "+
			clock(time.UnixMilli(state.StartedAt))))
	}
	if m.selected[entry.ID] {
		b.WriteString("  " + style.Pill("selected", style.Primary, style.Raised))
	}
	b.WriteString("\n")
	switch status {
	case "idle", "absent":
		b.WriteString("\n" + style.Muted.Render("Select it with ") + style.AccentBold.Render("space") +
			style.Muted.Render(" and run the selection with ") + style.AccentBold.Render("r") +
			style.Muted.Render(", or run just it with ") + style.AccentBold.Render("enter") + style.Muted.Render(" from the details."))
	case "failed", "aborted", "skipped":
		if state.Message != "" || state.Trace != "" {
			b.WriteString("\n" + failure(state, entry, width))
		}
	case "running":
		b.WriteString("\n" + style.Cyan.Render(runningGlyph+" running…"))
	}
	return b.String()
}

// failure is a test's message and stack trace beside a bar in its colour. The project's own frames stay bright;
// runs of library and framework frames fold into one line.
func failure(state progress.State, entry *catalog.Entry, width int) string {
	barColor := style.Fail
	if state.Status == "skipped" {
		barColor = style.Warn
	}
	bar := style.Fg(barColor).Render("┃ ")
	var lines []string
	message := cleanBlock(state.Message)
	trace := cleanBlock(state.Trace)
	first, rest, _ := strings.Cut(trace, "\n")
	if message == "" {
		message = first
	}
	for _, line := range wrapLines(message, width-2) {
		lines = append(lines, bar+style.Fg(barColor).Render(line))
	}
	own := projectRoot(entry.ClassName)
	folded := 0
	fold := func() {
		if folded > 0 {
			lines = append(lines, bar+style.Dim.Render(fmt.Sprintf("  ⋯ %s", count(folded, "library frame"))))
			folded = 0
		}
	}
	for _, frame := range strings.Split(rest, "\n") {
		text := strings.TrimSpace(frame)
		switch {
		case text == "":
		case strings.HasPrefix(text, "Caused by:") || strings.HasPrefix(text, "Suppressed:"):
			fold()
			lines = append(lines, bar+fit(style.Red.Render(text), width-2, true))
		case strings.HasPrefix(text, "at "+own):
			fold()
			lines = append(lines, bar+fit("  "+style.Bold.Render(text), width-2, true))
		case strings.HasPrefix(text, "..."):
			fold()
			lines = append(lines, bar+style.Dim.Render("  "+text))
		default:
			folded++
		}
	}
	fold()
	return strings.Join(lines, "\n")
}

// projectRoot is the start of a class's package that the project's own code shares: its first two parts.
func projectRoot(className string) string {
	parts := strings.Split(className, ".")
	if len(parts) > 2 {
		return strings.Join(parts[:2], ".") + "."
	}
	return className
}

func splitClass(className string) (string, string) {
	i := strings.LastIndex(className, ".")
	if i < 0 {
		return "", className
	}
	return className[:i], className[i+1:]
}

// classSummary is a class's results, its tests with their times as bars, and its failures.
func (m *Model) classSummary(id string, width int) string {
	entry := m.entries[id]
	var b strings.Builder
	pkg, _ := splitClass(entry.ClassName)
	b.WriteString(style.Dim.Render(pkg) + "\n")
	b.WriteString(style.Gradient(ansi.Strip(m.tree.name(m, id)), true, style.Text, style.Primary) + "\n\n")
	leaves := m.leavesUnder(id)
	counts := map[string]int{}
	for _, leaf := range leaves {
		counts[m.status(leaf)]++
	}
	b.WriteString(countLine(counts, len(leaves)) + "\n")
	b.WriteString(stackedBar(counts, len(leaves), width) + "\n\n")
	var longest int64
	for _, leaf := range leaves {
		if took, ok := m.state(leaf).Took(nowMillis()); ok && took > longest {
			longest = took
		}
	}
	b.WriteString(section("Tests", width, "") + "\n")
	nameWidth := min(80, max(20, width*3/5))
	for _, leaf := range leaves {
		mark, markStyle := icon(m.status(leaf))
		line := markStyle.Render(mark) + " " + fit(cleanText(m.entries[leaf].Name), nameWidth, true) + " "
		if took, ok := m.state(leaf).Took(nowMillis()); ok && m.status(leaf) != "absent" {
			barWidth := max(4, width-nameWidth-11)
			fraction := 0.0
			if longest > 0 {
				fraction = float64(took) / float64(longest)
			}
			line += style.BarOf(barWidth, fraction, "▬", style.Primary, style.Secondary) + " " +
				style.Dim.Render(duration(took))
		}
		b.WriteString(line + "\n")
	}
	if failures := m.failuresIn(leaves); failures != "" {
		b.WriteString("\n" + section("Failures", width, "") + "\n" + failures)
	}
	return b.String()
}

func countLine(counts map[string]int, total int) string {
	var parts []string
	for _, part := range []struct {
		status, label string
		color         color.Color
	}{{"passed", "passed", style.Pass}, {"failed", "failed", style.Fail}, {"skipped", "skipped", style.Warn},
		{"running", "running", style.Run}, {"pending", "waiting", style.MutedText}} {
		n := counts[part.status]
		if part.status == "failed" {
			n += counts["aborted"]
		}
		if n > 0 {
			mark, _ := icon(part.status)
			parts = append(parts, style.Pill(fmt.Sprintf("%s %d %s", mark, n, part.label), part.color, style.Tint(part.color)))
		}
	}
	if notRun := counts["idle"] + counts["absent"]; notRun > 0 {
		parts = append(parts, style.Pill(fmt.Sprintf("%d not run", notRun), style.SubtleText, style.Raised))
	}
	parts = append(parts, style.Dim.Render(count(total, "test")))
	return strings.Join(parts, " ")
}

func stackedBar(counts map[string]int, total, width int) string {
	return style.Stacked(width, total, style.Segment{Count: counts["passed"], Color: style.Pass},
		style.Segment{Count: counts["failed"] + counts["aborted"], Color: style.Fail},
		style.Segment{Count: counts["skipped"], Color: style.Warn},
		style.Segment{Count: counts["running"], Color: style.Run})
}

// failuresIn lists the failed tests among some, each with the first line of its message.
func (m *Model) failuresIn(ids []string) string {
	var b strings.Builder
	for _, id := range ids {
		status := m.status(id)
		if status != "failed" && status != "aborted" {
			continue
		}
		entry := m.entries[id]
		b.WriteString(style.Red.Render(ic.failed+" ") + style.Bold.Render(m.testName(entry)) +
			style.Dim.Render("  "+entry.ShortClass()) + "\n")
		if message := m.state(id).Message; message != "" {
			first, _, _ := strings.Cut(cleanBlock(message), "\n")
			b.WriteString(style.DimRed.Render("  "+first) + "\n")
		}
	}
	return b.String()
}

func (m *Model) slowest(ids []string, n, width int) string {
	type timed struct {
		id   string
		took int64
	}
	var all []timed
	for _, id := range ids {
		if took, ok := m.state(id).Took(nowMillis()); ok {
			all = append(all, timed{id, took})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].took > all[j].took })
	all = firstN(all, n)
	if len(all) == 0 {
		return ""
	}
	nameWidth := min(80, max(20, width*3/5))
	var b strings.Builder
	for _, t := range all {
		fraction := float64(t.took) / float64(all[0].took)
		b.WriteString(fit(m.testName(m.entries[t.id]), nameWidth, true) + " " +
			style.BarOf(max(4, width-nameWidth-9), fraction, "▬", style.Primary, style.Secondary) + " " +
			style.Dim.Render(fmt.Sprintf("%6s", duration(t.took))) + "\n")
	}
	return b.String()
}

// welcome is the dashboard before any run: what there is to run, and the keys that do it.
func (m *Model) welcome(width int) string {
	var b strings.Builder
	b.WriteString(style.Gradient("Ready when you are", true, style.Accent...) + "\n\n")
	if m.testTask != nil {
		classes := 0
		for _, e := range m.testTask.Entries {
			if e.Kind == "class" && m.entries[e.Parent] == nil {
				classes++
			}
		}
		b.WriteString(style.Bold.Render(count(len(m.selectable), "test")) + style.Muted.Render(" in ") +
			style.Bold.Render(count(classes, "class", "classes")) + style.Muted.Render(" in ") +
			style.Pill(m.testTask.Path, style.Teal, style.Raised) + "\n\n")
	} else {
		b.WriteString(style.Muted.Render(m.noTaskReason()) + "\n\n")
	}
	keys := [][2]string{
		{"r", "run everything, or the tests you selected"},
		{"space", "select the test or class under the cursor"},
		{"enter", "run the selection, or else the test or class under the cursor"},
		{"/", "filter the tests by name"},
		{"s", "show only failed or unfinished tests"},
		{"tab", "move between the tests, services and these details"},
		{"^t", "choose another test task"},
		{"?", "every key"},
	}
	for _, k := range keys {
		b.WriteString(keyCapWide(k[0], 5) + "  " + style.Muted.Render(k[1]) + "\n")
	}
	b.WriteString("\n" + style.Dim.Render("While tests run, the containers they start appear under Services, and each "+
		"test shows what reached them while it ran."))
	return strings.Join(wrapLines(b.String(), width), "\n")
}
