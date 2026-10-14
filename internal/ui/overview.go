package ui

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/noise"
	"github.com/steven509/testdash/internal/style"
	"github.com/steven509/testdash/internal/trace"
)

const (
	maxFeed        = 2000                   // the most events the Live tab holds
	liveBuildEvery = 250 * time.Millisecond // while a run goes, how often the Live tab is built again at most
)

func newOverviewTabs() tabbedView {
	return tabbedView{tabIDs: []string{"overview", "live", "activity", "classes", "profile"},
		tabLabels: []string{"Overview", "Live", "Activity", "Classes", "Profile"}}
}

// overviewHeadings are the overview's section headings that open a tab of their own.
var overviewHeadings = map[string]string{"LIVE ": "live", "ACTIVITY ": "activity", "CLASSES ": "classes"}

// renderOverview is the run in tabs: the overview of it all, then the live feed, the activity and the classes
// each at full size.
func (in *inspector) renderOverview(m *Model, r rect) []string {
	meta := ""
	if in.overview.activeID() == "live" {
		in.live.build(m)
		meta = count(len(in.live.events), "event")
		if in.live.noise > 0 {
			meta += ", " + number(in.live.noise) + " noise"
		}
		meta = style.Muted.Render(meta) + modeChips(in.live.filterLabel(), traceChip(m.traceFilter),
			map[bool]string{true: "with noise"}[in.live.showNoise])
	}
	out, rest := in.overview.tabLine(m, r, meta, func() { m.setFocus(in.overviewMain()) })
	switch in.overview.activeID() {
	case "live":
		return append(out, in.live.render(m, rest)...)
	case "activity":
		in.card.set("activity", m.activityChart(rest.w, rest.h))
	case "classes":
		in.card.set("classes", m.classTable(rest.w, true))
	case "profile":
		in.card.set("profile", m.profileChart(rest.w, rest.h))
	default:
		in.card.set("run", m.dashboard(rest.w, rest.h))
	}
	m.addZone(zone{r: rest, click: func(int, int) tea.Cmd { m.setFocus(in.card); return nil }, wheel: in.card.scroll})
	out = append(out, in.card.view(rest.w, rest.h)...)
	if in.overview.activeID() != "overview" {
		return out
	}
	if in.card.offset == 0 { // a click on the command shows or hides all of it
		m.addZone(zone{r: rect{rest.x, rest.y, rest.w, 1}, click: func(int, int) tea.Cmd {
			m.fullCommand = !m.fullCommand
			return nil
		}})
	}
	for i, line := range in.card.lines(rest.w) {
		y := i - in.card.offset
		if y < 0 || y >= rest.h {
			continue
		}
		for heading, tab := range overviewHeadings {
			if strings.HasPrefix(ansi.Strip(line), heading) {
				m.addZone(zone{r: rect{rest.x, rest.y + y, rest.w, 1}, click: func(int, int) tea.Cmd {
					in.overview.setActive(tab)
					m.setFocus(in.overviewMain())
					return nil
				}})
			}
		}
	}
	return out
}

// showsRun is whether the details show the run's overview tabs.
func (in *inspector) showsRun(m *Model) bool {
	return m.tree.cursor == "" && !m.runStarted.IsZero()
}

func (in *inspector) overviewMain() any {
	if in.overview.activeID() == "live" {
		return in.live.table
	}
	return in.card
}

func (in *inspector) overviewLevels(m *Model, focus any) []level {
	tabs := append(tabBindings(&in.overview, func() { m.setFocus(in.overviewMain()) }),
		bind("esc", "Overview", func() tea.Cmd {
			in.overview.setActive("overview")
			m.setFocus(in.card)
			return nil
		}).when(func() bool { return in.overview.activeID() != "overview" }))
	if in.overview.activeID() == "live" {
		return in.live.levels(m, focus, tabs)
	}
	shown := func() bool { return m.running() && in.overview.activeID() != "classes" }
	if in.overview.activeID() == "profile" {
		tabs = append(tabs,
			bind("p", map[bool]string{false: "Start profiling", true: "Stop profiling"}[m.prefs.Profile],
				func() tea.Cmd { return m.setProfiling(!m.prefs.Profile) }),
			bind("d", "Thread dump", m.threadDump).when(func() bool { return m.prefs.Profile }))
	}
	tabs = append(tabs,
		bind("+ =", "Activity window", func() tea.Cmd { return m.stepActivityWindow(1) }).shown("+ −").when(shown),
		bind("-", "Shorter activity window", func() tea.Cmd { return m.stepActivityWindow(-1) }).hidden().when(shown))
	return []level{detailLevel(in.card), {bindings: tabs}}
}

