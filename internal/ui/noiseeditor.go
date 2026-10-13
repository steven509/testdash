package ui

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/noise"
	"github.com/steven509/testdash/internal/style"
)

// noiseItem is something a service listed, as a noise rule sees it.
type noiseItem struct {
	service string // the service's name; gradle for Gradle's output
	text    string
}

// noiseItems are everything the services listed: requests, messages, queries and log lines.
func (m *Model) noiseItems() []noiseItem {
	var items []noiseItem
	for _, line := range m.output.all() {
		items = append(items, noiseItem{"gradle", lineNoiseText(line)})
	}
	for _, entry := range m.svcs {
		key := entry.svc.Key
		switch v := entry.view.(type) {
		case *wiremockView:
			for _, e := range v.events {
				items = append(items, noiseItem{key, requestNoiseText(e)})
			}
		case *kafkaView:
			for _, message := range v.messages {
				items = append(items, noiseItem{key, messageNoiseText(message)})
			}
		case *dbView:
			for _, q := range v.queries {
				items = append(items, noiseItem{key, queryNoiseText(q)})
			}
		}
		for _, line := range entry.lines.all() {
			items = append(items, noiseItem{key, lineNoiseText(line)})
		}
	}
	return items
}

// noiseRef is a rule where it is saved: among the build's rules or every build's, at an index.
type noiseRef struct {
	everyBuild bool
	index      int
}

// noiseEditor makes or changes a noise rule, showing what its pattern matches as it is typed.
type noiseEditor struct {
	services   []string // the choices: a service, and every service ("")
	service    int
	pattern    *lineInput
	everyBuild bool
	field      int       // 0 the service, 1 the pattern, 2 where it is saved
	editing    *noiseRef // the rule changed, nil for a new one
	items      []noiseItem
	shown      noisePreview
}

// noisePreview is what a pattern matched among a service's items, kept while the pattern and service stay.
type noisePreview struct {
	pattern, service string
	matches          []noiseItem
	err              error
}

// hideAsNoise opens the editor on a rule for a service's items like the one given.
func (m *Model) hideAsNoise(service, pattern string) {
	e := &noiseEditor{services: []string{service, ""}, items: m.noiseItems(), field: 1,
		pattern: &lineInput{placeholder: "a regular expression"}}
	if service == "" {
		e.services = []string{""}
	}
	e.pattern.SetValue(pattern)
	m.popup = e
}

// editNoise opens the editor on a saved rule.
func (m *Model) editNoise(ref noiseRef) {
	rules := m.opts.Noise
	if ref.everyBuild {
		rules = m.prefs.Noise
	}
	rule := rules[ref.index]
	m.hideAsNoise(rule.Service, rule.Match)
	e := m.popup.(*noiseEditor)
	e.editing, e.everyBuild = &ref, ref.everyBuild
}

func (e *noiseEditor) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.popup = nil
	case "ctrl+c":
		m.popup = &confirmQuit{}
	case "enter":
		e.save(m)
	case "tab", "down":
		e.field = (e.field + 1) % 3
	case "shift+tab", "up":
		e.field = (e.field + 2) % 3
	case "left", "right", "space":
		switch e.field {
		case 0:
			e.service = (e.service + 1) % len(e.services)
		case 2:
			e.everyBuild = !e.everyBuild
		default:
			e.pattern.key(k)
		}
	default:
		if e.field == 1 {
			e.pattern.key(k)
		}
	}
	return nil
}

func (e *noiseEditor) save(m *Model) {
	rule := noise.Rule{Service: e.services[e.service], Match: e.pattern.Value()}
	if _, err := regexp.Compile(rule.Match); err != nil || rule.Match == "" {
		e.field = 1
		return
	}
	build, everyBuild := append([]noise.Rule(nil), m.opts.Noise...), append([]noise.Rule(nil), m.prefs.Noise...)
	if e.editing != nil {
		if e.editing.everyBuild {
			everyBuild = append(everyBuild[:e.editing.index], everyBuild[e.editing.index+1:]...)
		} else {
			build = append(build[:e.editing.index], build[e.editing.index+1:]...)
		}
	}
	if e.everyBuild {
		everyBuild = append(everyBuild, rule)
	} else {
		build = append(build, rule)
	}
	if err := m.saveNoise(build, everyBuild); err != nil {
		m.notify(err.Error(), "Could not save the noise rule", "error")
		return
	}
	m.popup = nil
	m.notify(fmt.Sprintf("Hiding %s as noise", count(len(e.preview().matches), "item")), "", "success")
}

