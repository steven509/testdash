package ui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/gradle"
	"github.com/steven509/testdash/internal/style"
)

// argsEditor changes the arguments every run gives Gradle, as on gradlew's command line, such as -Dname=value,
// for the rest of the session.
type argsEditor struct {
	input lineInput
	err   string
}

func (m *Model) openArgs() {
	e := &argsEditor{input: lineInput{placeholder: "-Dname=value -Pname=value --info"}}
	e.input.SetValue(joinArgs(m.opts.ExtraArgs))
	m.popup = e
}

func (e *argsEditor) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.popup = nil
	case "ctrl+c":
		m.popup = &confirmQuit{}
	case "enter":
		args, err := splitArgs(e.input.Value())
		if err != nil {
			e.err = err.Error()
			return nil
		}
		m.popup = nil
		return m.setExtraArgs(args)
	default:
		e.input.key(k)
		e.err = ""
	}
	return nil
}

func (e *argsEditor) render(m *Model, screen []string) {
	width := min(100, m.width-4)
	inner := width - 4
	content := []string{typed(&e.input), ""}
	if e.err != "" {
		content = append(content, style.Red.Render(e.err), "")
	}
	for _, line := range wrapLines("Every run gives these to Gradle from now on, as gradlew's command line does. "+
		"-Dname=value sets a system property, which tests see when the build passes it to them; -Pname=value sets "+
		"a project property. Quote a value with spaces.", inner) {
		content = append(content, style.Muted.Render(line))
	}
	content = append(content, "", style.Dim.Render("enter keeps them · esc cancels"))
	m.dialog(screen, content, width, "Gradle arguments", -1)
}

// setExtraArgs gives every run from now on these arguments for Gradle; the tests are listed again with them when
// the build's properties changed, as they may change what the build has.
func (m *Model) setExtraArgs(args []string) tea.Cmd {
	before := gradle.BuildProperties(m.opts.ExtraArgs)
	m.opts.ExtraArgs = args
	m.version++
	if joinArgs(gradle.BuildProperties(args)) != joinArgs(before) && !m.running() && !m.discovering && m.testTask != nil {
		return m.discover()
	}
	return nil
}

// splitArgs splits a command line as a shell would: on spaces, but not within quotes, and a backslash escapes
// the character after it.
func splitArgs(line string) ([]string, error) {
	var args []string
	var arg strings.Builder
	inArg, quote, escaped := false, rune(0), false
	for _, r := range line {
		switch {
		case escaped:
			arg.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inArg = true, true
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			arg.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t':
			if inArg {
				args = append(args, arg.String())
				arg.Reset()
				inArg = false
			}
		default:
			arg.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("a quote is not closed")
	}
	if inArg {
		args = append(args, arg.String())
	}
	return args, nil
}

// joinArgs is arguments as a command line splitArgs reads back, quoting those that need it.
func joinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t'\"\\$`") {
			arg = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
		quoted[i] = arg
	}
	return strings.Join(quoted, " ")
}