// tabKeys are [ and ], to change the overview's tab from the tests.
func (in *inspector) tabKeys(m *Model) []binding {
	step := func(by int) func() tea.Cmd {
		return func() tea.Cmd { in.overview.step(by); return nil }
	}
	shown := func() bool { return in.showsRun(m) }
	return []binding{bind("[", "Previous tab", step(-1)).hidden().when(shown),
		bind("]", "Next tab", step(1)).hidden().when(shown)}
}

// liveView is everything the run has shown, newest last, to look through: the row under the cursor in full
// beside it, and enter opens it in its service. It follows the newest while its cursor is on the last row.
type liveView struct {
	table      *table
	event      *textbox
	input      *lineInput
	inputShown bool
	pattern    *regexp.Regexp
	showNoise  bool
	noise      int // noise left out of the events
	events     []timelineEvent
	builtAt    int       // the model's version it was built at
	builtWhen  time.Time // and when
	builtFor   string    // and with what filter and trace
}

func newLiveView() *liveView {
	return &liveView{table: newTable("Time", "Source", "What"), event: &textbox{}, builtAt: -1,
		input: &lineInput{placeholder: "filter (regex), Enter keeps it, Esc clears it"}}
}

func (v *liveView) build(m *Model) {
	filter := fmt.Sprintf("%s/%s/%v", v.filterLabel(), m.traceFilter, v.showNoise)
	if v.builtFor == filter && (v.builtAt == m.version || m.running() && time.Since(v.builtWhen) < liveBuildEvery) {
		return
	}
	v.builtAt, v.builtWhen, v.builtFor = m.version, time.Now(), filter
	key, following := v.table.cursorKey(), v.table.cursor >= len(v.table.rows)-1
	v.events = nil
	v.table.clear()
	events, noise := m.feed(maxFeed, v.showNoise)
	v.noise = noise
	for _, ev := range events {
		if v.pattern != nil && !v.pattern.MatchString(ev.source+" "+ansi.Strip(ev.summary)) {
			continue
		}
		v.events = append(v.events, ev)
		v.table.addRow(eventKey(ev), style.Dim.Render(clockMillis(ev.at)), feedSource(ev), ev.summary)
	}
	if following {
		v.table.moveCursor(len(v.table.rows) - 1)
	} else {
		v.table.selectKey(key)
	}
}

func eventKey(ev timelineEvent) string {
	return fmt.Sprintf("%d %s %s", ev.at.UnixNano(), ev.source, ansi.Strip(ev.summary))
}

func (v *liveView) filterLabel() string {
	if v.pattern == nil {
		return ""
	}
	return "/" + strings.TrimPrefix(v.pattern.String(), "(?i)")
}

func (v *liveView) selected() (timelineEvent, bool) {
	if i := v.table.cursor; i >= 0 && i < len(v.events) {
		return v.events[i], true
	}
	return timelineEvent{}, false
}

func (v *liveView) render(m *Model, r rect) []string {
	v.build(m)
	var out []string
	if v.inputShown {
		out = append(out, v.input.view(r.w, m.focus == v.input))
		m.addZone(zone{r: rect{r.x, r.y, r.w, 1}, click: func(int, int) tea.Cmd { m.setFocus(v.input); return nil }})
		r = rect{r.x, r.y + 1, r.w, max(0, r.h-1)}
	}
	if len(v.events) == 0 {
		empty := "Nothing yet."
		if v.pattern != nil {
			empty = "Nothing matches the filter."
		}
		return append(out, style.Dim.Render(empty))
	}
	detail := v.event
	if r.w < 90 {
		detail = nil
	} else if ev, ok := v.selected(); ok {
		v.event.set(eventKey(ev), ev.detail())
	}
	return append(out, tablePane(m, r, v.table, detail)...)
}

func (v *liveView) filterChanged() {
	if value := v.input.Value(); value == "" {
		v.pattern = nil
	} else if pattern, err := regexp.Compile("(?i)" + value); err == nil {
		v.pattern = pattern
	}
}

func (v *liveView) clearFilter() {
	v.input.SetValue("")
	v.inputShown, v.pattern = false, nil
}

