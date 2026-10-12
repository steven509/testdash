package ui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/style"
)

const (
	narrowWidth   = 96 // below this, the lists and the details take turns on the screen
	headerHeight  = 2
	minLeftWidth  = 34
	headerMargin  = 5 // room at the right for the buttons some terminals draw over the top of a pane
	maxLeftWidth  = 58
	leftWidthPart = 38 // percent of the width
)

// renderScreen draws the header, the panels and the footer, exactly the terminal's size.
func (m *Model) renderScreen() []string {
	screen := append([]string{}, m.header()...)
	bodyHeight := m.height - headerHeight - 1
	screen = append(screen, m.body(rect{0, headerHeight, m.width, bodyHeight})...)
	screen = block(screen, m.width, m.height-1)
	return append(screen, m.footer(m.height-1))
}

// header is the top two lines: who and what, how the run is going, and a progress rule across the screen.
func (m *Model) header() []string {
	left := " " + style.Gradient(ic.logo+" testdash", true, style.Accent...) + "  " +
		style.Muted.Render(baseName(m.opts.Workdir))
	if m.testTask != nil && m.opts.Command == nil {
		task := style.Pill(m.testTask.Path+" ▾", style.Teal, style.Raised)
		x := ansi.StringWidth(left) + 2
		m.addZone(zone{r: rect{x, 0, ansi.StringWidth(task), 1}, click: func(int, int) tea.Cmd {
			m.openTasks()
			return nil
		}})
		left += "  " + task
	}
	right := m.counts() + "  " + m.stateBadge() + strings.Repeat(" ", headerMargin)
	middle := "   " + m.runLine()
	space := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	line := left + fit(middle, max(0, space), true) + right
	if space < 4 {
		line = left + strings.Repeat(" ", max(0, m.width-ansi.StringWidth(left)-ansi.StringWidth(m.stateBadge())-headerMargin)) +
			m.stateBadge()
	}
	return []string{fit(line, m.width, true), m.progressRule()}
}

// runLine is what the run is doing right now.
func (m *Model) runLine() string {
	switch {
	case m.discovering && !m.running():
		return style.Cyan.Render(m.spinnerFrame() + " listing the build's tests…")
	case m.runStarted.IsZero():
		if m.testTask == nil {
			return ""
		}
		return style.Dim.Render(fmt.Sprintf("%d tests ready · ", len(m.selectable))) + style.AccentText.Render("r") +
			style.Dim.Render(" "+strings.ToLower(m.runLabel()))
	case !m.running():
		return style.Dim.Render(m.displayCommand())
	}
	s := m.runSummary()
	if len(s.running) == 0 {
		return style.Cyan.Render(m.spinnerFrame() + " " + m.activity())
	}
	r := s.running[0]
	took, _ := r.state.Took(nowMillis())
	line := style.Cyan.Render(m.spinnerFrame()+" ") + style.Muted.Render(strings.Join(m.groupOf(r.entry), " › ")+" › ") +
		style.BoldCyan.Render(cleanText(r.entry.Name)) + style.Dim.Render("  "+duration(took))
	if len(s.running) > 1 {
		line += style.Dim.Render(fmt.Sprintf("  +%d more", len(s.running)-1))
	}
	return line
}

// counts are the run's results, each lit when it is not zero.
func (m *Model) counts() string {
	if m.runStarted.IsZero() {
		return ""
	}
	s := m.runSummary()
	var parts []string
	for _, part := range []struct {
		n      int
		status string
		color  color.Color
	}{{s.passed, "passed", style.Pass}, {s.failed, "failed", style.Fail}, {s.skipped, "skipped", style.Warn},
		{s.left, "pending", style.MutedText}} {
		mark, _ := icon(part.status)
		text := fmt.Sprintf("%s %d", mark, part.n)
		if part.n == 0 {
			parts = append(parts, style.Dim.Render(text))
		} else {
			parts = append(parts, style.Fg(part.color).Render(text))
		}
	}
	return strings.Join(parts, "  ")
}

// stateBadge is how the run is going, as a pill.
func (m *Model) stateBadge() string {
	switch {
	case m.runStarted.IsZero():
		return style.Pill("READY", style.MutedText, style.Raised)
	case m.runFinished.IsZero():
		return style.BoldPill(m.spinnerFrame()+" RUNNING "+m.elapsed(), style.Base, style.Run)
	case m.exitCode == 0:
		return style.BoldPill(ic.passed+" PASSED "+m.elapsed(), style.Base, style.Pass)
	}
	return style.BoldPill(fmt.Sprintf("%s FAILED %s", ic.failed, m.elapsed()), style.Base, style.Fail)
}

// progressRule is a line across the screen that fills as the run goes, or sweeps while it builds.
func (m *Model) progressRule() string {
	if m.runStarted.IsZero() {
		if m.discovering {
			return m.sweep()
		}
		return style.LineText.Render(strings.Repeat("─", m.width))
	}
	s := m.runSummary()
	if s.total == 0 && m.running() {
		return m.sweep()
	}
	fraction := 0.0
	if s.total > 0 {
		fraction = float64(s.done()) / float64(s.total)
	}
	return style.Bar(m.width, fraction, m.barColors(s)...)
}

