package ui

import (
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// copyText puts text on the clipboard and says so.
func (m *Model) copyText(what, text string) tea.Cmd {
	m.notify("Copied "+what, "", "success")
	return func() tea.Msg {
		if systemClipboard(text) {
			return nil
		}
		return tea.SetClipboard(text)()
	}
}

// systemClipboard copies with the desktop's own command. Over SSH that would be the remote machine's clipboard,
// so there the terminal's (OSC 52) is used instead, which reaches the clipboard of the machine it runs on.
var systemClipboard = func(text string) bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	for _, command := range [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"}, {"clip.exe"}} {
		if _, err := exec.LookPath(command[0]); err != nil {
			continue
		}
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return true
		}
	}
	return false
}