func (v *liveView) levels(m *Model, focus any, tabs []binding) []level {
	clear := bind("esc", "Clear filter", func() tea.Cmd {
		v.clearFilter()
		m.setFocus(v.table)
		return nil
	}).when(func() bool { return v.inputShown || v.pattern != nil })
	switch focus {
	case v.input:
		return []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
			if k.String() == "enter" {
				if v.input.Value() == "" {
					v.inputShown = false
				}
				m.setFocus(v.table)
				return true, nil
			}
			handled, changed := v.input.key(k)
			if changed {
				v.filterChanged()
			}
			return handled, nil
		}, bindings: []binding{clear}}}
	case v.event:
		return []level{detailLevel(v.event), {bindings: tabs}}
	}
	l := tableLevel(v.table, "Open in its service", func() tea.Cmd {
		if ev, ok := v.selected(); ok {
			m.openEvent(ev, nil)
		}
		return nil
	})
	l.bindings = append(l.bindings,
		bind("/", "Filter", func() tea.Cmd {
			v.inputShown = true
			m.setFocus(v.input)
			return nil
		}),
		m.traceBinding(func() (trace.Context, bool) {
			ev, ok := v.selected()
			return ev.trace, ok && ev.trace.TraceID != ""
		}),
		noiseToggle(&v.showNoise, func() int { return v.noise }, func() {}),
		clear, m.allTracesOnEsc())
	if ev, ok := v.selected(); ok && ev.copy.text != "" {
		l.bindings = append(l.bindings, bind("c", "Copy "+ev.copy.name, func() tea.Cmd {
			return m.copyText("the "+ev.copy.name, ev.copy.text)
		}))
	}
	if ev, ok := v.selected(); ok && ev.service != nil && ev.noise != "" {
		l.bindings = append(l.bindings, bind("N", "Hide as noise", func() tea.Cmd {
			m.hideAsNoise(ev.service.svc.Key, noise.Suggest(ev.noise))
			return nil
		}))
	}
	return []level{l, {bindings: tabs}}
}

// activityChart is the activity at full size: each source's counts over the run as a bar chart, as tall as the
// height leaves room for.
func (m *Model) activityChart(width, height int) string {
	grid := m.activityGrid(activityChartColumns(width), time.Now())
	key := fmt.Sprintf("%d/%d/%d/%s/%s", m.version, width, height, grid.key(), ic.output)
	if m.activityChartKey == key {
		return m.activityChartText
	}
	var b strings.Builder
	b.WriteString(section("Activity", width, grid.about()) + "\n")
	var rows []chartRow
	for _, row := range m.activityRows(grid) {
		rows = append(rows, chartRow{activityRow: row})
	}
	chartRows(&b, rows, height-2, grid)
	b.WriteString(strings.Repeat(" ", activityLabelWidth+1) + grid.axis() + "\n")
	m.activityChartKey, m.activityChartText = key, b.String()
	return m.activityChartText
}

// chartRow is a row of a chart at full size: the counts of a source, or for a measure, as of memory, the highest
// in each column, with peak to say what the highest stands for.
type chartRow struct {
	activityRow
	peak func(n int) string
}

// chartRows draws a bar chart for each row, as tall as the height leaves room for, each labelled with what its
// top stands for: a rate, unless the row says otherwise.
func chartRows(b *strings.Builder, rows []chartRow, height int, grid activityGrid) {
	each, gap := height/max(1, len(rows)), false
	tall := max(1, each)
	if each >= 3 {
		tall, gap = min(8, each-1), true
	}
	for _, row := range rows {
		var top []string // what the top of the chart stands for, beside it
		if peak := slices.Max(row.counts); peak > 0 {
			label := rate(peak, grid.step)
			if row.peak != nil {
				label = row.peak(peak)
			}
			top = append(top, "peak "+label)
		}
		for i, line := range style.Chart(row.counts, tall, row.colorOf) {
			heading, side := strings.Repeat(" ", activityLabelWidth), []string(nil)
			if i == 0 {
				side = top
			}
			if i == tall-1 {
				heading, side = row.heading(), append([]string{row.total}, side...)
			}
			b.WriteString(heading + " " + line + " " + style.Dim.Render(fit(strings.Join(side, " · "), activitySideWidth, true)) + "\n")
		}
		if gap {
			b.WriteString("\n")
		}
	}
}

const activitySideWidth = 30 // beside the activity at full size: the totals, and the peak where a chart is one line

func activityChartColumns(width int) int {
	return max(10, width-activityLabelWidth-activitySideWidth-2)
}

// rate is how many in a while as a rate: a second, or below one a second, a minute.
func rate(n int, while time.Duration) string {
	perSecond := float64(n) / while.Seconds()
	switch {
	case perSecond >= 10:
		return compact(int(math.Round(perSecond))) + "/s"
	case perSecond >= 1:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", perSecond), ".0") + "/s"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", perSecond*60), ".0") + "/min"
}

// activityWindows are the lengths the activity can show of a run that goes on.
var activityWindows = []int{10, 30, 60, 120, 300}

// stepActivityWindow makes the activity show a longer or shorter stretch of the run, and keeps it in the settings.
func (m *Model) stepActivityWindow(by int) tea.Cmd {
	i := slices.Index(activityWindows, int(m.prefs.Window()/time.Second))
	if i < 0 {
		i = 1
	}
	prefs := m.prefs
	prefs.ActivityWindow = activityWindows[max(0, min(len(activityWindows)-1, i+by))]
	m.prefs = prefs
	m.version++
	if err := prefs.Save(); err != nil {
		m.notify("Could not keep the activity's window: "+err.Error(), "", "warning")
	}
	return nil
}
