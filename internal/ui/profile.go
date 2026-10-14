package ui

import (
	"context"
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/profile"
	"github.com/steven509/testdash/internal/style"
)

const maxSamples = 7200 // of each kind, an application's last two hours at a sample a second

// appProfile is what profiling has sampled of an application: its container's usage, and its JVM's counters.
type appProfile struct {
	usage  []profile.Usage
	jvm    []profile.JVM
	cancel context.CancelFunc // stops the sampling
}

type profileMsg struct {
	entry *svcEntry
	usage *profile.Usage
	jvm   *profile.JVM
}

// followProfile samples an application while profiling is on, each container a run starts for it in turn.
func (m *Model) followProfile(entry *svcEntry) {
	if !m.prefs.Profile || !entry.svc.IsApp() || m.send == nil {
		return
	}
	if entry.profile == nil {
		entry.profile = &appProfile{}
	}
	if entry.profile.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	entry.profile.cancel = cancel
	send, svc := m.send, entry.svc
	go func() {
		for ctx.Err() == nil {
			if id := svc.ContainerID(); id != "" && svc.State() == "running" {
				profile.Follow(ctx, id, func() bool { return svc.ContainerID() == id && svc.State() == "running" },
					func(u profile.Usage) { send(profileMsg{entry: entry, usage: &u}) },
					func(j profile.JVM) { send(profileMsg{entry: entry, jvm: &j}) })
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
}

// setProfiling starts or stops sampling the applications, and keeps the choice in the settings.
func (m *Model) setProfiling(on bool) tea.Cmd {
	prefs := m.prefs
	prefs.Profile = on
	if err := prefs.Save(); err != nil {
		m.notify("Could not keep the setting: "+err.Error(), "", "warning")
	}
	m.applySettings(prefs)
	return nil
}

// profilingChanged starts or stops the sampling as the setting says.
func (m *Model) profilingChanged() {
	for _, entry := range m.svcs {
		if m.prefs.Profile {
			m.followProfile(entry)
		} else if p := entry.profile; p != nil && p.cancel != nil {
			p.cancel()
			p.cancel = nil
		}
	}
}

func (m *Model) profiled(msg profileMsg) {
	p := msg.entry.profile
	if p == nil || p.cancel == nil {
		return // sampled before profiling stopped
	}
	if msg.usage != nil {
		p.usage = append(p.usage, *msg.usage)
		if len(p.usage) > maxSamples+maxSamples/4 {
			p.usage = append([]profile.Usage(nil), p.usage[len(p.usage)-maxSamples:]...)
		}
	}
	if msg.jvm != nil {
		p.jvm = append(p.jvm, *msg.jvm)
		if len(p.jvm) > maxSamples+maxSamples/4 {
			p.jvm = append([]profile.JVM(nil), p.jvm[len(p.jvm)-maxSamples:]...)
		}
	}
	m.version++
}

// threadDump asks each running application's JVM for a thread dump, which it writes to its log.
func (m *Model) threadDump() tea.Cmd {
	var ids, names []string
	for _, entry := range m.orderedServices() {
		if entry.svc.IsApp() && entry.svc.State() == "running" && entry.svc.ContainerID() != "" {
			ids, names = append(ids, entry.svc.ContainerID()), append(names, entry.svc.Key)
		}
	}
	if len(ids) == 0 {
		m.notify("No application is running to dump the threads of", "", "information")
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		for _, id := range ids {
			if err := profile.ThreadDump(ctx, id); err != nil {
				return notifyMsg{text: "Could not ask for a thread dump: " + err.Error(), severity: "warning"}
			}
		}
		return notifyMsg{text: "The thread dump is in " + strings.Join(names, " and ") + "'s log", severity: "information"}
	}
}

// profiledApps are the applications profiling has samples of.
func (m *Model) profiledApps() []*svcEntry {
	var apps []*svcEntry
	for _, entry := range m.orderedServices() {
		if p := entry.profile; p != nil && (len(p.usage) > 0 || len(p.jvm) > 0) {
			apps = append(apps, entry)
		}
	}
	return apps
}

// profileChart is the Profile tab: each application's CPU and memory, and for a JVM, its heap, garbage
// collection and threads, on the run's time line as the activity has it.
func (m *Model) profileChart(width, height int) string {
	var b strings.Builder
	if !m.prefs.Profile {
		b.WriteString(section("Profile", width, "off") + "\n")
		b.WriteString(strings.Join(wrapLines(style.Muted.Render("Profiling samples the application while tests run: "+
			"its container's CPU and memory from docker stats, and for a JVM, its heap, garbage collection and "+
			"threads from the counters every JVM keeps, with nothing to set up. A test's details then say what it "+
			"used while it ran."), width), "\n") + "\n\n")
		b.WriteString(keyCap("p") + style.Muted.Render(" starts it, and keeps it on; it costs a docker exec every two seconds.") + "\n")
		return b.String()
	}
	grid := m.activityGrid(activityChartColumns(width), time.Now())
	b.WriteString(section("Profile", width, grid.about()) + "\n")
	apps := m.profiledApps()
	if len(apps) == 0 {
		b.WriteString(style.Muted.Render("Waiting for the application: it is sampled once a run starts its container.") + "\n")
		return b.String()
	}
	var all [][]chartRow
	rows := 0
	for _, entry := range apps {
		all = append(all, profileRows(entry.profile, grid))
		rows += len(all[len(all)-1])
	}
	each := max(1, (height-4-len(apps))/max(1, rows)) // the lines each chart may have, under the headings, over the axis and keys
	for i, entry := range apps {
		b.WriteString(style.Bold.Render(ic.app+" "+entry.svc.Key) + "\n")
		chartRows(&b, all[i], each*len(all[i]), grid)
	}
	b.WriteString(strings.Repeat(" ", activityLabelWidth+1) + grid.axis() + "\n")
	b.WriteString(style.Dim.Render("p stops profiling · d asks the JVM for a thread dump, into its log") + "\n")
	return b.String()
}

// profileRows chart an application's samples on a grid: the highest of each measure in each column, and the time
// collecting garbage in it.
func profileRows(p *appProfile, grid activityGrid) []chartRow {
	fixed := func(c color.Color) func(int) color.Color { return func(int) color.Color { return c } }
	mib := func(n int) string { return sizeLabel(int64(n) << 20) }
	cpu, memory, sampled := make([]int, grid.columns), make([]int, grid.columns), make([]bool, grid.columns)
	for _, u := range p.usage {
		if i := grid.column(u.At); i >= 0 {
			cpu[i], memory[i] = max(cpu[i], int(math.Round(u.CPU))), max(memory[i], int(u.Memory>>20))
			sampled[i] = true
		}
	}
	if len(p.usage) > 0 {
		holdBetween(sampled, grid.holdsOn(p.usage[len(p.usage)-1].At), cpu, memory)
	}
	var cpuNow, memoryNow string
	if len(p.usage) > 0 {
		last := p.usage[len(p.usage)-1]
		cpuNow = fmt.Sprintf("%.0f%% now", last.CPU)
		memoryNow = sizeLabel(last.Memory) + " of " + sizeLabel(last.MemoryLimit)
	}
	rows := []chartRow{
		{activityRow{label: "CPU", counts: cpu, colorOf: fixed(style.Run), total: cpuNow}, func(n int) string { return fmt.Sprintf("%d%%", n) }},
		{activityRow{label: "Memory", counts: memory, colorOf: fixed(style.Pink), total: memoryNow}, mib},
	}
	if len(p.jvm) == 0 {
		return rows
	}
	heap, gc, threads := make([]int, grid.columns), make([]int, grid.columns), make([]int, grid.columns)
	sampled = make([]bool, grid.columns)
	for i, j := range p.jvm {
		c := grid.column(j.At)
		if c < 0 {
			continue
		}
		heap[c], threads[c] = max(heap[c], int(j.HeapUsed>>20)), max(threads[c], int(j.Threads))
		sampled[c] = true
		if i > 0 {
			gc[c] += max(0, int((j.GCTime() - p.jvm[i-1].GCTime()).Milliseconds())) // a new JVM starts again
		}
	}
	holdBetween(sampled, grid.holdsOn(p.jvm[len(p.jvm)-1].At), heap, threads)
	last := p.jvm[len(p.jvm)-1]
	return append(rows,
		chartRow{activityRow{label: "Heap", counts: heap, colorOf: fixed(style.Teal),
			total: sizeLabel(last.HeapUsed) + " of " + sizeLabel(last.HeapCommitted) + ", max " + sizeLabel(last.HeapMax)}, mib},
		chartRow{activityRow{label: "GC", counts: gc, colorOf: fixed(style.Orange),
			total: count(int(last.GCs()), "collection") + ", " + last.GCTime().Round(time.Millisecond).String()},
			func(n int) string { return fmt.Sprintf("%d ms", n) }},
		chartRow{activityRow{label: "Threads", counts: threads, colorOf: fixed(style.Secondary),
			total: fmt.Sprintf("%d live", last.Threads)}, func(n int) string { return fmt.Sprint(n) }})
}

// holdBetween carries each sample over the columns up to the next, as a measure such as the heap's goes on
// between samples, which come less often than columns: from the first sampled column to the last, or to the end
// when the measure goes on past it.
func holdBetween(sampled []bool, toEnd bool, series ...[]int) {
	last := -1
	for i := range sampled {
		if sampled[i] || toEnd {
			last = i
		}
	}
	held := -1
	for i := 0; i <= last; i++ {
		switch {
		case sampled[i]:
			held = i
		case held >= 0:
			for _, s := range series {
				s[i] = s[held]
			}
		}
	}
}

// holdsOn is whether a measure whose newest sample is at a moment goes on to the grid's end: it was sampled after
// it, or so shortly before that the next sample is still to come.
func (g activityGrid) holdsOn(newest time.Time) bool {
	return !newest.Before(g.to.Add(-3 * time.Second))
}

// sizeLabel is a size in MiB, or GiB from one.
func sizeLabel(bytes int64) string {
	if bytes >= 1<<30 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(bytes)/(1<<30)), ".0") + " GiB"
	}
	return fmt.Sprintf("%d MiB", bytes>>20)
}

