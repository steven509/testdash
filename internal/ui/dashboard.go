package ui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/catalog"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/style"
)

// dashboard is the run as a whole: how it is going, what failed, how busy each service has been, every class
// on the run's time line, and while it runs, a live feed of what the build and the services are doing, filling
// what is left of height. Before any run, how to start.
func (m *Model) dashboard(width, height int) string {
	if m.runStarted.IsZero() {
		return m.welcome(width)
	}
	s := m.runSummary()
	var b strings.Builder
	b.WriteString(m.commandLine(width) + "\n\n")
	counts := map[string]int{"passed": s.passed, "failed": s.failed, "skipped": s.skipped, "pending": s.left}
	b.WriteString(countLine(counts, s.total) + "\n")
	b.WriteString(style.Bar(width, float64(s.done())/float64(max(1, s.total)), m.barColors(s)...) + "\n")
	if m.portConflict != "" {
		b.WriteString("\n" + style.Red.Render(ic.failed+" ") + style.BoldRed.Render("The run could not start its containers") + "\n")
		b.WriteString(strings.Join(wrapLines(style.Muted.Render(m.portConflict), width-2), "\n") + "\n")
	}
	if m.running() {
		b.WriteString("\n" + section("Now", width, "") + "\n")
		if len(s.running) == 0 {
			b.WriteString(style.Cyan.Render(runningGlyph+" "+m.activity()) + "\n")
		}
		for _, r := range s.running {
			took, _ := r.state.Took(nowMillis())
			b.WriteString(style.Cyan.Render(runningGlyph+" ") + style.Muted.Render(strings.Join(m.groupOf(r.entry), " › ")+" › ") +
				style.Bold.Render(cleanText(r.entry.Name)) + style.Dim.Render("  "+duration(took)) + "\n")
		}
	}
	var ids []string
	for _, e := range m.runLeaves() {
		ids = append(ids, e.ID)
	}
	if failures := m.failuresIn(ids); failures != "" {
		b.WriteString("\n" + section("Failures", width, number(s.failed)) + "\n" + failures)
	}
	if m.running() {
		b.WriteString("\n" + m.activitySection(width))
		b.WriteString("\n" + m.classTable(width, false))
	} else {
		if since := m.sinceLastRun(width); since != "" {
			b.WriteString("\n" + since)
		}
		b.WriteString("\n" + m.classTable(width, false))
		b.WriteString("\n" + m.activitySection(width))
		if slow := m.slowest(ids, 5, width); slow != "" {
			b.WriteString("\n" + section("Slowest", width, "") + "\n" + slow)
		}
	}
	rows := max(minLiveRows, height-len(lines(b.String()))-2)
	b.WriteString("\n" + section("Live", width, "the newest from the build and the services") + "\n")
	return b.String() + m.liveFeed(rows, width)
}

const minLiveRows = 8 // the live feed's rows on the overview, when the rest leaves fewer

// commandLine is the run's badge and command on one line, its --tests filters counted rather than listed, or
// with c, the whole command.
func (m *Model) commandLine(width int) string {
	badge := m.stateBadge() + "  "
	command := m.displayCommand()
	words := strings.Fields(command)
	var kept []string
	filters := 0
	for i := 0; i < len(words); i++ {
		if words[i] == "--tests" && i+1 < len(words) {
			filters++
			i++
			continue
		}
		kept = append(kept, words[i])
	}
	if filters == 0 && ansi.StringWidth(badge+command) <= width {
		return badge + style.Dim.Render(command)
	}
	if m.fullCommand {
		return badge + style.Dim.Render(command) + "  " + keyCap("c") + style.Dim.Render(" less")
	}
	short := style.Dim.Render(strings.Join(kept, " "))
	if filters > 0 {
		short += "  " + style.Pill(count(filters, "--tests filter"), style.MutedText, style.Raised)
	}
	more := "  " + keyCap("c") + style.Dim.Render(" whole command")
	return fit(badge+short, max(0, width-ansi.StringWidth(more)), true) + more
}

// runSpan is the run's time so far: when it started, and how long it has taken.
func (m *Model) runSpan() (time.Time, time.Duration) {
	end := m.runFinished
	if end.IsZero() {
		end = time.Now()
	}
	return m.runStarted, max(time.Second, end.Sub(m.runStarted))
}

// column is where a moment of the run falls on a line width cells long.
func column(at, start time.Time, span time.Duration, width int) int {
	return max(0, min(width-1, int(float64(at.Sub(start))/float64(span)*float64(width))))
}

// classRow is how one class of the run is doing, its nested classes included.
type classRow struct {
	entry                                   *catalog.Entry
	leaves                                  []string
	total, passed, failed, skipped, running int
	took                                    int64
}

func (r classRow) done() int {
	return r.passed + r.failed + r.skipped
}

