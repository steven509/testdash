package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/settings"
	"github.com/steven509/testdash/internal/style"
)

// settingItem is one line of the settings: what it is, what it says now, and how it changes.
type settingItem struct {
	section string // the heading it goes under
	label   string
	visible func(s settings.Settings) bool // shown only when it applies; nil is always
	help    string
	toggle  func(s *settings.Settings) *bool // for an on/off setting
	value   func(s settings.Settings) string // for a choice
	change  func(s *settings.Settings, step int)
}

func choice(options []string, current string, step int) string {
	for i, option := range options {
		if strings.EqualFold(option, current) {
			return options[(i+step+len(options))%len(options)]
		}
	}
	return options[0]
}

func themeNames() []string {
	var names []string
	for _, t := range style.Themes {
		names = append(names, t.Name)
	}
	return names
}

var settingItems = []settingItem{
	{section: "Display", label: "Icons",
		help: "Nerd Font glyphs for the carets, statuses and services. Your terminal needs a Nerd Font " +
			"(nerdfonts.com); Unicode works with any font.",
		value: func(s settings.Settings) string {
			if s.NerdFont {
				return "Nerd Font"
			}
			return "Unicode"
		},
		change: func(s *settings.Settings, _ int) { s.NerdFont = !s.NerdFont }},
	{section: "Display", label: "Icon spacing",
		help: "Spaces after each icon. Nerd Fonts draw some icons wider than a letter, into the " +
			"space after them; two spaces keep them clear of the text. The Mono variant of a Nerd Font keeps every icon " +
			"to one cell instead.",
		visible: func(s settings.Settings) bool { return s.NerdFont },
		value: func(s settings.Settings) string {
			if s.IconSpacing >= 2 {
				return "2 spaces"
			}
			return "1 space"
		},
		change: func(s *settings.Settings, _ int) {
			if s.IconSpacing >= 2 {
				s.IconSpacing = 1
			} else {
				s.IconSpacing = 2
			}
		}},
	{section: "Display", label: "Accent",
		help:   "The colour of focus and selection, and of the gradients frames and bars blend through.",
		value:  func(s settings.Settings) string { return choice(themeNames(), s.Theme, 0) },
		change: func(s *settings.Settings, step int) { s.Theme = choice(themeNames(), s.Theme, step) }},
	{section: "Display", label: "Mouse",
		help:   "Click and scroll in testdash. Off leaves the mouse to your terminal, to select and copy text.",
		toggle: func(s *settings.Settings) *bool { return &s.Mouse }},
	{section: "Logs", label: "Log lines",
		help: "How logs start: simplified to each line's level and message, or as they were logged. " +
			"v switches in a log.",
		value: func(s settings.Settings) string {
			if s.FullLogLines {
				return "As logged"
			}
			return "Simplified"
		},
		change: func(s *settings.Settings, _ int) { s.FullLogLines = !s.FullLogLines }},
	{section: "Logs", label: "Times in logs",
		help:   "Logs start with the time Docker received each line. t switches in a log.",
		toggle: func(s *settings.Settings) *bool { return &s.LogTime }},
	{section: "Logs", label: "Wrap log lines",
		help:   "Logs start wrapping long lines, rather than scrolling sideways. w switches in a log.",
		toggle: func(s *settings.Settings) *bool { return &s.WrapLogs }},
	{section: "Runs", label: "Test output",
		help: "Where tests' own output goes: to the containers' logs only, or into Gradle's output " +
			"too. Applies from the next run.",
		value: func(s settings.Settings) string {
			if s.ShowTestOutput {
				return "Gradle's output too"
			}
			return "Service logs"
		},
		change: func(s *settings.Settings, _ int) { s.ShowTestOutput = !s.ShowTestOutput }},
	{section: "Runs", label: "Containers",
		help: "Follow the containers the run starts, or every container on the machine. Applies " +
			"from the next start.",
		value: func(s settings.Settings) string {
			if s.AllContainers {
				return "Every container"
			}
			return "The run's"
		},
		change: func(s *settings.Settings, _ int) { s.AllContainers = !s.AllContainers }},
	{section: "Runs", label: "Activity window",
		help: "While a run goes, its activity shows the last stretch of it this long, moving on with it; once " +
			"the run is over, all of it. + and − change it on the overview too.",
		value: func(s settings.Settings) string { return duration(s.Window().Milliseconds()) },
		change: func(s *settings.Settings, step int) {
			i := slices.Index(activityWindows, int(s.Window()/time.Second))
			s.ActivityWindow = activityWindows[(max(0, i)+step+len(activityWindows))%len(activityWindows)]
		}},
	{section: "Runs", label: "Profiling",
		help: "Samples the application while tests run: its container's CPU and memory, and for a JVM, its heap, " +
			"garbage collection and threads. The run's Profile tab charts them, and a test's details say what it " +
			"used. It costs a docker exec every two seconds.",
		value: func(s settings.Settings) string {
			if s.Profile {
				return "On"
			}
			return "Off"
		},
		change: func(s *settings.Settings, _ int) { s.Profile = !s.Profile }},
	{section: "Runs", label: "Notifications",
		help: "A desktop notification when a run ends, through your terminal (iTerm2, WezTerm, " +
			"Ghostty, kitty, Windows Terminal and more): while you are in another window, always, or never.",
		value: func(s settings.Settings) string { return notifyLabels[s.Notify] },
		change: func(s *settings.Settings, step int) {
			s.Notify = choice([]string{"away", "always", "off"}, s.Notify, step)
		}},
}

