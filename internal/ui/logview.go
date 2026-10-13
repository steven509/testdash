package ui

import (
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/logformat"
)

const maxLogRows = 20_000

// view is one of the run screen's views, or a pane inside one.
type view interface {
	render(m *Model, r rect) []string
	focusables(m *Model) []any
	levels(m *Model, focus any) []level
	main() any
	clear()
	append(m *Model, lines []*logformat.Line)
}

// logView is a scrolling log with a regex filter, and follow and errors-only toggles. Lines come from a buffer,
// so a filter applies to everything kept, including what arrived before it was typed, and matches the whole
// line even when the simplified view hides part of it. Simplified or full lines and the time column are the
// app's settings, so every log shows them alike. Scrolling up pauses following; End or f resumes it.
type logView struct {
	source     func() []*logformat.Line
	info       func() string
	pattern    *regexp.Regexp
	errorsOnly bool
	noise      noiseMarks[*logformat.Line]
	showNoise  bool
	window     *window
	styleShown int
	rows       []string
	wrap       bool
	follow     bool
	top, sub   int // the first line shown when not following: a row, and a line of it when wrapped
	hscroll    int
	input      *lineInput
	inputShown bool
	width      int
	height     int
}

func newLogView(source func() []*logformat.Line, info func() string) *logView {
	return &logView{source: source, info: info, follow: true,
		input: &lineInput{placeholder: "filter (regex), Enter keeps it, Esc clears it"}}
}

func styleKey(m *Model) int {
	key := 1
	if m.logSimple {
		key++
	}
	if m.logTime {
		key += 2
	}
	return key
}

func (v *logView) main() any {
	return v
}

func (v *logView) focusables(m *Model) []any {
	if v.inputShown {
		return []any{v.input, v}
	}
	return []any{v}
}

func (v *logView) shown(line *logformat.Line) bool {
	if v.window != nil && !v.window.holds(line.At) {
		return false
	}
	if v.errorsOnly && !line.IsProblem() {
		return false
	}
	if !v.showNoise && v.isNoise(line) {
		return false
	}
	return v.pattern == nil || v.pattern.MatchString(line.Text)
}

func (v *logView) isNoise(line *logformat.Line) bool {
	return v.noise.is(line, func() string { return lineNoiseText(line) })
}

func (v *logView) forgetNoise(m *Model) {
	v.noise.forget()
	v.rerender(m)
}

func (v *logView) addRows(m *Model, lines []*logformat.Line) {
	simple, showTime := m.logSimple, m.logTime
	for _, line := range lines {
		if v.shown(line) {
			v.rows = append(v.rows, strings.Split(line.Render(simple, showTime), "\n")...)
		}
	}
}

func (v *logView) append(m *Model, lines []*logformat.Line) {
	v.addRows(m, lines)
	if len(v.rows) > maxLogRows+maxLogRows/4 {
		drop := len(v.rows) - maxLogRows
		v.rows = append([]string(nil), v.rows[drop:]...)
		if v.top -= drop; v.top < 0 {
			v.top, v.sub = 0, 0
		}
	}
}

func (v *logView) clear() {
	v.rows = nil
	v.top, v.sub = 0, 0
}

func (v *logView) rerender(m *Model) {
	v.rows = nil
	source := v.source()
	v.addRows(m, source[max(0, len(source)-maxLogRows):])
	if len(v.rows) > maxLogRows {
		v.rows = v.rows[len(v.rows)-maxLogRows:]
	}
	v.styleShown = styleKey(m)
	v.top = min(v.top, max(0, len(v.rows)-1))
}

// latest follows the end of the log again.
func (v *logView) latest() {
	v.follow = true
}

func (v *logView) setWindow(m *Model, w *window) {
	v.window = w
	v.rerender(m)
	if w != nil {
		v.follow, v.top, v.sub = false, 0, 0
	}
}

func (v *logView) modes(m *Model) string {
	return modeChips(v.modeNames(m)...)
}

func (v *logView) modeNames(m *Model) []string {
	var modes []string
	if v.window != nil {
		modes = append(modes, v.window.label)
	}
	if v.pattern != nil {
		modes = append(modes, "filter /"+strings.TrimPrefix(v.pattern.String(), "(?i)")+"/")
	}
	if v.errorsOnly {
		modes = append(modes, "errors only")
	}
	if v.showNoise {
		modes = append(modes, "with noise")
	}
	if !m.logSimple {
		modes = append(modes, "full lines")
	}
	if !v.follow {
		modes = append(modes, "paused, f follows")
	}
	return modes
}

// pieces is how a row shows: its wrapped lines, or the part the horizontal scroll shows.
func (v *logView) pieces(i int) []string {
	row := v.rows[i]
	if !v.wrap {
		if v.hscroll > 0 {
			row = ansi.Cut(row, v.hscroll, v.hscroll+v.width)
		}
		return []string{row}
	}
	if ansi.StringWidth(row) <= v.width {
		return []string{row}
	}
	return strings.Split(ansi.Wrap(row, v.width, ""), "\n")
}

// bottom is the first line shown when the last row ends at the bottom.
func (v *logView) bottom() (int, int) {
	n := 0
	for i := len(v.rows) - 1; i >= 0; i-- {
		n += len(v.pieces(i))
		if n >= v.height {
			return i, n - v.height
		}
	}
	return 0, 0
}

func before(row, sub, otherRow, otherSub int) bool {
	return row < otherRow || (row == otherRow && sub < otherSub)
}