// runClasses are the run's top-level classes in the order the run takes them.
func (m *Model) runClasses() []classRow {
	var rows []classRow
	index := map[string]int{}
	for _, leaf := range m.runLeaves() {
		top := m.entries[leaf.ID]
		for top != nil && m.entries[top.Parent] != nil {
			top = m.entries[top.Parent]
		}
		if top == nil {
			continue
		}
		i, ok := index[top.ID]
		if !ok {
			i = len(rows)
			index[top.ID] = i
			rows = append(rows, classRow{entry: top})
		}
		row := &rows[i]
		row.total++
		row.leaves = append(row.leaves, leaf.ID)
		state := m.state(leaf.ID)
		switch state.Status {
		case "passed":
			row.passed++
		case "failed", "aborted":
			row.failed++
		case "skipped":
			row.skipped++
		case "running":
			row.running++
		}
		if took, ok := state.Took(nowMillis()); ok {
			row.took += took
		}
	}
	return rows
}

// classTable is every class in the run on the run's time line: when it was setting up (a thin line), when each
// of its tests ran, coloured by how it did, and how far it got; with tests, each test under its class as well.
func (m *Model) classTable(width int, tests bool) string {
	rows := m.runClasses()
	if len(rows) == 0 {
		return ""
	}
	finished := 0
	for _, r := range rows {
		if r.done() == r.total {
			finished++
		}
	}
	const countsWidth = 16
	nameWidth := min(48, max(16, width/3))
	barWidth := max(10, width-nameWidth-countsWidth-17)
	start, span := m.runSpan()
	var b strings.Builder
	b.WriteString(section("Classes", width, number(finished)+" of "+number(len(rows))+" done") + "\n")
	b.WriteString(strings.Repeat(" ", nameWidth+3) + axis(barWidth, span) + "\n")
	nowMillis := time.Now().UnixMilli()
	for _, r := range rows {
		status := "pending"
		switch {
		case r.failed > 0:
			status = "failed"
		case r.running > 0:
			status = "running"
		case r.done() == r.total:
			status = "passed"
		}
		mark, markStyle := icon(status)
		cells := make([]style.Cell, barWidth)
		draw := func(cells []style.Cell, from, to int64, cell style.Cell) {
			if from == 0 {
				return
			}
			if to == 0 {
				to = nowMillis
			}
			for c := column(time.UnixMilli(from), start, span, barWidth); c <= column(time.UnixMilli(to), start, span, barWidth); c++ {
				cells[c] = cell
			}
		}
		class := m.state(r.entry.ID)
		draw(cells, class.StartedAt, class.FinishedAt, style.Cell{Char: "─", Color: style.SubtleText})
		for _, leaf := range r.leaves {
			state := m.state(leaf)
			draw(cells, state.StartedAt, state.FinishedAt, style.Cell{Char: "━", Color: statusColor(state.Status)})
		}
		var counts []string
		if r.passed > 0 {
			counts = append(counts, style.Green.Render(ic.passed+" "+compact(r.passed)))
		}
		if r.failed > 0 {
			counts = append(counts, style.Red.Render(ic.failed+" "+compact(r.failed)))
		}
		if r.skipped > 0 {
			counts = append(counts, style.Yellow.Render(ic.skipped+" "+compact(r.skipped)))
		}
		took := ""
		if r.took > 0 {
			took = duration(r.took)
		}
		name := style.Muted.Render(r.entry.ShortClass())
		if status != "pending" {
			name = style.Bold.Render(r.entry.ShortClass())
		}
		b.WriteString(markStyle.Render(mark) + " " + fit(name, nameWidth, true) + " " + style.Cells(cells) + " " +
			fit(style.Dim.Render(compact(r.done())+"/"+compact(r.total)), 7, false) +
			fit(strings.Join(counts, " "), countsWidth-6, false) + style.Dim.Render(fmt.Sprintf("%6s", took)) + "\n")
		if !tests {
			continue
		}
		for _, leaf := range r.leaves {
			state, entry := m.state(leaf), m.entries[leaf]
			if entry == nil {
				continue
			}
			cells := make([]style.Cell, barWidth)
			draw(cells, state.StartedAt, state.FinishedAt, style.Cell{Char: "━", Color: statusColor(state.Status)})
			mark, markStyle := icon(state.Status)
			names := append(m.groupOf(entry), cleanText(entry.Name))
			took := ""
			if t, ok := state.Took(nowMillis); ok {
				took = duration(t)
			}
			b.WriteString(markStyle.Render(mark) + " " + fit(style.Muted.Render("  "+strings.Join(names[1:], " › ")), nameWidth, true) +
				" " + style.Cells(cells) + " " + strings.Repeat(" ", countsWidth+1) + style.Dim.Render(fmt.Sprintf("%6s", took)) + "\n")
		}
	}
	return b.String()
}