var notifyLabels = map[string]string{"away": "When away", "always": "Always", "off": "Off"}

// integrations are the services testdash reads more than the logs of; each can be hidden.
var integrations = []struct {
	label string
	kinds []string
}{
	{"WireMock", []string{"wiremock"}}, {"Kafka", []string{"kafka"}}, {"MariaDB and MySQL", []string{"mariadb", "mysql"}},
	{"Postgres", []string{"postgres"}}, {"MongoDB", []string{"mongodb"}}, {"Redis", []string{"redis"}},
}

func init() {
	for _, integration := range integrations {
		integration := integration
		settingItems = append(settingItems, settingItem{section: "Integrations", label: integration.label,
			help: integration.label + " containers, with their requests, messages, queries or commands. Hidden, " +
				"testdash leaves them alone: they are not listed, followed, or changed to log what they do.",
			value: func(s settings.Settings) string {
				if s.Hidden(integration.kinds[0]) {
					return "Hidden"
				}
				return "Shown"
			},
			change: func(s *settings.Settings, _ int) { s.Hide(integration.kinds, !s.Hidden(integration.kinds[0])) }})
	}
}

// settingsForm shows and changes the settings, saving each change as it is made.
type settingsForm struct {
	values   settings.Settings
	cursor   int
	status   string
	failed   bool
	onChange func(settings.Settings)
	rowsAt   int        // the first row's line in the last render
	offset   int        // the first line of rows shown, when they do not all fit
	lines    []formLine // the rows and their sections' headings, as the last render laid them out
	shown    int        // how many of them it showed
}

// formLine is a setting's row, or a section's heading (item -1).
type formLine struct {
	item int
	text string
}

func newSettingsForm(values settings.Settings, onChange func(settings.Settings)) *settingsForm {
	f := &settingsForm{values: values, onChange: onChange}
	f.status = "Saved in " + tildePath(settings.Path())
	return f
}

