package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/style"
)

// lineInput is a one-line text field.
type lineInput struct {
	value       []rune
	cursor      int
	placeholder string
}

func (in *lineInput) Value() string {
	return string(in.value)
}

func (in *lineInput) SetValue(value string) {
	in.value = []rune(value)
	in.cursor = len(in.value)
}

func (in *lineInput) insert(text string) {
	runes := []rune(cleanText(text))
	in.value = append(in.value[:in.cursor], append(runes, in.value[in.cursor:]...)...)
	in.cursor += len(runes)
}

// key edits the field; handled is false for keys it leaves to others, changed says whether the value changed.
func (in *lineInput) key(k tea.KeyPressMsg) (handled, changed bool) {
	before := in.Value()
	mod := k.Mod
	switch k.String() {
	case "backspace", "ctrl+h":
		if in.cursor > 0 {
			in.value = append(in.value[:in.cursor-1], in.value[in.cursor:]...)
			in.cursor--
		}
	case "delete", "ctrl+d":
		if in.cursor < len(in.value) {
			in.value = append(in.value[:in.cursor], in.value[in.cursor+1:]...)
		}
	case "left", "ctrl+b":
		in.cursor = max(0, in.cursor-1)
	case "right", "ctrl+f":
		in.cursor = min(len(in.value), in.cursor+1)
	case "home", "ctrl+a":
		in.cursor = 0
	case "end", "ctrl+e":
		in.cursor = len(in.value)
	case "ctrl+u":
		in.value, in.cursor = in.value[in.cursor:], 0
	case "ctrl+k":
		in.value = in.value[:in.cursor]
	case "ctrl+w", "alt+backspace":
		start := in.cursor
		for start > 0 && in.value[start-1] == ' ' {
			start--
		}
		for start > 0 && in.value[start-1] != ' ' {
			start--
		}
		in.value, in.cursor = append(in.value[:start], in.value[in.cursor:]...), start
	default:
		if k.Text == "" || mod.Contains(tea.ModCtrl) || mod.Contains(tea.ModAlt) {
			return false, false
		}
		in.insert(k.Text)
	}
	return true, in.Value() != before
}

