package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/style"
)

// serviceList is Gradle's output and the services the run started: the applications, then everything else.
type serviceList struct {
	cursor int
	offset int
	height int
}

// rows are the list's entries; nil is Gradle's output.
func (l *serviceList) rows(m *Model) []*svcEntry {
	return append([]*svcEntry{nil}, m.orderedServices()...)
}

func (l *serviceList) selected(m *Model) *svcEntry {
	rows := l.rows(m)
	l.cursor = max(0, min(l.cursor, len(rows)-1))
	return rows[l.cursor]
}

func (l *serviceList) selectedView(m *Model) view {
	if entry := l.selected(m); entry != nil {
		return entry.view
	}
	return m.outputView
}

// selectEntry moves the cursor to a service.
func (l *serviceList) selectEntry(m *Model, entry *svcEntry) {
	for i, row := range l.rows(m) {
		if row == entry {
			l.cursor = i
		}
	}
}

// choose moves the cursor to a row; a service newly chosen opens at its newest rows.
func (l *serviceList) choose(m *Model, i int) {
	i = max(0, min(i, len(l.rows(m))-1))
	if i == l.cursor {
		return
	}
	l.cursor = i
	if v, ok := l.selectedView(m).(interface{ latest() }); ok {
		v.latest()
	}
}

// preferredHeight is the list's height: its rows, up to two fifths of the body.
func (l *serviceList) preferredHeight(m *Model, body int) int {
	wanted := len(l.rows(m)) + 2
	if len(m.orderedServices()) == 0 {
		wanted += 2 // room to say containers will show up
	}
	return max(5, min(wanted, body*2/5))
}

func (l *serviceList) title(m *Model) (string, string) {
	entry := l.selected(m)
	if entry == nil {
		return ic.output + " Gradle output", gradleBadge(m)
	}
	return kindIcon(entry.svc.Kind) + " " + cleanText(entry.svc.Key), stateBadge(entry.svc.State())
}

func gradleBadge(m *Model) string {
	switch {
	case m.runner.Running():
		return style.Pill(m.spinnerFrame()+" running", style.Run, style.Tint(style.Run))
	case m.discovering:
		return style.Pill(m.spinnerFrame()+" listing tests", style.Run, style.Tint(style.Run))
	case m.runFinished.IsZero():
		return ""
	case m.exitCode == 0:
		return style.Pill(ic.passed+" exit 0", style.Pass, style.Tint(style.Pass))
	}
	return style.Pill(fmt.Sprintf("%s exit %d", ic.failed, m.exitCode), style.Fail, style.Tint(style.Fail))
}

func stateBadge(state string) string {
	switch state {
	case "running":
		return style.Pill("● running", style.Pass, style.Tint(style.Pass))
	case "stopped":
		return style.Pill("■ stopped", style.MutedText, style.Raised)
	}
	return style.Pill("from the last run", style.SubtleText, style.Raised)
}

func (l *serviceList) render(m *Model, r rect) []string {
	focused := m.panelOf(m.focus) == panelServices
	inner := rect{r.x + 2, r.y + 1, r.w - 4, r.h - 2}
	rows := l.rows(m)
	l.height = inner.h
	l.cursor = max(0, min(l.cursor, len(rows)-1))
	if l.cursor < l.offset {
		l.offset = l.cursor
	} else if l.cursor >= l.offset+inner.h {
		l.offset = l.cursor - inner.h + 1
	}
	l.offset = max(0, min(l.offset, len(rows)-inner.h))
	var content []string
	for i := l.offset; i < len(rows) && len(content) < inner.h; i++ {
		line := l.row(m, rows[i], inner.w)
		if i == l.cursor {
			bg := style.Bg(style.CursorBlur)
			if focused {
				bg = style.BoldBg(style.Text, style.Cursor)
			}
			if m.detailFrom == panelServices || focused {
				line = style.WithBg(fit(line, inner.w, false), bg)
			}
		}
		content = append(content, line)
	}
	if len(m.orderedServices()) == 0 && len(content) < inner.h {
		content = append(content, "", style.Dim.Render("Containers the run starts show here"))
	}
	m.addZone(zone{r: rect{inner.x, inner.y, inner.w, inner.h}, click: func(_, y int) tea.Cmd {
		m.setFocus(l)
		if i := l.offset + y; i < len(rows) {
			l.choose(m, i)
		}
		return nil
	}, wheel: func(delta int) {
		l.offset = max(0, min(l.offset+delta, len(l.rows(m))-l.height))
	}})
	count := ""
	if n := len(m.orderedServices()); n > 0 {
		count = style.Dim.Render(fmt.Sprint(n))
	}
	return frame(content, r, numbered(2, "Services"), count, "", focused)
}

