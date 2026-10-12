package ui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/style"
)

// rect is a region of the screen.
type rect struct {
	x, y, w, h int
}

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// fit makes a line exactly width cells: cut, or padded with spaces. A cut line ends in an ellipsis when tail is.
func fit(line string, width int, ellipsis bool) string {
	if width <= 0 {
		return ""
	}
	w := ansi.StringWidth(line)
	if w > width {
		tail := ""
		if ellipsis {
			tail = "…"
		}
		line = ansi.Truncate(line, width, tail)
		w = ansi.StringWidth(line)
	}
	if strings.Contains(line, "\x1b") {
		line += style.Reset
	}
	if w < width {
		line += strings.Repeat(" ", width-w)
	}
	return line
}

// block makes lines exactly width by height: each line fitted, missing lines blank, extra lines dropped.
func block(lines []string, width, height int) []string {
	out := make([]string, height)
	for i := range out {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out[i] = fit(line, width, false)
	}
	return out
}

// blockEllipsis is block with an ellipsis on cut lines.
func blockEllipsis(lines []string, width, height int) []string {
	out := make([]string, height)
	for i := range out {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out[i] = fit(line, width, true)
	}
	return out
}

// hjoin puts blocks of the same height side by side.
func hjoin(blocks ...[]string) []string {
	height := 0
	for _, b := range blocks {
		height = max(height, len(b))
	}
	out := make([]string, height)
	for i := range out {
		var line strings.Builder
		for _, b := range blocks {
			if i < len(b) {
				line.WriteString(b[i])
			}
		}
		out[i] = line.String()
	}
	return out
}

// lines splits text into its lines, nil for empty text.
func lines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// wrapLines word-wraps text to width, keeping its styles: a style still open at the end of a line carries on
// at the start of the next.
func wrapLines(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	var out []string
	for _, line := range lines(text) {
		if ansi.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, carryStyles(strings.Split(ansi.Wrap(line, width, ""), "\n"))...)
	}
	return out
}

var sgrSequence = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// carryStyles starts each line with the styles left open by the lines before it, and closes them at its end.
func carryStyles(parts []string) []string {
	open := ""
	for i, part := range parts {
		line := open + part
		for _, sequence := range sgrSequence.FindAllString(part, -1) {
			if sequence == style.Reset || sequence == "\x1b[0m" {
				open = ""
			} else {
				open += sequence
			}
		}
		if open != "" {
			line += style.Reset
		}
		parts[i] = line
	}
	return parts
}

// overlay draws top over base at (x, y), keeping what base shows either side of it.
func overlay(base []string, top []string, x, y int) {
	for i, line := range top {
		row := y + i
		if row < 0 || row >= len(base) {
			continue
		}
		w := ansi.StringWidth(line)
		left := ansi.Truncate(base[row], x, "")
		if gap := x - ansi.StringWidth(left); gap > 0 {
			left += strings.Repeat(" ", gap)
		}
		right := ansi.TruncateLeft(base[row], x+w, "")
		base[row] = left + style.Reset + line + style.Reset + right
	}
}

// box draws a rounded frame around content of (width-2) by (height-2) cells, with a title pill at the top left
// and a subtitle at the bottom right. A focused frame blends the accent colours around it.
func box(content []string, width, height int, title, subtitle string, focused bool) []string {
	if width < 4 || height < 2 {
		return block(nil, width, height)
	}
	frame := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(width).Height(height)
	if focused {
		frame = frame.BorderForegroundBlend(style.Accent...)
	} else {
		frame = frame.BorderForeground(style.Line)
	}
	out := strings.Split(frame.Render(strings.Join(block(content, width-2, height-2), "\n")), "\n")
	if title != "" {
		pill := style.Pill(title, style.MutedText, style.Raised)
		if focused {
			pill = style.BoldPill(title, style.Base, style.Primary)
		}
		overlay(out, []string{ansi.Truncate(pill, width-4, "")}, 2, 0)
	}
	if subtitle != "" && width > 8 {
		sub := ansi.Truncate(style.Dim.Render(" "+subtitle+" "), width-6, "…")
		overlay(out, []string{sub}, width-2-ansi.StringWidth(sub), height-1)
	}
	return out
}

