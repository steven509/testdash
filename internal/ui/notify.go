package ui

import (
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// notifyDesktop asks the terminal for a desktop notification, as the settings say: while it is in the background,
// always, or never.
func (m *Model) notifyDesktop(text string) tea.Cmd {
	if m.prefs.Notify == "off" || (m.prefs.Notify != "always" && !m.away) {
		return nil
	}
	return tea.Raw(desktopNotification("testdash · " + baseName(m.opts.Workdir) + ": " + text))
}

// desktopNotification is the escape code for one: kitty's own, else iTerm2's, which most terminals that show
// notifications take; inside tmux it passes through to the terminal.
func desktopNotification(text string) string {
	seq := ansi.Notify(text)
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" {
		seq = ansi.DesktopNotification(text)
	}
	if os.Getenv("TMUX") != "" {
		seq = ansi.TmuxPassthrough(seq)
	}
	return seq
}