func (v *logView) scroll(delta int) {
	if len(v.rows) == 0 || v.height == 0 {
		return
	}
	if v.follow {
		if delta > 0 {
			return
		}
		v.top, v.sub = v.bottom()
		v.follow = false
	}
	endRow, endSub := v.bottom()
	for ; delta > 0; delta-- {
		if !before(v.top, v.sub, endRow, endSub) {
			v.follow = true
			return
		}
		if v.sub++; v.sub >= len(v.pieces(v.top)) {
			v.top, v.sub = v.top+1, 0
		}
	}
	for ; delta < 0; delta++ {
		switch {
		case v.sub > 0:
			v.sub--
		case v.top > 0:
			v.top--
			v.sub = len(v.pieces(v.top)) - 1
		}
	}
	if !before(v.top, v.sub, endRow, endSub) {
		v.follow = true
	}
}

func (v *logView) visible(width, height int) []string {
	if len(v.rows) > height {
		width-- // the scrollbar's column
	}
	v.width, v.height = width, height
	row, sub := v.top, v.sub
	if v.follow {
		row, sub = v.bottom()
	} else if row >= len(v.rows) {
		row, sub = max(0, len(v.rows)-1), 0
	}
	first := row
	var out []string
	for ; row < len(v.rows) && len(out) < height; row, sub = row+1, 0 {
		pieces := v.pieces(row)
		if sub < len(pieces) {
			out = append(out, pieces[sub:]...)
		}
	}
	shown := block(out, width, height)
	if len(v.rows) > height {
		return withScrollbar(shown, width+1, first, len(v.rows))
	}
	return shown
}

func (v *logView) render(m *Model, r rect) []string {
	if v.styleShown != styleKey(m) { // toggled in another log while this one was hidden
		v.rerender(m)
	}
	out := []string{fit(v.info()+v.modes(m), r.w, true)}
	y := r.y + 1
	if v.inputShown {
		out = append(out, v.input.view(r.w, m.focus == v.input))
		input := v.input
		m.addZone(zone{r: rect{r.x, y, r.w, 1}, click: func(int, int) tea.Cmd { m.setFocus(input); return nil }})
		y++
	}
	height := max(0, r.h-len(out))
	out = append(out, v.visible(r.w, height)...)
	m.addZone(zone{r: rect{r.x, y, r.w, height}, click: func(int, int) tea.Cmd { m.setFocus(v); return nil },
		wheel: v.scroll})
	return out
}

func (v *logView) levels(m *Model, focus any) []level {
	viewLevel := level{bindings: []binding{
		bind("/", "Filter", func() tea.Cmd {
			v.inputShown = true
			m.setFocus(v.input)
			return nil
		}),
		bind("e", "Errors only", func() tea.Cmd {
			v.errorsOnly = !v.errorsOnly
			v.rerender(m)
			return nil
		}),
		bind("n", "Noise", func() tea.Cmd {
			v.showNoise = !v.showNoise
			v.rerender(m)
			return nil
		}).hidden(),
		bind("f", "Follow", func() tea.Cmd {
			v.follow = !v.follow
			if !v.follow {
				v.top, v.sub = v.bottom()
			}
			return nil
		}),
		bind("w", "Wrap", func() tea.Cmd {
			v.wrap = !v.wrap
			v.sub, v.hscroll = 0, 0
			return nil
		}),
		bind("v", "Simple/full", func() tea.Cmd {
			m.logSimple = !m.logSimple
			v.rerender(m)
			return nil
		}),
		bind("t", "Time", func() tea.Cmd {
			m.logTime = !m.logTime
			v.rerender(m)
			return nil
		}),
		bind("esc", "Clear filter", func() tea.Cmd {
			hadWindow := v.window != nil
			v.input.SetValue("")
			v.inputShown = false
			v.pattern, v.window = nil, nil
			if hadWindow {
				v.follow = true
			}
			v.rerender(m)
			m.setFocus(v)
			return nil
		}).hidden().when(func() bool { return v.inputShown || v.pattern != nil || v.window != nil }),
	}}
	if focus == v.input {
		return []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
			if k.String() == "enter" {
				if v.input.Value() == "" {
					v.inputShown = false
				}
				m.setFocus(v)
				return true, nil
			}
			handled, changed := v.input.key(k)
			if changed {
				return true, v.inputChanged(m, v.input)
			}
			return handled, nil
		}}, viewLevel}
	}
	return []level{{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) { return v.key(k), nil }}, viewLevel}
}

func (v *logView) inputChanged(m *Model, input *lineInput) tea.Cmd {
	value := input.Value()
	if value == "" {
		v.pattern = nil
	} else {
		pattern, err := regexp.Compile("(?i)" + value)
		if err != nil {
			return nil
		}
		v.pattern = pattern
	}
	v.rerender(m)
	return nil
}

func (v *logView) key(k tea.KeyPressMsg) bool {
	page := max(1, v.height-1)
	switch k.String() {
	case "up":
		v.scroll(-1)
	case "down":
		v.scroll(1)
	case "pgup":
		v.scroll(-page)
	case "pgdown":
		v.scroll(page)
	case "home", "ctrl+home":
		v.follow, v.top, v.sub = false, 0, 0
	case "end", "ctrl+end":
		v.follow = true
	case "left":
		if v.wrap {
			return false
		}
		v.hscroll = max(0, v.hscroll-8)
	case "right":
		if v.wrap {
			return false
		}
		v.hscroll += 8
	default:
		return false
	}
	return true
}
