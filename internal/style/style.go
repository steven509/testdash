// Package style is testdash's theme: a dark canvas testdash paints itself, so it looks the same in any terminal,
// an accent gradient the user picks, status colours, and helpers for the pills, gradients and bars the screens use.
package style

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Style wraps text in one SGR sequence and a reset.
type Style struct {
	prefix string
}

func New(s ansi.Style) Style {
	if len(s) == 0 {
		return Style{}
	}
	return Style{prefix: s.String()}
}

func (s Style) Render(text string) string {
	if s.prefix == "" || text == "" {
		return text
	}
	return s.prefix + text + Reset
}

const Reset = "\x1b[m"

// The palette. The greys stay close to neutral, so terminals with 256 colours show them as greys too.
var (
	Base       = lipgloss.Color("#12121C") // the canvas
	Surface    = lipgloss.Color("#1A1A29") // bars, inputs
	Raised     = lipgloss.Color("#30303C") // key caps, quiet pills
	Overlay    = lipgloss.Color("#383848") // toasts, the palette, dialogs
	Line       = lipgloss.Color("#34344F") // borders and rules
	Zebra      = lipgloss.Color("#161624")
	CursorBlur = lipgloss.Color("#30303C")
	Text       = lipgloss.Color("#E4E4F0")
	MutedText  = lipgloss.Color("#A0A0BC")
	SubtleText = lipgloss.Color("#6B6B88")
	Pass       = lipgloss.Color("#4ADE80")
	Fail       = lipgloss.Color("#F87171")
	Warn       = lipgloss.Color("#FBBF24")
	Run        = lipgloss.Color("#60A5FA")
	Teal       = lipgloss.Color("#5EEAD4")
	Pink       = lipgloss.Color("#F472B6")
	Orange     = lipgloss.Color("#FB923C")
)

// Theme is an accent: the colour of focus, selection and the wordmark, and the colour it blends into.
type Theme struct {
	Name      string
	Primary   color.Color
	Secondary color.Color
	Cursor    color.Color
}

var Themes = []Theme{
	{"Violet", lipgloss.Color("#8B7CFF"), lipgloss.Color("#F472B6"), lipgloss.Color("#3D2E86")},
	{"Ocean", lipgloss.Color("#60A5FA"), lipgloss.Color("#2DD4BF"), lipgloss.Color("#1E3A6E")},
	{"Sunset", lipgloss.Color("#FB923C"), lipgloss.Color("#F472B6"), lipgloss.Color("#6B3420")},
	{"Rose", lipgloss.Color("#FB7185"), lipgloss.Color("#C084FC"), lipgloss.Color("#6B2440")},
}

// The accent, set by ApplyTheme.
var (
	Primary   color.Color
	Secondary color.Color
	Cursor    color.Color
	Accent    []color.Color
)

func fg(c color.Color) ansi.Style {
	return ansi.NewStyle().ForegroundColor(c)
}

// Span styles.
var (
	Plain      = Style{}
	Dim        = New(fg(SubtleText))
	Muted      = New(fg(MutedText))
	Bold       = New(fg(Text).Bold())
	Red        = New(fg(Fail))
	Green      = New(fg(Pass))
	Yellow     = New(fg(Warn))
	Cyan       = New(fg(Run))
	BoldRed    = New(fg(Fail).Bold())
	BoldGreen  = New(fg(Pass).Bold())
	BoldYellow = New(fg(Warn).Bold())
	BoldCyan   = New(fg(Run).Bold())
	DimRed     = New(fg(lipgloss.Darken(Fail, 0.25)))
	Fatal      = New(ansi.NewStyle().Bold().ForegroundColor(Text).BackgroundColor(lipgloss.Darken(Fail, 0.35)))
	TealText   = New(fg(Teal))
	PinkText   = New(fg(Pink))
	LineText   = New(fg(Line))
	AccentText Style
	AccentBold Style
)

func init() {
	ApplyTheme("")
}

// ApplyTheme sets the accent by the theme's name; an unknown name gets the first theme.
func ApplyTheme(name string) {
	theme := Themes[0]
	for _, t := range Themes {
		if strings.EqualFold(t.Name, name) {
			theme = t
		}
	}
	Primary, Secondary, Cursor = theme.Primary, theme.Secondary, theme.Cursor
	Accent = []color.Color{Primary, Secondary}
	AccentText = New(fg(Primary))
	AccentBold = New(fg(Primary).Bold())
}

// Bg is a background colour as an SGR sequence, for WithBg.
func Bg(c color.Color) string {
	return ansi.NewStyle().BackgroundColor(c).String()
}

// FgBg is a foreground and background colour as an SGR sequence, for WithBg.
func FgBg(f, b color.Color) string {
	return ansi.NewStyle().ForegroundColor(f).BackgroundColor(b).String()
}

// BoldBg is a bold foreground on a background, for WithBg.
func BoldBg(f, b color.Color) string {
	return ansi.NewStyle().Bold().ForegroundColor(f).BackgroundColor(b).String()
}

// Fg is a foreground colour as a style.
func Fg(c color.Color) Style {
	return New(fg(c))
}

// WithBg keeps a background (and any other attributes in base) under a line whose spans reset their own styles:
// base is set at the start and again after every reset.
func WithBg(line, base string) string {
	if base == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", Reset)
	return base + strings.ReplaceAll(line, Reset, Reset+base) + Reset
}

// Pill is text on a coloured background with a space either side.
func Pill(text string, f, b color.Color) string {
	return WithBg(" "+text+" ", FgBg(f, b))
}