// padded adds horizontal padding inside a width.
func padded(content []string, width, height, left, right int) []string {
	inner := block(content, max(0, width-left-right), height)
	for i, line := range inner {
		inner[i] = strings.Repeat(" ", left) + line + strings.Repeat(" ", right)
	}
	return inner
}

// bar is a line of the full width on a background, with one cell of padding either side.
func bar(line string, width int, base string) string {
	return style.WithBg(" "+fit(line, max(0, width-2), true)+" ", base)
}

// leftBorder puts a vertical line down the left of a block.
func leftBorder(content []string) []string {
	out := make([]string, len(content))
	for i, line := range content {
		out[i] = style.LineText.Render("│") + line
	}
	return out
}

// splitWidth divides a width by fractions, the last part taking what is left.
func splitWidth(total int, fractions ...int) []int {
	sum := 0
	for _, f := range fractions {
		sum += f
	}
	widths := make([]int, len(fractions))
	used := 0
	for i, f := range fractions {
		if i == len(fractions)-1 {
			widths[i] = total - used
		} else {
			widths[i] = total * f / sum
			used += widths[i]
		}
	}
	return widths
}

// runningGlyph is the spinner's frame, which a running test shows; the model sets it as it draws.
var runningGlyph = "▶"

func icon(status string) (string, style.Style) {
	switch status {
	case "running":
		return runningGlyph, style.Cyan
	case "passed":
		return ic.passed, style.Green
	case "failed":
		return ic.failed, style.Red
	case "aborted":
		return ic.aborted, style.Yellow
	case "skipped":
		return ic.skipped, style.Yellow
	case "absent", "idle":
		return ic.idle, style.Dim
	}
	return ic.pending, style.Dim
}

func clock(at time.Time) string {
	if at.IsZero() {
		return "        "
	}
	return at.Local().Format("15:04:05")
}

func clockMillis(at time.Time) string {
	return at.Local().Format("15:04:05.000")
}

func duration(millis int64) string {
	seconds := float64(millis) / 1000
	switch {
	case seconds < 10:
		return fmt.Sprintf("%.1fs", seconds)
	case seconds < 60:
		return fmt.Sprintf("%.0fs", seconds)
	}
	return fmt.Sprintf("%d:%02d", int(seconds)/60, int(seconds)%60)
}

func count(n int, one string, many ...string) string {
	if n == 1 {
		return "1 " + one
	}
	if len(many) > 0 {
		return number(n) + " " + many[0]
	}
	return number(n) + " " + one + "s"
}

// number writes a count with thousands separators: 1,456.
func number(n int) string {
	digits := strconv.Itoa(n)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	return sign + b.String()
}

// compact rounds a count for a narrow column: 999, 1.5k, 10.5k, 107k, 2.4M.
func compact(n int) string {
	if n > -1000 && n < 1000 {
		return strconv.Itoa(n)
	}
	value, unit := float64(n)/1000, "k"
	if math.Abs(value) >= 999.5 {
		value, unit = value/1000, "M"
	}
	if math.Abs(value) >= 99.95 {
		return fmt.Sprintf("%.0f%s", value, unit)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0") + unit
}

// preview flattens text to one line of at most width characters.
func preview(text *string, width int) string {
	if text == nil {
		return style.Dim.Render("null")
	}
	flat := strings.Join(strings.Fields(cleanText(*text)), " ")
	if runes := []rune(flat); len(runes) > width {
		return string(runes[:width-1]) + "…"
	}
	return flat
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// withScrollbar puts a scrollbar down the right of a block when its content is longer than it: offset of total
// lines starts the view.
func withScrollbar(content []string, width, offset, total int) []string {
	height := len(content)
	if total <= height || height == 0 || width < 2 {
		return content
	}
	thumb := max(1, height*height/total)
	start := min(height-thumb, offset*height/total)
	out := make([]string, height)
	for i, line := range content {
		mark := " "
		if i >= start && i < start+thumb {
			mark = style.Dim.Render("▐")
		}
		out[i] = fit(line, width-1, false) + mark
	}
	return out
}
