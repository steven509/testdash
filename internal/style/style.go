// Package style is testdash's theme: a dark canvas testdash paints itself, so it looks the same in any terminal,
// with a violet-to-pink accent, status colours, and helpers for the pills, gradients and bars the screens use.
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

// Prefix is the SGR sequence that turns the style on.
func (s Style) Prefix() string {
	return s.prefix
}

const Reset = "\x1b[m"

// The palette. The greys stay close to neutral, so terminals with 256 colours show them as greys too.
var (
	Base       = lipgloss.Color("#12121C") // the canvas
	Surface    = lipgloss.Color("#1A1A29") // bars, the sidebar, inputs
	Raised     = lipgloss.Color("#30303C") // header rows, key caps, quiet pills
	Overlay    = lipgloss.Color("#383848") // toasts, the palette, tooltips
	Line       = lipgloss.Color("#34344F") // borders and guides
	Zebra      = lipgloss.Color("#161624")
	Cursor     = lipgloss.Color("#3D2E86")
	CursorBlur = lipgloss.Color("#30303C")
	Text       = lipgloss.Color("#E4E4F0")
	MutedText  = lipgloss.Color("#A0A0BC")
	SubtleText = lipgloss.Color("#6B6B88")
	Primary    = lipgloss.Color("#8B7CFF")
	Pink       = lipgloss.Color("#F472B6")
	Teal       = lipgloss.Color("#5EEAD4")
	Pass       = lipgloss.Color("#4ADE80")
	Fail       = lipgloss.Color("#F87171")
	Warn       = lipgloss.Color("#FBBF24")
	Run        = lipgloss.Color("#60A5FA")
)

// Accent is the gradient focused frames and the wordmark use.
var Accent = []color.Color{Primary, Pink}

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
	AccentText = New(fg(Primary))
	AccentBold = New(fg(Primary).Bold())
	PinkText   = New(fg(Pink))
	TealText   = New(fg(Teal))
	LineText   = New(fg(Line))
)

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

// Tint is a status colour's dark background, for a pill in that colour.
func Tint(c color.Color) color.Color {
	return lipgloss.Darken(c, 0.68)
}

// Gradient colours each character of plain text along a blend of colours.
func Gradient(text string, bold bool, stops ...color.Color) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	colors := lipgloss.Blend1D(len(runes), stops...)
	var b strings.Builder
	for i, r := range runes {
		s := fg(colors[min(i, len(colors)-1)])
		if bold {
			s = s.Bold()
		}
		b.WriteString(s.Styled(string(r)))
	}
	return b.String()
}

// GradientBg puts plain text on a background that blends across it, in a foreground colour.
func GradientBg(text string, f color.Color, bold bool, stops ...color.Color) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	colors := lipgloss.Blend1D(len(runes), stops...)
	var b strings.Builder
	for i, r := range runes {
		s := ansi.NewStyle().ForegroundColor(f).BackgroundColor(colors[min(i, len(colors)-1)])
		if bold {
			s = s.Bold()
		}
		b.WriteString(s.Styled(string(r)))
	}
	return b.String()
}

// Bar is a progress bar width cells wide, filled to fraction along a blend of colours.
func Bar(width int, fraction float64, stops ...color.Color) string {
	if width <= 0 {
		return ""
	}
	filled := int(float64(width)*fraction + 0.5)
	filled = max(0, min(width, filled))
	var b strings.Builder
	if filled > 0 {
		colors := lipgloss.Blend1D(max(2, width), stops...)
		for i := 0; i < filled; i++ {
			b.WriteString(fg(colors[i]).Styled("━"))
		}
	}
	if filled < width {
		b.WriteString(LineText.Render(strings.Repeat("━", width-filled)))
	}
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