func (l *serviceList) row(m *Model, entry *svcEntry, width int) string {
	var iconText, name, stats string
	if entry == nil {
		iconText, name = style.AccentText.Render(ic.output), "Gradle"
		switch {
		case m.runner.Running():
			stats = style.Cyan.Render(m.spinnerFrame() + " running")
		case m.discovering:
			stats = style.Cyan.Render(m.spinnerFrame() + " listing")
		case !m.runFinished.IsZero() && m.exitCode == 0:
			stats = style.Green.Render(ic.passed + " exit 0")
		case !m.runFinished.IsZero():
			stats = style.Red.Render(fmt.Sprintf("%s exit %d", ic.failed, m.exitCode))
		default:
			stats = style.Dim.Render("output")
		}
	} else {
		service := entry.svc
		color := style.Muted
		if service.IsApp() {
			color = style.Yellow
		}
		iconText = color.Render(kindIcon(service.Kind))
		name = cleanText(service.Key)
		if service.State() == "previous" {
			name = style.Dim.Render(name)
		}
		stats = serviceStats(entry)
		dot := style.Dim.Render(" ■")
		switch service.State() {
		case "running":
			dot = style.Green.Render(" ●")
		case "previous":
			dot = style.Dim.Render(" ○")
		}
		stats += dot
	}
	left := iconText + " " + name
	space := width - ansi.StringWidth(stats) - 1
	return fit(left, max(0, space), true) + " " + stats
}

// serviceStats is a short word on what a service has shown so far.
func serviceStats(entry *svcEntry) string {
	switch v := entry.view.(type) {
	case *wiremockView:
		unmatched := 0
		for _, e := range v.events {
			if !e.Matched && !v.isNoise(e) {
				unmatched++
			}
		}
		text := style.Dim.Render(compact(len(v.events)-v.noisy()) + " req")
		if unmatched > 0 {
			text += style.Red.Render(" " + ic.failed + " " + compact(unmatched))
		}
		return text
	case *kafkaView:
		if v.client.Error() != "" {
			return style.Red.Render(ic.warning)
		}
		return style.Dim.Render(compact(len(v.messages)-v.noisy()) + " msg")
	case *dbView:
		if v.client != nil && v.client.Error() != "" {
			return style.Red.Render(ic.warning)
		}
		unit := " q"
		if v.kind == "redis" {
			unit = " cmd"
		}
		return style.Dim.Render(compact(len(v.queries)-v.noisy()) + unit)
	}
	if entry.problems > 0 {
		return style.Yellow.Render(ic.warning + " " + compact(entry.problems))
	}
	return ""
}

func (l *serviceList) level(m *Model) level {
	return level{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
		rows := l.rows(m)
		switch k.String() {
		case "up", "k":
			l.choose(m, l.cursor-1)
		case "down", "j":
			l.choose(m, l.cursor+1)
		case "home", "g":
			l.choose(m, 0)
		case "end", "G":
			l.choose(m, len(rows)-1)
		case "enter", "right", "l":
			m.setFocus(l.selectedView(m).main())
		default:
			return false, nil
		}
		return true, nil
	}, bindings: []binding{
		bind("enter", "Open", func() tea.Cmd { m.setFocus(l.selectedView(m).main()); return nil }),
		bind("esc", "Tests", func() tea.Cmd { m.setFocus(m.tree); return nil }),
	}}
}