// workPath is a path from the directory testdash runs in, when it is under it, else as tildePath has it.
func (m *Model) workPath(path string) string {
	if rel, err := filepath.Rel(m.opts.Workdir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return tildePath(path)
}

func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// items are the settings that apply to the others' values: icon spacing only with Nerd Font icons.
func (f *settingsForm) items() []settingItem {
	var shown []settingItem
	for _, item := range settingItems {
		if item.visible == nil || item.visible(f.values) {
			shown = append(shown, item)
		}
	}
	return shown
}

func (f *settingsForm) change(i, step int) {
	item := f.items()[i]
	if item.toggle != nil {
		value := item.toggle(&f.values)
		*value = !*value
	} else {
		item.change(&f.values, step)
	}
	if err := f.values.Save(); err != nil {
		f.status, f.failed = "Could not save: "+err.Error(), true
	} else {
		f.status, f.failed = "Saved in "+tildePath(settings.Path()), false
	}
	if f.onChange != nil {
		f.onChange(f.values)
	}
	f.cursor = min(f.cursor, len(f.items())-1)
}

func (f *settingsForm) key(k tea.KeyPressMsg) {
	switch k.String() {
	case "up", "k":
		f.cursor = max(0, f.cursor-1)
	case "down", "j", "tab":
		f.cursor = min(len(f.items())-1, f.cursor+1)
	case "left", "h":
		f.change(f.cursor, -1)
	case "right", "l", "space", "enter":
		f.change(f.cursor, 1)
	}
}

func (f *settingsForm) render(width, height int) []string {
	formWidth := width
	showPreview := width >= 78
	if showPreview {
		formWidth = width - 32
	}
	var rows []string
	rows = append(rows, style.Muted.Render("Choose with ↑ ↓, change with ← → or space."), "")
	f.rowsAt = len(rows)
	help := wrapLines(f.items()[f.cursor].help, formWidth-2)
	items := f.items()
	f.lines = nil
	cursorLine := 0
	for i, item := range items {
		if i == 0 || item.section != items[i-1].section {
			if i > 0 {
				f.lines = append(f.lines, formLine{-1, ""})
			}
			f.lines = append(f.lines, formLine{-1, " " + section(item.section, formWidth-2, "")})
		}
		if i == f.cursor {
			cursorLine = len(f.lines)
		}
		f.lines = append(f.lines, formLine{i, f.row(i, item, formWidth)})
	}
	f.shown = max(4, min(len(f.lines), height-f.rowsAt-max(4, len(help)+1)-1)) // the rows scroll in a short terminal
	top := cursorLine
	for top > 0 && f.lines[top-1].item < 0 && f.lines[top-1].text != "" {
		top-- // with its section's heading
	}
	f.offset = max(0, min(f.offset, top, len(f.lines)-f.shown))
	if cursorLine >= f.offset+f.shown {
		f.offset = cursorLine - f.shown + 1
	}
	for _, line := range f.lines[f.offset:min(len(f.lines), f.offset+f.shown)] {
		rows = append(rows, line.text)
	}
	rows = append(rows, "")
	for _, line := range help {
		rows = append(rows, " "+style.Muted.Render(line))
	}
	for len(rows) < f.rowsAt+f.shown+4 {
		rows = append(rows, "")
	}
	statusStyle := style.Dim
	mark := ic.check + " "
	if f.failed {
		statusStyle, mark = style.Red, ic.failed+" "
	}
	rows = append(rows, statusStyle.Render(mark+f.status))
	if !showPreview {
		return rows
	}
	preview := f.preview(30, max(len(rows), 12))
	return hjoin(block(rows, formWidth, len(preview)), block(nil, 2, len(preview)), preview)
}

func (f *settingsForm) row(i int, item settingItem, width int) string {
	selected := i == f.cursor
	label := fit(item.label, 18, true)
	var control string
	if item.toggle != nil {
		if *item.toggle(&f.values) {
			control = style.BoldPill("● on ", style.Base, style.Pass)
		} else {
			control = style.Pill("○ off", style.MutedText, style.Raised)
		}
	} else {
		value := item.value(f.values)
		if selected {
			control = style.Dim.Render("‹ ") + style.BoldPill(value, style.Base, style.Primary) + style.Dim.Render(" ›")
		} else {
			control = "  " + style.Pill(value, style.Text, style.Raised)
		}
	}
	line := " " + label + control
	if selected {
		return style.AccentBold.Render("▌") + style.WithBg(fit(style.Bold.Render(label)+control, width-1, false),
			style.Bg(style.CursorBlur))
	}
	return line
}

// preview is a small panel drawn with the settings as they are now.
func (f *settingsForm) preview(width, height int) []string {
	_, mark := icon("passed")
	lines := []string{
		style.Gradient(ic.logo+" testdash", true, style.Accent...),
		"",
		style.AccentText.Render(ic.partly) + " " + style.Dim.Render(ic.caretOpen+" ") + style.Red.Render(ic.failed) + " " +
			style.Bold.Render("CalculatorTest"),
		style.Dim.Render(ic.unpicked) + "   " + mark.Render(ic.passed) + " adds two numbers",
		style.WithBg(style.AccentBold.Render(ic.picked)+"   "+style.Red.Render(ic.failed)+" "+style.AccentText.Render("subtracts")+"     ",
			style.BoldBg(style.Text, style.Cursor)),
		style.Dim.Render(ic.unpicked) + "   " + style.Yellow.Render(ic.skipped) + " divides",
		"",
		style.Yellow.Render(ic.app) + " orders-api " + style.Yellow.Render(ic.warning+" 3") + style.Green.Render(" ●"),
		style.Muted.Render(ic.http) + " wiremock  " + style.Dim.Render("12 req") + style.Green.Render(" ●"),
		style.Muted.Render(ic.database) + " mariadb   " + style.Dim.Render("40 q") + style.Green.Render(" ●"),
		"",
		style.Bar(width-4, 0.7, style.Teal, style.Pass),
	}
	if f.values.LogTime {
		lines = append(lines, style.Dim.Render("12:04:51 ")+style.Green.Render("INFO ")+"started")
	} else {
		lines = append(lines, style.Green.Render("INFO  ")+"started")
	}
	return frame(lines, rect{0, 0, width, max(height, len(lines)+2)}, "Preview", "", "", true)
}

// zones makes the rows clickable, area being where the form was drawn.
func (f *settingsForm) zones(m *Model, area rect) {
	for n, line := range f.lines[f.offset:min(len(f.lines), f.offset+f.shown)] {
		i := line.item
		if i < 0 {
			continue
		}
		m.addZone(zone{r: rect{area.x, area.y + f.rowsAt + n, area.w, 1}, click: func(int, int) tea.Cmd {
			if f.cursor == i {
				f.change(i, 1)
			}
			f.cursor = i
			return nil
		}})
	}
}

// applySettings puts changed settings into effect at once, where they can be.
func (m *Model) applySettings(s settings.Settings) {
	old := m.prefs
	m.prefs = s
	style.ApplyTheme(s.Theme)
	useIcons(s.NerdFont, s.IconSpacing)
	if old.FullLogLines != s.FullLogLines {
		m.logSimple = !s.FullLogLines
	}
	if old.LogTime != s.LogTime {
		m.logTime = s.LogTime
	}
	if old.WrapLogs != s.WrapLogs {
		m.logWrap = s.WrapLogs
		for _, v := range m.logViews() {
			v.wrap = s.WrapLogs
		}
	}
	if old.ShowTestOutput != s.ShowTestOutput {
		m.opts.ShowTestOutput = s.ShowTestOutput
	}
	if !slices.Equal(old.HiddenServices, s.HiddenServices) {
		m.watcher.SetHidden(s.HiddenServices)
	}
	if !slices.Equal(old.Noise, s.Noise) {
		m.setNoise()
	}
	if old.Profile != s.Profile {
		m.profilingChanged()
	}
	m.tree.flatDirty = true
	m.version++
}

// SettingsApp is the settings on their own screen, for testdash --settings.
type SettingsApp struct {
	form          *settingsForm
	width, height int
	zones         []zone
	model         *Model // for zones; never runs
}

func NewSettingsApp(values settings.Settings) *SettingsApp {
	style.ApplyTheme(values.Theme)
	useIcons(values.NerdFont, values.IconSpacing)
	app := &SettingsApp{model: &Model{}}
	app.form = newSettingsForm(values, func(s settings.Settings) {
		style.ApplyTheme(s.Theme)
		useIcons(s.NerdFont, s.IconSpacing)
	})
	return app
}

func (a *SettingsApp) Init() tea.Cmd {
	return nil
}

func (a *SettingsApp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c", "ctrl+q":
			return a, tea.Quit
		}
		a.form.key(msg)
	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		for i := len(a.model.zones) - 1; i >= 0; i-- {
			if z := a.model.zones[i]; z.click != nil && z.r.contains(mouse.X, mouse.Y) {
				return a, z.click(mouse.X-z.r.x, mouse.Y-z.r.y)
			}
		}
	}
	return a, nil
}