func statusColor(status string) color.Color {
	switch status {
	case "passed":
		return style.Pass
	case "failed", "aborted":
		return style.Fail
	case "skipped":
		return style.Warn
	}
	return style.Run
}

// axis marks a time line width cells long: the start, the middle and the end.
func axis(width int, span time.Duration) string {
	return axisBetween(width, 0, span)
}

// axisBetween marks a time line of a stretch of the run width cells long, by the run's time at its start, middle
// and end.
func axisBetween(width int, from, to time.Duration) string {
	marks := []struct {
		at    int
		label string
	}{{0, duration(from.Milliseconds())}, {width / 2, duration((from + (to-from)/2).Milliseconds())},
		{width, duration(to.Milliseconds())}}
	line := []rune(strings.Repeat(" ", width))
	for _, mark := range marks {
		label := []rune(mark.label)
		at := max(0, min(width-len(label), mark.at-len(label)/2))
		if mark.at == width {
			at = width - len(label)
		}
		copy(line[at:], label)
	}
	return style.Dim.Render(string(line))
}

// activity is how busy one source was over the run, a count for each slice of its time.
type activityRow struct {
	icon, label string
	counts      []int
	colorOf     func(i int) color.Color
	total       string
}

// activitySection is a sparkline for each source over the run so far: tests finishing, Gradle's output, each
// service's log (yellow or red where it warned or failed), requests, messages and queries.
func (m *Model) activitySection(width int) string {
	grid := m.activityGrid(activityBuckets(width), time.Now())
	key := fmt.Sprintf("%d/%d/%s/%s", m.version, width, grid.key(), ic.output)
	if m.activityKey != key {
		m.activityKey, m.activityText = key, m.drawActivity(width, grid)
	}
	return m.activityText
}

const activityLabelWidth, activityTotalWidth = 18, 18

func (m *Model) drawActivity(width int, grid activityGrid) string {
	var b strings.Builder
	b.WriteString(section("Activity", width, grid.about()) + "\n")
	for _, row := range m.activityRows(grid) {
		b.WriteString(row.heading() + " " + style.Sparkline(row.counts, row.colorOf) + " " + style.Dim.Render(row.total) + "\n")
	}
	return b.String()
}

func activityBuckets(width int) int {
	return max(10, width-activityLabelWidth-activityTotalWidth-2)
}

func (row activityRow) heading() string {
	return fit(style.Dim.Render(row.icon+" ")+cleanText(row.label), activityLabelWidth, true)
}

// activityGrid is the stretch of the run the activity shows, in columns of one step each. While the run goes it
// is the last stretch, as long as the activity window setting, and moves with the run a column at a time; the
// columns keep to steps from the run's start, so each counts the same slice of the run for as long as it shows.
// Once the run is over, it is the whole run.
type activityGrid struct {
	start    time.Time // the run's
	from, to time.Time // the first column's start, and the last moment counted
	step     time.Duration
	columns  int
	window   time.Duration // while the run goes; zero once it is over
}

func (m *Model) activityGrid(columns int, now time.Time) activityGrid {
	start := m.runStarted
	if !m.running() {
		_, span := m.runSpan()
		return activityGrid{start: start, from: start, to: start.Add(span), columns: columns,
			step: max(time.Millisecond, span/time.Duration(columns))}
	}
	window := m.prefs.Window()
	step := max(time.Millisecond, window/time.Duration(columns))
	first := max(0, int(now.Sub(start)/step)-columns+1)
	return activityGrid{start: start, from: start.Add(time.Duration(first) * step), to: now, step: step,
		columns: columns, window: window}
}

// column is the column a moment falls in, or -1 outside the grid.
func (g activityGrid) column(at time.Time) int {
	if at.Before(g.from) || at.After(g.to) {
		return -1
	}
	return min(g.columns-1, int(at.Sub(g.from)/g.step))
}

// key changes when the grid moves on a column.
func (g activityGrid) key() string {
	return fmt.Sprintf("%d/%d/%d", g.from.UnixNano(), g.step, int(g.to.Sub(g.from)/g.step))
}

// about says what stretch of the run the grid shows.
func (g activityGrid) about() string {
	if g.window > 0 {
		return "the last " + duration(g.window.Milliseconds()) + " · + − to change"
	}
	return "the whole run, " + duration(g.to.Sub(g.start).Milliseconds())
}

func (g activityGrid) axis() string {
	from := g.from.Sub(g.start)
	return axisBetween(g.columns, from, from+g.step*time.Duration(g.columns))
}