// BoldPill is a Pill in bold.
func BoldPill(text string, f, b color.Color) string {
	return WithBg(" "+text+" ", BoldBg(f, b))
}

// Tint is a colour's dark background, for a pill in that colour.
func Tint(c color.Color) color.Color {
	return lipgloss.Darken(c, 0.68)
}

// Gradient colours each character of text along a blend of colours, in place of any styles it had.
func Gradient(text string, bold bool, stops ...color.Color) string {
	runes := []rune(ansi.Strip(text))
	if len(runes) == 0 {
		return ""
	}
	colors := lipgloss.Blend1D(max(2, len(runes)), stops...)
	var b strings.Builder
	for i, r := range runes {
		s := fg(colors[i])
		if bold {
			s = s.Bold()
		}
		b.WriteString(s.Styled(string(r)))
	}
	return b.String()
}

// GradientBg puts text on a background that blends across it, in a foreground colour, in place of any styles it
// had.
func GradientBg(text string, f color.Color, bold bool, stops ...color.Color) string {
	runes := []rune(ansi.Strip(text))
	if len(runes) == 0 {
		return ""
	}
	colors := lipgloss.Blend1D(max(2, len(runes)), stops...)
	var b strings.Builder
	for i, r := range runes {
		s := ansi.NewStyle().ForegroundColor(f).BackgroundColor(colors[i])
		if bold {
			s = s.Bold()
		}
		b.WriteString(s.Styled(string(r)))
	}
	return b.String()
}

// Bar is a progress bar width cells wide, filled to fraction along a blend of colours.
func Bar(width int, fraction float64, stops ...color.Color) string {
	return BarOf(width, fraction, "━", stops...)
}

// BarOf is a Bar drawn with its own character.
func BarOf(width int, fraction float64, char string, stops ...color.Color) string {
	if width <= 0 {
		return ""
	}
	filled := max(0, min(width, int(float64(width)*fraction+0.5)))
	var b strings.Builder
	if filled > 0 {
		colors := lipgloss.Blend1D(max(2, width), stops...)
		for i := 0; i < filled; i++ {
			b.WriteString(fg(colors[i]).Styled(char))
		}
	}
	if filled < width {
		b.WriteString(LineText.Render(strings.Repeat(char, width-filled)))
	}
	return b.String()
}

// Segment is part of a stacked bar.
type Segment struct {
	Count int
	Color color.Color
}

// Stacked is a bar width cells wide split between segments by their counts, the rest of total in a line colour.
func Stacked(width, total int, segments ...Segment) string {
	if width <= 0 || total <= 0 {
		return LineText.Render(strings.Repeat("━", max(0, width)))
	}
	var b strings.Builder
	used := 0
	for _, s := range segments {
		cells := s.Count * width / total
		if s.Count > 0 && cells == 0 {
			cells = 1
		}
		cells = min(cells, width-used)
		b.WriteString(fg(s.Color).Styled(strings.Repeat("━", cells)))
		used += cells
	}
	b.WriteString(LineText.Render(strings.Repeat("━", width-used)))
	return b.String()
}

// ByName looks up the styles the status icons use.
func ByName(name string) Style {
	switch name {
	case "dim":
		return Dim
	case "cyan":
		return Cyan
	case "green":
		return Green
	case "red":
		return Red
	case "yellow":
		return Yellow
	}
	return Plain
}

// Sparkline draws counts as bars an eighth of a cell high at a time, each in the colour colorOf gives it; an
// empty bucket is a flat line.
func Sparkline(counts []int, colorOf func(i int) color.Color) string {
	const levels = "▁▂▃▄▅▆▇█"
	peak := 0
	for _, c := range counts {
		peak = max(peak, c)
	}
	var b strings.Builder
	for i, c := range counts {
		if c == 0 {
			b.WriteString(LineText.Render("▁"))
			continue
		}
		level := min(7, (c*8-1)/peak)
		b.WriteString(fg(colorOf(i)).Styled(string([]rune(levels)[level])))
	}
	return b.String()
}

// Chart is a bar chart rows lines tall, a bar for each count, the largest count the full height.
func Chart(counts []int, rows int, colorOf func(i int) color.Color) []string {
	const levels = "▁▂▃▄▅▆▇█"
	peak := 0
	for _, c := range counts {
		peak = max(peak, c)
	}
	out := make([]string, rows)
	for r := range out {
		below := (rows - 1 - r) * 8 // eighths of a cell under this line
		var b strings.Builder
		for i, c := range counts {
			height := 0
			if c > 0 {
				height = (c*rows*8 + peak - 1) / peak
			}
			switch fill := height - below; {
			case fill <= 0 && r == rows-1:
				b.WriteString(LineText.Render("▁"))
			case fill <= 0:
				b.WriteString(" ")
			default:
				b.WriteString(fg(colorOf(i)).Styled(string([]rune(levels)[min(8, fill)-1])))
			}
		}
		out[r] = b.String()
	}
	return out
}

// Cell is one character of a picture drawn cell by cell, such as a timeline.
type Cell struct {
	Char  string
	Color color.Color // nil draws the line colour
}

// Cells draws a row of cells, a run of cells in one colour as one span.
func Cells(cells []Cell) string {
	var b strings.Builder
	for _, c := range cells {
		if c.Char == "" {
			b.WriteString(" ")
			continue
		}
		if c.Color == nil {
			b.WriteString(LineText.Render(c.Char))
			continue
		}
		b.WriteString(fg(c.Color).Styled(c.Char))
	}
	return b.String()
}