func (a *SettingsApp) View() tea.View {
	a.model.zones = a.model.zones[:0]
	width, height := max(40, a.width), max(12, a.height)
	header := " " + style.Gradient(ic.logo+" testdash", true, style.Accent...) + "  " + style.Muted.Render("settings")
	cardWidth := min(100, width-4)
	content := a.form.render(cardWidth-4, height-6)
	framed := frame(content, rect{0, 0, cardWidth, len(content) + 2}, ic.gear+" Settings", "", "", true)
	x := (width - cardWidth) / 2
	screen := block([]string{header, style.LineText.Render(strings.Repeat("─", width))}, width, height-1)
	overlay(screen, framed, x, 3)
	a.form.zones(a.model, rect{x + 2, 4, cardWidth - 4, len(content)})
	footer := " " + keyCap("↑↓") + " " + style.Muted.Render("choose") + "  " + keyCap("←→") + " " +
		style.Muted.Render("change") + "  " + keyCap("esc") + " " + style.Muted.Render("done")
	screen = append(screen, fit(footer, width, true))
	canvas := style.FgBg(style.Text, style.Base)
	for i, line := range screen {
		screen[i] = style.WithBg(line, canvas)
	}
	v := tea.NewView(strings.Join(screen, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor = style.Base
	v.WindowTitle = "testdash settings"
	return v
}