// testResources says what the applications used while a test ran: their CPU, and for a JVM, how its heap changed
// and what it collected, from the samples on either side of the test, as a short test may have none within.
func (m *Model) testResources(id string, width int) string {
	w := m.windowFor(id)
	if w == nil {
		return ""
	}
	var lines []string
	for _, entry := range m.profiledApps() {
		p := entry.profile
		var parts []string
		var sum, peak float64
		n := 0
		for _, u := range p.usage {
			if w.holds(u.At) {
				sum, peak, n = sum+u.CPU, max(peak, u.CPU), n+1
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("CPU %.0f%% on average, %.0f%% at most", sum/float64(n), peak))
		}
		if before, after, ok := bracket(p.jvm, w); ok {
			parts = append(parts, "heap "+sizeLabel(before.HeapUsed)+" → "+sizeLabel(after.HeapUsed))
			if gcs := after.GCs() - before.GCs(); gcs > 0 {
				parts = append(parts, count(int(gcs), "collection")+", "+
					(after.GCTime()-before.GCTime()).Round(time.Millisecond).String())
			}
		}
		if len(parts) > 0 {
			lines = append(lines, style.Bold.Render(entry.svc.Key)+style.Muted.Render("  "+strings.Join(parts, " · ")))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return section("Resources", width, "while it ran") + "\n" + strings.Join(lines, "\n")
}

// bracket finds the JVM's samples around a window: the last at or before its start, else the first in it, and the
// first at or after its end, else the last in it.
func bracket(samples []profile.JVM, w *window) (profile.JVM, profile.JVM, bool) {
	start, end := -1, -1
	for i, s := range samples {
		if !s.At.After(w.start) || start < 0 && w.holds(s.At) {
			start = i
		}
		if end < 0 && !s.At.Before(w.end) {
			end = i
		}
	}
	for i := len(samples) - 1; end < 0 && i >= 0; i-- {
		if w.holds(samples[i].At) {
			end = i
		}
	}
	if start < 0 || end < start {
		return profile.JVM{}, profile.JVM{}, false
	}
	return samples[start], samples[end], true
}
