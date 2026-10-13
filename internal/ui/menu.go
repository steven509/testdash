package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/style"
)

// menuAction is something a right click offers, with the key that does it too.
type menuAction struct {
	label, key string
	run        func() tea.Cmd
}

// rightClick does what a click there does, picking the row under the pointer, then offers what can be done
// with it: what the focus can do, as its keys would, and what the place clicked adds.
func (m *Model) rightClick(mouse tea.Mouse) tea.Cmd {
	if m.popup != nil {
		m.popup = nil
		return nil
	}
	if mouse.Y == 0 || mouse.Y >= m.height-1 { // the header and the footer run things when clicked
		return nil
	}
	var extra []menuAction
	for i := len(m.zones) - 1; i >= 0; i-- {
		z := m.zones[i]
		if !z.r.contains(mouse.X, mouse.Y) || (z.click == nil && z.menu == nil) {
			continue
		}
		if z.click != nil {
			z.click(mouse.X-z.r.x, mouse.Y-z.r.y)
		}
		if z.menu != nil {
			extra = z.menu(mouse.X-z.r.x, mouse.Y-z.r.y)
		}
		break
	}
	if m.popup != nil {
		return nil
	}
	actions := append(extra, m.focusActions()...)
	if len(actions) > 0 {
		m.popup = &contextMenu{x: mouse.X, y: mouse.Y, actions: actions}
	}
	return nil
}

// focusActions are what the focus can do: its own keys, not testdash's everywhere, nor moving between tabs.
func (m *Model) focusActions() []menuAction {
	chain := m.chain()
	seen := map[string]bool{}
	var actions []menuAction
	for _, l := range chain[:len(chain)-1] {
		for _, b := range l.bindings {
			if b.priority || !b.active() || b.label == "" || seen[b.display] {
				continue
			}
			seen[b.display] = true
			actions = append(actions, menuAction{label: b.label, key: b.display, run: b.run})
		}
	}
	return actions
}

// contextMenu is a right click's menu, by the pointer.
type contextMenu struct {
	x, y    int
	actions []menuAction
	cursor  int
}

func (c *contextMenu) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.popup = nil
	case "up", "k":
		c.cursor = max(0, c.cursor-1)
	case "down", "j":
		c.cursor = min(len(c.actions)-1, c.cursor+1)
	case "enter", "space":
		return c.choose(m, c.cursor)
	default:
		for i, action := range c.actions {
			if action.key != "" && action.key == displayKey(k.String()) {
				return c.choose(m, i)
			}
		}
		m.popup = nil
	}
	return nil
}

func (c *contextMenu) choose(m *Model, i int) tea.Cmd {
	m.popup = nil
	return c.actions[i].run()
}

func (c *contextMenu) render(m *Model, screen []string) {
	width := 0
	for _, action := range c.actions {
		width = max(width, len([]rune(action.label))+len([]rune(action.key))+8)
	}
	width = min(width, m.width-2)
	var rows []string
	for i, action := range c.actions {
		rows = append(rows, menuRow(action.label, action.key, width-4, i == c.cursor))
	}
	framed := card(rows, width, style.Accent...)
	x, y := min(c.x, m.width-width), min(c.y, m.height-len(framed))
	m.addZone(zone{r: rect{0, 0, m.width, m.height}, click: func(int, int) tea.Cmd {
		m.popup = nil
		return nil
	}})
	overlay(screen, framed, max(0, x), max(0, y))
	m.addZone(zone{r: rect{max(0, x), max(0, y) + 1, width, len(rows)}, click: func(_, row int) tea.Cmd {
		return c.choose(m, row)
	}})
}