// activityRows count what each source did in each column of a grid, leaving out what came before it or after
// the run ended (or now, while it goes).
func (m *Model) activityRows(grid activityGrid) []activityRow {
	buckets, bucket := grid.columns, grid.column
	var rows []activityRow

	tests, failedAt := make([]int, buckets), make([]bool, buckets)
	done := 0
	for _, leaf := range m.runLeaves() {
		state := m.state(leaf.ID)
		if state.FinishedAt == 0 {
			continue
		}
		done++
		if i := bucket(time.UnixMilli(state.FinishedAt)); i >= 0 {
			tests[i]++
			failedAt[i] = failedAt[i] || state.Status == "failed" || state.Status == "aborted"
		}
	}
	rows = append(rows, activityRow{ic.check, "Tests", tests, func(i int) color.Color {
		if failedAt[i] {
			return style.Fail
		}
		return style.Pass
	}, number(done) + " done"})

	output := m.output.all()
	gradle := make([]int, buckets)
	for _, line := range output {
		if i := bucket(line.At); i >= 0 {
			gradle[i]++
		}
	}
	rows = append(rows, activityRow{ic.output, "Gradle", gradle, func(int) color.Color { return style.Primary },
		count(len(output), "line")})

	for _, entry := range m.orderedServices() {
		counts := make([]int, buckets)
		switch v := entry.view.(type) {
		case *wiremockView:
			unmatched := make([]bool, buckets)
			missed := 0
			for _, e := range v.events {
				if v.isNoise(e) {
					continue
				}
				if i := bucket(e.At); i >= 0 {
					counts[i]++
					unmatched[i] = unmatched[i] || !e.Matched
				}
				if !e.Matched {
					missed++
				}
			}
			total := count(len(v.events)-v.noisy(), "request")
			if missed > 0 {
				total += style.Red.Render(" " + ic.failed + " " + compact(missed))
			}
			rows = append(rows, activityRow{kindIcon(entry.svc.Kind), entry.svc.Key, counts, func(i int) color.Color {
				if unmatched[i] {
					return style.Fail
				}
				return style.Teal
			}, total})
		case *kafkaView:
			for _, message := range v.messages {
				if i := bucket(message.At); i >= 0 && !v.isNoise(message) {
					counts[i]++
				}
			}
			rows = append(rows, activityRow{kindIcon(entry.svc.Kind), entry.svc.Key, counts,
				func(int) color.Color { return style.Pink }, count(len(v.messages), "message")})
		case *dbView:
			for _, q := range v.queries {
				if i := bucket(q.At); i >= 0 && !q.Noise() {
					counts[i]++
				}
			}
			one, many := v.noun()
			rows = append(rows, activityRow{kindIcon(entry.svc.Kind), entry.svc.Key, counts,
				func(int) color.Color { return style.Orange }, count(len(v.queries)-v.noisy(), one, many)})
		default:
			severity := make([]int, buckets)
			lines := entry.lines.all()
			log, _ := entry.view.(*logView)
			for _, line := range lines {
				i := bucket(line.At)
				if i < 0 || log != nil && log.isNoise(line) {
					continue
				}
				counts[i]++
				if record := line.Record(); record != nil {
					switch record.Level {
					case "ERROR", "FATAL":
						severity[i] = 2
					case "WARN":
						severity[i] = max(severity[i], 1)
					}
				}
			}
			total := count(len(lines), "line")
			if entry.problems > 0 {
				total += style.Yellow.Render(" " + ic.warning + " " + compact(entry.problems))
			}
			base := style.MutedText
			if entry.svc.IsApp() {
				base = style.Primary
			}
			rows = append(rows, activityRow{kindIcon(entry.svc.Kind), entry.svc.Key, counts, func(i int) color.Color {
				switch severity[i] {
				case 2:
					return style.Fail
				case 1:
					return style.Warn
				}
				return base
			}, total})
		}
	}
	return rows
}

// liveFeed is the newest of everything the run shows, one line each.
func (m *Model) liveFeed(n, width int) string {
	var b strings.Builder
	for _, ev := range m.feed(n) {
		b.WriteString(fit(style.Dim.Render(clock(ev.at))+" "+feedSource(ev)+" "+ev.summary, width, true) + "\n")
	}
	if b.Len() == 0 {
		b.WriteString(style.Dim.Render("Nothing yet.") + "\n")
	}
	return b.String()
}

func feedSource(ev timelineEvent) string {
	return style.Pill(fit(cleanText(ev.source), 10, true), ev.color, style.Tint(ev.color))
}

// feedWorthy is whether a log line belongs in the live feed: an application's records and lines that are not
// part of a stack trace, and anything's warnings and errors.
func feedWorthy(line *logformat.Line, app bool) bool {
	if line.IsLoggedProblem() {
		return true
	}
	if !app || strings.TrimSpace(line.Text) == "" {
		return false
	}
	return line.Record() != nil || (line.Text[0] != ' ' && line.Text[0] != '\t')
}