// saveNoise keeps the build's rules and every build's, and puts them in force.
func (m *Model) saveNoise(build, everyBuild []noise.Rule) error {
	if !rulesEqual(build, m.opts.Noise) {
		if err := noise.SaveConfig(m.opts.ConfigPath, build); err != nil {
			return err
		}
		m.opts.Noise = build
	}
	if !rulesEqual(everyBuild, m.prefs.Noise) {
		prefs := m.prefs
		prefs.Noise = everyBuild
		if err := prefs.Save(); err != nil {
			return err
		}
		m.prefs = prefs
	}
	m.setNoise()
	return nil
}

func rulesEqual(a, b []noise.Rule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// preview is what the pattern matches among the chosen service's items, kept until either changes.
func (e *noiseEditor) preview() *noisePreview {
	pattern, service := e.pattern.Value(), e.services[e.service]
	if e.shown.pattern == pattern && e.shown.service == service && (e.shown.matches != nil || e.shown.err != nil) {
		return &e.shown
	}
	e.shown.pattern, e.shown.service, e.shown.matches, e.shown.err = pattern, service, []noiseItem{}, nil
	re, err := regexp.Compile(pattern)
	if err != nil || pattern == "" {
		e.shown.err = err
		return &e.shown
	}
	for _, item := range e.items {
		if (service == "" || item.service == service) && re.MatchString(item.text) {
			e.shown.matches = append(e.shown.matches, item)
		}
	}
	return &e.shown
}

func (e *noiseEditor) render(m *Model, screen []string) {
	width := min(100, m.width-4)
	inner := width - 4
	field := func(i int, label, value string) string {
		mark := " "
		if i == e.field {
			mark = style.AccentBold.Render("▌")
		}
		return mark + style.Muted.Render(fit(label, 11, true)) + value
	}
	choice := func(i int, value string) string {
		if i == e.field {
			return style.Dim.Render("‹ ") + style.BoldPill(value, style.Base, style.Primary) + style.Dim.Render(" ›")
		}
		return "  " + style.Pill(value, style.Text, style.Raised)
	}
	service := e.services[e.service]
	if service == "" {
		service = "every service"
	}
	saved := "this build: " + tildePath(m.opts.ConfigPath)
	if e.everyBuild {
		saved = "every build: the settings"
	}
	pattern := typed(e.pattern)
	if e.field != 1 {
		pattern = style.Cyan.Render(e.pattern.Value())
	}
	content := []string{
		field(0, "Service", choice(0, service)),
		field(1, "Pattern", pattern),
		field(2, "Saved for", choice(2, saved)),
		"",
	}
	p := e.preview()
	switch {
	case p.err != nil:
		content = append(content, style.Red.Render(cleanText(p.err.Error())))
	case p.pattern == "":
		content = append(content, style.Muted.Render("A regular expression over each item's text: a query, a request's "+
			"method and URL, a message's topic, key and value, or a log line"))
	default:
		total := 0
		for _, item := range e.items {
			if e.services[e.service] == "" || item.service == e.services[e.service] {
				total++
			}
		}
		content = append(content, style.Bold.Render("Matches "+number(len(p.matches)))+style.Dim.Render(" of "+
			number(total)+" now"))
		re := regexp.MustCompile(p.pattern)
		for i, item := range p.matches {
			if i == 8 {
				content = append(content, style.Dim.Render(fmt.Sprintf("  and %s more", number(len(p.matches)-8))))
				break
			}
			content = append(content, "  "+style.Dim.Render(fit(item.service, 10, true))+" "+highlighted(item.text, re, inner-14))
		}
	}
	content = append(content, "", style.Dim.Render("enter saves · tab moves · ← → changes a choice · esc cancels"))
	title := "Hide as noise"
	if e.editing != nil {
		title = "Noise rule"
	}
	m.dialog(screen, content, width, title, -1)
}

// highlighted is an item's first line, with what a pattern matches in it marked.
func highlighted(text string, re *regexp.Regexp, width int) string {
	line, _, _ := strings.Cut(cleanText(text), "\n")
	span := re.FindStringIndex(line)
	if span == nil {
		return fit(style.Muted.Render(line), width, true)
	}
	return fit(style.Muted.Render(line[:span[0]])+style.AccentBold.Render(line[span[0]:span[1]])+
		style.Muted.Render(line[span[1]:]), width, true)
}