// sweep is a short bright segment moving along the rule, for work of unknown length.
func (m *Model) sweep() string {
	const length = 16
	at := int(time.Now().UnixMilli()/40) % (m.width + length)
	var b strings.Builder
	colors := lipgloss.Blend1D(length, style.Accent...)
	for x := 0; x < m.width; x++ {
		if i := x - (at - length); i >= 0 && i < length {
			b.WriteString(style.Fg(colors[i]).Render("━"))
		} else {
			b.WriteString(style.LineText.Render("─"))
		}
	}
	return b.String()
}

// barColors are a progress bar's gradient: teal to green while all is well, amber to red once a test failed.
func (m *Model) barColors(s summary) []color.Color {
	if s.failed > 0 {
		return []color.Color{style.Warn, style.Fail}
	}
	return []color.Color{style.Teal, style.Pass}
}

// body lays out the tests and services on the left and the details on the right. A narrow terminal shows the
// lists or the details, whichever has focus; zoom gives the details the whole width.
func (m *Model) body(r rect) []string {
	focusInDetail := m.panelOf(m.focus) == panelDetail
	showLists, showDetail := true, true
	switch {
	case m.zoomed:
		showLists = false
	case m.width < narrowWidth && focusInDetail:
		showLists = false
	case m.width < narrowWidth:
		showDetail = false
	}
	leftWidth := min(maxLeftWidth, max(minLeftWidth, m.width*leftWidthPart/100))
	if !showDetail {
		leftWidth = r.w
	}
	var columns [][]string
	if showLists {
		servicesHeight := m.serviceList.preferredHeight(m, r.h)
		testsHeight := r.h - servicesHeight
		tests := m.tree.render(m, rect{r.x, r.y, leftWidth, testsHeight})
		list := m.serviceList.render(m, rect{r.x, r.y + testsHeight, leftWidth, servicesHeight})
		columns = append(columns, append(tests, list...))
	}
	if showDetail {
		x := r.x
		if showLists {
			x += leftWidth
		}
		columns = append(columns, m.detailPanel(rect{x, r.y, r.w - (x - r.x), r.h}))
	}
	return hjoin(columns...)
}

// detailPanel frames whatever the detail pane shows.
func (m *Model) detailPanel(r rect) []string {
	inner := rect{r.x + 2, r.y + 1, r.w - 4, r.h - 2}
	focused := m.panelOf(m.focus) == panelDetail
	var title, badge string
	if m.detailFrom == panelServices {
		title, badge = m.serviceList.title(m)
	} else {
		title, badge = m.inspector.title(m)
	}
	content := m.detailView().render(m, inner)
	hint := ""
	if m.zoomed {
		hint = "z restores"
	}
	return frame(content, r, numbered(3, title), badge, hint, focused)
}

// frame draws a panel: a rounded border, its title and a badge at the top, a hint at the bottom, and content
// with a column of padding either side. A focused panel's border blends the accent colours.
func frame(content []string, r rect, title, badge, hint string, focused bool) []string {
	if r.w < 6 || r.h < 3 {
		return block(nil, r.w, r.h)
	}
	inner := padded(block(content, r.w-4, r.h-2), r.w-2, r.h-2, 1, 1)
	border := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(r.w).Height(r.h)
	if focused {
		border = border.BorderForegroundBlend(style.Accent...)
	} else {
		border = border.BorderForeground(style.Line)
	}
	out := strings.Split(border.Render(strings.Join(inner, "\n")), "\n")
	if title != "" {
		number, name, numbered := strings.Cut(title, " ")
		if !numbered || len(number) != 1 || number[0] < '1' || number[0] > '9' {
			number, name = "", title
		}
		pill := style.Dim.Render(" "+number+" ") + style.Bold.Render(name+" ")
		if focused {
			pill = style.BoldPill(number, style.Primary, style.Cursor) + style.BoldPill(name, style.Base, style.Primary)
		}
		if number == "" {
			pill = style.Bold.Render(" " + name + " ")
			if focused {
				pill = style.BoldPill(name, style.Base, style.Primary)
			}
		}
		overlay(out, []string{ansi.Truncate(pill, r.w-6, "…")}, 2, 0)
	}
	if badge != "" && r.w > ansi.StringWidth(title)+ansi.StringWidth(badge)+10 {
		overlay(out, []string{badge}, r.w-3-ansi.StringWidth(badge), 0)
	}
	if hint != "" && r.w > 20 {
		text := ansi.Truncate(style.Dim.Render(" "+hint+" "), r.w-6, "…")
		overlay(out, []string{text}, r.w-3-ansi.StringWidth(text), r.h-1)
	}
	return out
}

// numbered is a panel's title after the key that focuses it.
func numbered(n int, title string) string {
	return fmt.Sprintf("%d %s", n, title)
}

// chip is a small pill saying how a list is narrowed.
func chip(text string) string {
	return style.Pill(text, style.Warn, style.Tint(style.Warn))
}

// section is a heading inside a panel: a label, then a rule to the edge.
func section(label string, width int, extra string) string {
	head := style.Bold.Render(strings.ToUpper(label))
	if extra != "" {
		head += "  " + style.Dim.Render(extra)
	}
	rest := width - ansi.StringWidth(head) - 1
	if rest > 0 {
		head += " " + style.LineText.Render(strings.Repeat("─", rest))
	}
	return head
}
