package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/style"
)

// keycastFor is how long the keys stay on screen after the last one.
const keycastFor = 1500 * time.Millisecond

// keycast shows the last keys pressed at the bottom of the screen, with what they did, for screen recordings
// and demos (TESTDASH_KEYCAST=1).
type keycast struct {
	keys  []castKey
	until time.Time
}

// castKey is a key pressed one or more times in a row, or text typed into a field.
type castKey struct {
	key, label string
	times      int
	typed      bool
}

func (c *keycast) add(k tea.KeyPressMsg, typed bool, label string) {
	now := time.Now()
	if now.After(c.until) {
		c.keys = nil
	}
	c.until = now.Add(keycastFor)
	name := displayKey(k.String())
	last := len(c.keys) - 1
	switch {
	case typed && last >= 0 && c.keys[last].typed:
		c.keys[last].key += k.Text
	case typed:
		c.keys = append(c.keys, castKey{key: k.Text, typed: true})
	case last >= 0 && !c.keys[last].typed && c.keys[last].key == name && c.keys[last].label == label:
		c.keys[last].times++
	default:
		c.keys = append(c.keys, castKey{key: name, label: label, times: 1})
	}
	if len(c.keys) > 3 {
		c.keys = c.keys[len(c.keys)-3:]
	}
}

func (m *Model) renderKeycast(screen []string) {
	if len(m.keycast.keys) == 0 || time.Now().After(m.keycast.until) {
		return
	}
	var parts []string
	for _, k := range m.keycast.keys {
		if k.typed {
			parts = append(parts, style.BoldPill(k.key, style.Text, style.Raised))
			continue
		}
		text := keyCap(k.key)
		if k.times > 1 {
			text += style.Muted.Render(" ×" + number(k.times))
		}
		if k.label != "" {
			text += " " + k.label
		}
		parts = append(parts, text)
	}
	line := strings.Join(parts, style.Dim.Render("  ·  "))
	width := min(ansi.StringWidth(line)+4, m.width-2)
	lines := card([]string{line}, width, style.Primary, lipgloss.Darken(style.Primary, 0.5))
	overlay(screen, lines, (m.width-width)/2, m.height-1-len(lines))
}