func (in *lineInput) view(width int, focused bool) string {
	if width <= 0 {
		return ""
	}
	base := style.FgBg(style.Text, style.Surface)
	prompt := style.Dim.Render(" ⌕ ")
	if focused {
		base = style.FgBg(style.Text, style.Raised)
		prompt = style.AccentBold.Render(" ⌕ ")
	}
	width -= 3
	if len(in.value) == 0 && !focused {
		return style.WithBg(prompt+fit(style.Dim.Render(in.placeholder), width, true), base)
	}
	text := in.value
	cursor := in.cursor
	start := 0
	if cursor >= width {
		start = cursor - width + 1
	}
	visible := text[start:min(len(text), start+width)]
	caret := ansi.NewStyle().ForegroundColor(style.Base).BackgroundColor(style.Primary)
	var b strings.Builder
	for i, r := range visible {
		if focused && start+i == cursor {
			b.WriteString(caret.Styled(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	if focused && cursor >= len(text) {
		b.WriteString(caret.Styled(" "))
	}
	if len(in.value) == 0 && focused {
		b.WriteString(style.Dim.Render(in.placeholder))
	}
	return style.WithBg(prompt+fit(b.String(), width, false), base)
}

// table is a list of rows with a header and a row cursor.
type table struct {
	columns        []string
	rows           []tableRow
	widths         []int
	cursor         int
	offset         int
	height         int
	scrollToCursor bool
}

type tableRow struct {
	key   string
	cells []string
}

func newTable(columns ...string) *table {
	t := &table{columns: columns}
	t.clear()
	return t
}

func (t *table) clear() {
	t.rows = nil
	t.widths = make([]int, len(t.columns))
	for i, c := range t.columns {
		t.widths[i] = ansi.StringWidth(c)
	}
}

func (t *table) addRow(key string, cells ...string) {
	for i, cell := range cells {
		if i < len(t.widths) {
			t.widths[i] = max(t.widths[i], ansi.StringWidth(cell))
		}
	}
	t.rows = append(t.rows, tableRow{key: key, cells: cells})
}

func (t *table) cursorKey() string {
	if t.cursor >= 0 && t.cursor < len(t.rows) {
		return t.rows[t.cursor].key
	}
	return ""
}

// selectKey moves the cursor to the row with a key, when there is one.
func (t *table) selectKey(key string) {
	for i, row := range t.rows {
		if row.key == key {
			t.moveCursor(i)
			return
		}
	}
}

func (t *table) moveCursor(row int) {
	t.cursor = max(0, min(row, len(t.rows)-1))
	t.scrollToCursor = true
}

// key moves the cursor; selected reports Enter on a row.
func (t *table) key(k tea.KeyPressMsg) (handled, selected bool) {
	page := max(1, t.height-1)
	switch k.String() {
	case "up":
		t.moveCursor(t.cursor - 1)
	case "down":
		t.moveCursor(t.cursor + 1)
	case "pgup":
		t.moveCursor(t.cursor - page)
	case "pgdown":
		t.moveCursor(t.cursor + page)
	case "home", "ctrl+home":
		t.moveCursor(0)
	case "end", "ctrl+end":
		t.moveCursor(len(t.rows) - 1)
	case "enter":
		return true, len(t.rows) > 0
	default:
		return false, false
	}
	return true, false
}

func (t *table) scroll(delta int) {
	t.offset = max(0, min(t.offset+delta, len(t.rows)-t.height))
}

// columnWidths shrinks the widest columns until the table fits width.
func (t *table) columnWidths(width int) []int {
	widths := append([]int{}, t.widths...)
	total := func() int {
		sum := 0
		for _, w := range widths {
			sum += w + 2
		}
		return sum
	}
	for total() > width {
		widest := 0
		for i, w := range widths {
			if w > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= 6 {
			break
		}
		widths[widest] -= min(widths[widest]-6, total()-width)
	}
	return widths
}

func (t *table) view(width, height int, focused bool) []string {
	t.height = max(1, height-1)
	if len(t.rows) == 0 {
		t.cursor, t.offset = 0, 0
	} else {
		t.cursor = max(0, min(t.cursor, len(t.rows)-1))
	}
	if t.scrollToCursor {
		if t.cursor < t.offset {
			t.offset = t.cursor
		} else if t.cursor >= t.offset+t.height {
			t.offset = t.cursor - t.height + 1
		}
		t.scrollToCursor = false
	}
	t.offset = max(0, min(t.offset, len(t.rows)-t.height))
	widths := t.columnWidths(width)
	render := func(cells []string) string {
		var b strings.Builder
		for i, w := range widths {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			b.WriteString(" " + fit(cell, w, true) + " ")
		}
		return b.String()
	}
	out := []string{style.WithBg(fit(render(t.columns), width, false), style.BoldBg(style.MutedText, style.Raised))}
	for i := t.offset; i < len(t.rows) && len(out) < height; i++ {
		line := fit(render(t.rows[i].cells), width, false)
		switch {
		case i == t.cursor && focused:
			line = style.WithBg(line, style.BoldBg(style.Text, style.Cursor))
		case i == t.cursor:
			line = style.WithBg(line, style.Bg(style.CursorBlur))
		case i%2 == 1:
			line = style.WithBg(line, style.Bg(style.Zebra))
		}
		out = append(out, line)
	}
	return block(out, width, height)
}

// rowAt is the row index at a line of the table's view, counting the header as line 0.
func (t *table) rowAt(line int) (int, bool) {
	row := t.offset + line - 1
	if line < 1 || row >= len(t.rows) {
		return 0, false
	}
	return row, true
}

// textbox is a scrolling block of wrapped text, such as a row's details.
type textbox struct {
	about      string
	content    string
	offset     int
	height     int
	wrapped    []string
	wrappedFor string
	wrapWidth  int
}

// set shows content; the scroll position stays while what the content is about stays the same.
func (b *textbox) set(about, content string) {
	if about != b.about {
		b.offset = 0
	}
	b.about, b.content = about, content
}

func (b *textbox) lines(width int) []string {
	if b.wrappedFor != b.content || b.wrapWidth != width {
		b.wrapped, b.wrappedFor, b.wrapWidth = wrapLines(b.content, width), b.content, width
	}
	return b.wrapped
}

func (b *textbox) view(width, height int) []string {
	b.height = height
	all := b.lines(width)
	b.offset = max(0, min(b.offset, len(all)-height))
	return block(all[b.offset:min(len(all), b.offset+height)], width, height)
}

func (b *textbox) scroll(delta int) {
	b.offset = max(0, min(b.offset+delta, len(b.wrapped)-b.height))
}

func (b *textbox) key(k tea.KeyPressMsg) bool {
	page := max(1, b.height-1)
	switch k.String() {
	case "up":
		b.scroll(-1)
	case "down":
		b.scroll(1)
	case "pgup":
		b.scroll(-page)
	case "pgdown":
		b.scroll(page)
	case "home":
		b.offset = 0
	case "end":
		b.scroll(len(b.wrapped))
	default:
		return false
	}
	return true
}

// tabs draws a row of tab pills, the active one lit, and returns where each sits.
func tabs(labels []string, active, width int) (string, [][2]int) {
	var row strings.Builder
	var spans [][2]int
	x := 0
	for i, label := range labels {
		pill := style.Pill(label, style.MutedText, style.Surface)
		if i == active {
			pill = style.BoldPill(label, style.Base, style.Primary)
		}
		w := ansi.StringWidth(pill)
		spans = append(spans, [2]int{x, x + w})
		row.WriteString(pill + " ")
		x += w + 1
	}
	return fit(row.String(), width, true), spans
}

// cleanText is text from outside safe to draw: no escape sequences or control characters.
func cleanText(text string) string {
	return logformat.Plain(logformat.Sanitize(text))
}

// cleanBlock is cleanText for each line of a block of text.
func cleanBlock(text string) string {
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		parts[i] = cleanText(part)
	}
	return strings.Join(parts, "\n")
}
