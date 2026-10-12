// Package ui is testdash's terminal interface: the state both screens show (tests, selection, run progress,
// services, output), the work behind them (listing tests, running them, following Docker), and the screens.
//
//	picker ──Enter on Run──▶ run screen: overview ⇄ tests, output, each service (sidebar, 1-9)
//	       ◀──────Esc───────            ◀──Esc── from any other view
//
// Everything the screens show lives on the Model and changes only in Update. Work that waits (Gradle, Docker,
// the services' own tools) runs in goroutines that send their results in as messages.
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/catalog"
	"testdash/internal/databases"
	"testdash/internal/gradle"
	"testdash/internal/logformat"
	"testdash/internal/progress"
	"testdash/internal/runner"
	"testdash/internal/services"
	"testdash/internal/style"
)

const (
	correlationSlack = time.Second // either side of a test, for clocks that differ a little
	maxOutputLines   = 50_000
	maxServiceLines  = 50_000
	tickInterval     = 500 * time.Millisecond
)

var notSlug = regexp.MustCompile(`[^A-Za-z0-9_-]`)

var portTaken = regexp.MustCompile(`Bind for \S*:(\d+) failed: port is already allocated` +
	`|listen tcp\d? \S*:(\d+): bind: address already in use`)

type Settings struct {
	Workdir              string   // where the command runs
	BuildRoot            string   // the Gradle build, when there is one
	Command              []string // an explicit or configured command; nil runs the picked Gradle task
	ExtraArgs            []string
	Task                 string
	ShowTestOutput       bool
	IncludeAllContainers bool
	RunDir               string
	ExtraLogs            []ExtraLog // from .testdash.json
}

// ExtraLog is a log from .testdash.json: a file, or a command's output.
type ExtraLog struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Command string `json:"command"`
}

// window is when a test (or a class's tests) ran, for the views to show that time: "during <test>".
type window struct {
	start, end time.Time
	label      string
}

func (w *window) holds(at time.Time) bool {
	return w == nil || (!at.Before(w.start) && !at.After(w.end))
}

// lineBuffer keeps the latest lines of a log.
type lineBuffer struct {
	lines []*logformat.Line
	max   int
}

func (b *lineBuffer) add(lines ...*logformat.Line) {
	b.lines = append(b.lines, lines...)
	if len(b.lines) > b.max+b.max/4 {
		b.lines = append([]*logformat.Line(nil), b.lines[len(b.lines)-b.max:]...)
	}
}

func (b *lineBuffer) all() []*logformat.Line {
	if len(b.lines) > b.max {
		return b.lines[len(b.lines)-b.max:]
	}
	return b.lines
}

func (b *lineBuffer) clear() {
	b.lines = nil
}

// svcEntry is a service and what the UI keeps for it: its log, its problems, its views.
type svcEntry struct {
	svc      *services.Service
	slug     string
	viewID   string
	lines    *lineBuffer
	problems int // warnings and errors logged this run, counted as lines arrive
	full     view
	ovLog    *logView
}

type screenID int

const (
	pickerScreen screenID = iota
	runScreen
)

type Model struct {
	ctx      context.Context
	send     func(tea.Msg)
	settings Settings

	catalog       *catalog.Catalog
	testTask      *catalog.TestTask
	entries       map[string]*catalog.Entry
	kids          map[string][]*catalog.Entry
	selectable    map[string]bool
	selected      map[string]bool
	lastSelection map[string]bool
	runner        *runner.Runner
	discovery     *runner.Runner
	discovering   bool
	progress      *progress.Progress
	inRun         map[string]bool // nil before the first run
	output        *lineBuffer
	runStarted    time.Time
	runFinished   time.Time
	exitCode      int
	listening     bool
	claims        *services.Claims
	watcher       *services.Watcher
	gen           int
	svcs          []*svcEntry
	logSimple     bool // every log shows a line's level and message only
	logTime       bool
	portConflict  string // why the run could not start its containers
	explained     map[string]bool

	width, height int
	spinner       spinner.Model
	spinning      bool
	screen        screenID
	picker        *pickerState
	run           *runState
	toasts        []*toast
	toastSeq      int
	palette       *palette
	zones         []zone
	dirty         bool
	frame         string
}

func New(ctx context.Context, settings Settings) *Model {
	m := &Model{
		ctx: ctx, settings: settings, send: func(tea.Msg) {},
		selected: map[string]bool{}, lastSelection: map[string]bool{}, selectable: map[string]bool{},
		runner: &runner.Runner{}, discovery: &runner.Runner{},
		output: &lineBuffer{max: maxOutputLines}, claims: &services.Claims{}, logSimple: true,
		explained: map[string]bool{}, dirty: true,
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	if settings.BuildRoot != "" {
		m.catalog = catalog.Load(settings.BuildRoot)
	}
	var scope []string
	for _, dir := range []string{settings.Workdir, settings.BuildRoot} {
		if dir != "" {
			scope = append(scope, dir)
		}
	}
	m.watcher = services.NewWatcher(ctx, func(msg any) { m.send(msg) }, time.Now(), scope,
		settings.IncludeAllContainers, m.claims)
	m.picker = newPickerState()
	m.run = newRunState(m)
	m.chooseTask(settings.Task, true)
	return m
}

// SetSend gives the model the program's Send, for its goroutines.
func (m *Model) SetSend(send func(tea.Msg)) {
	m.send = send
}

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick()}
	if m.settings.BuildRoot != "" && (m.settings.Command == nil || m.commandIsGradle()) {
		cmds = append(cmds, m.discover())
	}
	if services.Available() {
		go m.watcher.Run()
	} else {
		m.notify("docker is not on PATH: no service views", "", "warning")
	}
	for _, extra := range m.settings.ExtraLogs {
		m.followExtra(extra)
	}
	return tea.Batch(cmds...)
}

// Shutdown stops what testdash started, once the program has ended.
func (m *Model) Shutdown() {
	m.runner.Kill()
	m.discovery.Kill()
	_ = os.RemoveAll(m.settings.RunDir)
}

// the tests

func (m *Model) chooseTask(wanted string, quiet bool) {
	tasks := map[string]*catalog.TestTask{}
	var paths []string
	if m.catalog != nil {
		tasks, paths = m.catalog.Tasks, m.catalog.Paths
	}
	path := ""
	for _, candidate := range []string{wanted, m.taskInCommand(), savedTask(m.settings.BuildRoot), ":test", ":app:test"} {
		if candidate != "" && tasks[candidate] != nil {
			path = candidate
			break
		}
	}
	if path == "" && len(paths) > 0 {
		path = paths[0]
	}
	changed := m.testTask == nil || m.testTask.Path != path
	m.testTask = tasks[path]
	if m.testTask != nil && m.settings.BuildRoot != "" {
		saveTask(m.settings.BuildRoot, m.testTask.Path)
	}
	m.index()
	for id := range m.selected {
		if !m.selectable[id] {
			delete(m.selected, id)
		}
	}
	if changed && !quiet && m.testTask != nil {
		m.notify(fmt.Sprintf("%s: %d tests", m.testTask.Path, len(m.selectable)), "", "information")
	}
	m.rebuildTrees(true)
}

// index builds the tree both screens show: the task's tests, plus the tests the current run created.
func (m *Model) index() {
	var taskEntries []*catalog.Entry
	if m.testTask != nil {
		taskEntries = m.testTask.Entries
	}
	m.entries = map[string]*catalog.Entry{}
	m.selectable = map[string]bool{}
	var order []*catalog.Entry
	for _, e := range taskEntries {
		if _, seen := m.entries[e.ID]; !seen {
			order = append(order, e)
		}
		m.entries[e.ID] = e
		if e.IsLeaf() {
			m.selectable[e.ID] = true
		}
	}
	if m.progress != nil {
		for _, e := range m.progress.Registered {
			if _, ok := m.entries[e.Parent]; ok {
				if _, seen := m.entries[e.ID]; !seen {
					m.entries[e.ID] = e
					order = append(order, e)
				}
			}
		}
	}
	m.kids = map[string][]*catalog.Entry{}
	for _, e := range order {
		e = m.entries[e.ID]
		parent := e.Parent
		if _, ok := m.entries[parent]; !ok {
			parent = ""
		}
		m.kids[parent] = append(m.kids[parent], e)
	}
}

func (m *Model) status(id string) string {
	if m.progress == nil || m.inRun == nil {
		return "idle"
	}
	entry := m.entries[id]
	if !m.inRun[id] && !(entry != nil && m.inRun[entry.Parent]) {
		return "absent"
	}
	return m.progress.State(id).Status
}

func (m *Model) state(id string) progress.State {
	if m.progress == nil {
		return progress.State{Status: "pending"}
	}
	return m.progress.State(id)
}

func (m *Model) leavesUnder(id string) []string {
	var leaves []string
	for _, kid := range m.kids[id] {
		if m.selectable[kid.ID] {
			leaves = append(leaves, kid.ID)
		} else if kid.Kind == "class" {
			leaves = append(leaves, m.leavesUnder(kid.ID)...)
		}
	}
	return leaves
}

// selectionFor is what runs one tree node again: a test, a class's tests, or for a parameterised test's
// invocation, which Gradle cannot filter on, the whole parameterised test.
func (m *Model) selectionFor(id string) map[string]bool {
	entry := m.entries[id]
	for entry != nil && !m.selectable[entry.ID] && entry.Kind != "class" {
		entry = m.entries[entry.Parent]
	}
	selection := map[string]bool{}
	if entry == nil {
		return selection
	}
	if m.selectable[entry.ID] {
		selection[entry.ID] = true
		return selection
	}
	for _, leaf := range m.leavesUnder(entry.ID) {
		selection[leaf] = true
	}
	return selection
}

// windowFor is when a test (or a class's tests) ran, with a second either side.
func (m *Model) windowFor(id string) *window {
	state := m.state(id)
	if state.StartedAt == 0 {
		return nil
	}
	end := state.FinishedAt
	if end == 0 {
		end = nowMillis()
	}
	name := id
	if entry := m.entries[id]; entry != nil {
		name = entry.Name
	}
	return &window{start: time.UnixMilli(state.StartedAt).Add(-correlationSlack),
		end: time.UnixMilli(end).Add(correlationSlack), label: "during " + name}
}

func (m *Model) descendants(id string) []string {
	var found []string
	for _, kid := range m.kids[id] {
		found = append(found, kid.ID)
		found = append(found, m.descendants(kid.ID)...)
	}
	return found
}

// failedIDs are the task's tests that failed last run; a failed parameterised case counts as its test.
func (m *Model) failedIDs() map[string]bool {
	failed := map[string]bool{}
	for id, entry := range m.entries {
		if s := m.status(id); s == "failed" || s == "aborted" {
			target := id
			if !m.selectable[id] {
				target = entry.Parent
			}
			if m.selectable[target] {
				failed[target] = true
			}
		}
	}
	return failed
}

func (m *Model) noTaskReason() string {
	switch {
	case m.settings.Command != nil && !m.commandIsGradle():
		return "\nNot a Gradle command, so there is no test list:\nRun runs the command."
	case m.discovering:
		return "\nListing the build's tests with Gradle…"
	case m.settings.BuildRoot == "":
		return "\nNo Gradle build here and no command:\npass one after --"
	}
	return "\nNo tests found (List tests again in ^p)"
}

func (m *Model) describeTest(id string, brief bool) string {
	entry := m.entries[id]
	if entry == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(style.Bold.Render(entry.ClassName) + "\n")
	if entry.Kind == "class" {
		leaves := m.leavesUnder(id)
		picked := 0
		counts := map[string]int{}
		for _, leaf := range leaves {
			if m.selected[leaf] {
				picked++
			}
			counts[m.status(leaf)]++
		}
		fmt.Fprintf(&b, "%d tests, %d selected\n", len(leaves), picked)
		for _, status := range []string{"passed", "failed", "skipped", "running", "pending"} {
			if counts[status] > 0 {
				mark, s := icon(status)
				b.WriteString(s.Render(fmt.Sprintf("%s %d %s", mark, counts[status], status)) + "  ")
			}
		}
		return b.String()
	}
	b.WriteString(cleanText(entry.Name) + "\n")
	status, state := m.status(id), m.state(id)
	if status == "idle" {
		return b.String() + style.Dim.Render("not run yet")
	}
	if status == "absent" {
		return b.String() + style.Dim.Render("not in the last run")
	}
	mark, s := icon(status)
	b.WriteString(s.Render(mark + " " + status))
	now := nowMillis()
	if state.StartedAt != 0 {
		took, _ := state.Took(now)
		b.WriteString(style.Dim.Render("  "+duration(took)+"  "+clock(time.UnixMilli(state.StartedAt))) + "\n")
	}
	if state.Message != "" {
		message := cleanBlock(state.Message)
		if brief {
			message = strings.Join(firstN(strings.Split(message, "\n"), 6), "\n")
		}
		messageStyle := style.Red
		if state.Status == "skipped" {
			messageStyle = style.Yellow
		}
		b.WriteString("\n" + styleLines(message, messageStyle) + "\n")
	}
	if state.StartedAt != 0 && !brief {
		end := state.FinishedAt
		if end == 0 {
			end = now
		}
		b.WriteString("\n" + m.correlate(time.UnixMilli(state.StartedAt), time.UnixMilli(end)))
	}
	return b.String()
}

// correlate is what WireMock received, what Kafka carried, what the databases were asked and what the
// application warned about while a test ran.
func (m *Model) correlate(start, end time.Time) string {
	w := &window{start: start.Add(-correlationSlack), end: end.Add(correlationSlack)}
	var b strings.Builder
	for _, entry := range m.svcs {
		if !entry.svc.IsApp() && entry.svc.Kind != "wiremock" && entry.svc.Kind != "kafka" &&
			!databases.QueryKinds[entry.svc.Kind] {
			continue
		}
		key := entry.svc.Key
		switch view := entry.full.(type) {
		case *wiremockView:
			events := view.eventsBetween(w)
			unmatched := 0
			for _, e := range events {
				if !e.Matched {
					unmatched++
				}
			}
			header := fmt.Sprintf("%s: %d requests", key, len(events))
			if unmatched > 0 {
				header += fmt.Sprintf(", %d unmatched", unmatched)
			}
			b.WriteString(style.Bold.Render(header) + "\n")
			for _, e := range sortedByTime(events) {
				line := fmt.Sprintf("  %s %d %s %s", clock(e.At), e.Status, e.Method, cleanText(e.URL))
				if e.Matched {
					b.WriteString(line + "\n")
				} else {
					b.WriteString(style.Red.Render(line+"  no stub matched") + "\n")
				}
			}
		case *kafkaView:
			messages := view.messagesBetween(w)
			b.WriteString(style.Bold.Render(fmt.Sprintf("%s: %d messages", key, len(messages))) + "\n")
			for _, message := range lastN(messages, 15) {
				messageKey := ""
				if message.Key != nil {
					messageKey = cleanText(*message.Key)
				}
				b.WriteString(fmt.Sprintf("  %s %s", clock(message.At), cleanText(message.Topic)) +
					style.Dim.Render("  "+messageKey) + "\n")
			}
		case *dbView:
			queries := view.queriesBetween(w)
			b.WriteString(style.Bold.Render(fmt.Sprintf("%s: %d queries", key, len(queries))) + "\n")
			for _, q := range lastN(queries, 10) {
				target := ""
				if q.Target != "" {
					target = q.Target + " "
				}
				flat := []rune(strings.Join(strings.Fields(cleanText(q.Text)), " "))
				b.WriteString("  " + clock(q.At) + " " + style.Dim.Render(target+string(flat[:min(100, len(flat))])) + "\n")
			}
		default:
			if !entry.svc.IsApp() {
				continue
			}
			var problems []*logformat.Line
			for _, line := range entry.lines.all() {
				if w.holds(line.At) && !line.At.IsZero() && line.IsLoggedProblem() {
					problems = append(problems, line)
				}
			}
			b.WriteString(style.Bold.Render(fmt.Sprintf("%s: %d warnings or errors", key, len(problems))) + "\n")
			for _, line := range firstN(problems, 25) {
				b.WriteString(line.Render(m.logSimple, true) + "\n")
			}
		}
	}
	if b.Len() == 0 {
		return style.Dim.Render("No services seen while it ran")
	}
	return b.String()
}

type outputMsg struct{ lines []string }

type discoveredMsg struct {
	code      int
	outputDir string
	err       error
}

func (m *Model) discover() tea.Cmd {
	root := m.settings.BuildRoot
	if root == "" {
		return nil
	}
	if m.discovering {
		m.notify("Already listing the tests", "", "information")
		return nil
	}
	outputDir := filepath.Join(m.settings.RunDir, "discovery")
	_ = os.RemoveAll(outputDir)
	_ = os.MkdirAll(outputDir, 0o755)
	m.discovering = true
	m.addOutput("── listing tests with Gradle ──", "bold")
	send := m.send
	discovery := m.discovery
	return tea.Batch(m.spin(), func() tea.Msg {
		command, err := gradle.DiscoverCommand(filepath.Join(root, "gradlew"), outputDir)
		if err != nil {
			return discoveredMsg{err: err}
		}
		code := discovery.Run(command, root, func(lines []string) { send(outputMsg{lines}) })
		return discoveredMsg{code: code, outputDir: outputDir}
	})
}

func (m *Model) discovered(msg discoveredMsg) {
	m.discovering = false
	if msg.err != nil {
		m.notify("Cannot list tests: "+msg.err.Error(), "", "error")
		return
	}
	m.catalog.UpdateFromDiscovery(msg.outputDir)
	current := m.settings.Task
	if m.testTask != nil {
		current = m.testTask.Path
	}
	m.chooseTask(current, true)
	if msg.code != 0 && len(m.catalog.Tasks) == 0 {
		m.notify("Listing the tests failed: see Output on the run screen", "", "error")
	}
}

func (m *Model) taskInCommand() string {
	if len(m.settings.Command) == 0 || m.catalog == nil {
		return ""
	}
	for _, argument := range m.settings.Command[1:] {
		for _, path := range m.catalog.Paths {
			if argument == path || argument == path[strings.LastIndex(path, ":")+1:] {
				return path
			}
		}
	}
	return ""
}

func (m *Model) commandIsGradle() bool {
	return len(m.settings.Command) > 0 && gradle.IsGradle(m.settings.Command)
}

// running

func (m *Model) baseCommand() []string {
	if m.settings.Command != nil {
		return m.settings.Command
	}
	if m.testTask != nil && m.settings.BuildRoot != "" {
		return []string{filepath.Join(m.settings.BuildRoot, "gradlew"), m.testTask.Path}
	}
	return nil
}

type runDoneMsg struct{ code int }

type listeningMsg struct{ on bool }

// startRun runs the selection, or the picker's selection when nil.
func (m *Model) startRun(selection map[string]bool) tea.Cmd {
	if m.runner.Running() {
		m.notify("Already running: stop it first (x, or ctrl+x in the picker)", "", "warning")
		return nil
	}
	base := m.baseCommand()
	if base == nil {
		m.notify("Nothing to run: no Gradle test task and no command", "", "warning")
		return nil
	}
	if selection == nil {
		selection = m.selected
	}
	picked := map[string]bool{}
	for id := range selection {
		if m.selectable[id] {
			picked[id] = true
		}
	}
	m.lastSelection = picked
	if m.discovering {
		m.discovery.Stop()
	}
	command := append(append([]string{}, base...), m.settings.ExtraArgs...)
	if len(picked) > 0 && m.testTask != nil && gradle.IsGradle(command) {
		command = append(command, catalog.GradleFilters(m.testTask, picked)...)
	}
	shown := strings.Join(command, " ")
	m.listening = gradle.IsGradle(command)
	if files, _ := filepath.Glob(filepath.Join(m.settings.RunDir, "progress-*.jsonl")); len(files) > 0 {
		for _, stale := range files {
			_ = os.Remove(stale)
		}
	}
	m.runStarted, m.runFinished, m.exitCode = time.Now(), time.Time{}, 0
	m.portConflict, m.explained = "", map[string]bool{}
	m.progress = progress.New(m.settings.RunDir)
	m.inRun = map[string]bool{}
	m.index()
	m.updateClaims()
	m.gen = m.watcher.NewRun(m.runStarted)
	for _, entry := range m.svcs {
		entry.lines.clear()
		entry.problems = 0
		entry.full.clear()
		entry.ovLog.clear()
	}
	m.output.clear()
	m.screen = runScreen
	m.run.outputView.clear()
	m.run.ovOutput.clear()
	m.run.showView(m, "overview", true)
	m.addOutput("▶ "+shown, "bold")
	m.rebuildTrees(true)
	spin := m.spin()

	send, runDir, quiet := m.send, m.settings.RunDir, !m.settings.ShowTestOutput
	workdir, run := m.settings.Workdir, m.runner
	listening := m.listening
	go func() {
		if listening {
			withListener, err := gradle.WithListener(command, runDir, quiet)
			if err != nil {
				send(notifyMsg{text: "Running without test progress: " + err.Error(), severity: "warning"})
				send(listeningMsg{false})
			} else {
				command = withListener
			}
		}
		code := run.Run(command, workdir, func(lines []string) { send(outputMsg{lines}) })
		send(runDoneMsg{code})
	}()
	return spin
}

// updateClaims tells the watcher what the run's tests said about their containers.
func (m *Model) updateClaims() {
	state := services.ClaimState{Listening: m.listening,
		RunActive: !m.runStarted.IsZero() && m.runFinished.IsZero()}
	if m.progress != nil {
		state.Testcontainers, state.Labels = m.progress.Testcontainers, m.progress.ContainerLabels
	}
	m.claims.Set(state)
}

type portHolderMsg struct{ port, holder string }

// checkPorts notices Docker failing to publish a port and says who has it, as it reads like a test failure
// otherwise.
func (m *Model) checkPorts(text string) tea.Cmd {
	if !strings.Contains(text, "already") && !strings.Contains(text, "in use") {
		return nil
	}
	var cmds []tea.Cmd
	for _, match := range portTaken.FindAllStringSubmatch(text, -1) {
		port := match[1]
		if port == "" {
			port = match[2]
		}
		if m.explained[port] {
			continue
		}
		m.explained[port] = true
		ctx := m.ctx
		cmds = append(cmds, func() tea.Msg {
			return portHolderMsg{port: port, holder: services.PortHolder(ctx, port)}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Model) explainPort(msg portHolderMsg) {
	holder := ""
	if msg.holder != "" {
		holder = " by " + msg.holder
	}
	m.portConflict = style.Red.Render("✘ ") + style.BoldRed.Render("Port "+msg.port+" is taken") + holder +
		". Another run is probably using it: wait for it to finish, or stop it, then run again."
	m.notifyFor(cleanText(m.portConflict), "The run could not start its containers", "error", 20*time.Second)
}

func (m *Model) runDone(code int) tea.Cmd {
	m.exitCode, m.runFinished = code, time.Now()
	cmd := m.tick()
	took := duration(m.runFinished.Sub(m.runStarted).Milliseconds())
	summary := m.runSummary()
	if code == 0 {
		m.addOutput("passed in "+took, "green")
		m.notify(fmt.Sprintf("Passed: %d tests in %s", summary.passed, took), "", "information")
	} else {
		m.addOutput(fmt.Sprintf("failed (exit %d) in %s", code, took), "red")
		m.notify(fmt.Sprintf("Failed: %d of %d tests failed (exit %d)", summary.failed, summary.total, code), "", "error")
	}
	m.updateClaims()
	return cmd
}

func (m *Model) stop() {
	if m.runner.Running() {
		m.runner.Stop()
		m.notify("Stopping: x again kills it", "", "information")
	} else if m.discovering {
		m.discovery.Stop()
	}
}

var outputStyles = map[string]string{"bold": "1", "green": "32", "red": "31"}

func (m *Model) addOutput(text, styleName string) tea.Cmd {
	cmd := m.checkPorts(text)
	if code, ok := outputStyles[styleName]; ok {
		text = "\x1b[" + code + "m" + text + "\x1b[0m"
	}
	line := logformat.NewLine(time.Now(), text)
	m.output.add(line)
	m.run.outputView.append(m, []*logformat.Line{line})
	m.run.ovOutput.append(m, []*logformat.Line{line})
	return cmd
}

func (m *Model) addOutputLines(texts []string) tea.Cmd {
	var cmds []tea.Cmd
	lines := make([]*logformat.Line, 0, len(texts))
	now := time.Now()
	for _, text := range texts {
		cmds = append(cmds, m.checkPorts(text))
		lines = append(lines, logformat.NewLine(now, text))
	}
	m.output.add(lines...)
	m.run.outputView.append(m, lines)
	m.run.ovOutput.append(m, lines)
	return tea.Batch(cmds...)
}

func (m *Model) outputInfo() string {
	return style.Bold.Render(cleanText(strings.Join(m.baseCommand(), " "))) + style.Dim.Render("   "+m.settings.Workdir)
}

// what the screens summarise

type runEntry struct {
	entry *catalog.Entry
	state progress.State
}

type summary struct {
	total, passed, failed, skipped, left int
	running                              []runEntry
	recent                               []runEntry // finished, the failures first, latest first
}

func (m *Model) runLeaves() []*catalog.Entry {
	if m.progress == nil {
		return nil
	}
	expanded := map[string]bool{}
	for _, e := range m.progress.Registered {
		expanded[e.Parent] = true
	}
	var leaves []*catalog.Entry
	for _, e := range m.progress.Planned {
		if e.IsLeaf() && !expanded[e.ID] {
			leaves = append(leaves, e)
		}
	}
	return append(leaves, m.progress.Registered...)
}

func (m *Model) runSummary() summary {
	var s summary
	var finished []runEntry
	for _, entry := range m.runLeaves() {
		state := m.state(entry.ID)
		s.total++
		switch state.Status {
		case "passed":
			s.passed++
		case "failed", "aborted":
			s.failed++
		case "skipped":
			s.skipped++
		case "pending", "running":
			s.left++
		}
		if state.Status == "running" {
			s.running = append(s.running, runEntry{entry, state})
		}
		if state.FinishedAt != 0 {
			finished = append(finished, runEntry{entry, state})
		}
	}
	sortByFinished(finished)
	for _, failedFirst := range []bool{true, false} {
		for _, r := range finished {
			if (r.state.Status == "failed" || r.state.Status == "aborted") == failedFirst {
				s.recent = append(s.recent, r)
			}
		}
	}
	return s
}

func (m *Model) elapsed() string {
	end := m.runFinished
	if end.IsZero() {
		end = time.Now()
	}
	return duration(end.Sub(m.runStarted).Milliseconds())
}

// stateText is how the run is going, inline.
func (m *Model) stateText() string {
	switch {
	case m.runStarted.IsZero():
		return style.Dim.Render("no run yet")
	case m.runFinished.IsZero():
		return style.Cyan.Render(m.spinnerFrame() + " running " + m.elapsed())
	case m.exitCode == 0:
		return style.Green.Render("✔ passed in " + m.elapsed())
	}
	return style.Red.Render(fmt.Sprintf("✘ failed in %s (exit %d)", m.elapsed(), m.exitCode))
}

// stateBadge is how the run is going, as a pill.
func (m *Model) stateBadge() string {
	switch {
	case m.runStarted.IsZero():
		return style.Pill("no run yet", style.SubtleText, style.Raised)
	case m.runFinished.IsZero():
		return style.BoldPill(m.spinnerFrame()+" RUNNING "+m.elapsed(), style.Base, style.Run)
	case m.exitCode == 0:
		return style.BoldPill("✔ PASSED "+m.elapsed(), style.Base, style.Pass)
	}
	return style.BoldPill(fmt.Sprintf("✘ FAILED %s · exit %d", m.elapsed(), m.exitCode), style.Base, style.Fail)
}

// activity is what a run is doing when no test is running: building, setting up a class, or finishing.
func (m *Model) activity() string {
	var settingUp *catalog.Entry
	for _, e := range m.allEntries() {
		if e.Kind == "class" && m.state(e.ID).Status == "running" {
			settingUp = e
		}
	}
	if settingUp != nil {
		return style.Cyan.Render(m.spinnerFrame() + " setting up " + settingUp.ShortClass())
	}
	if len(m.runLeaves()) > 0 {
		return style.Cyan.Render(m.spinnerFrame() + " finishing…")
	}
	return style.Cyan.Render(m.spinnerFrame() + " building…")
}

// allEntries is every entry in tree order.
func (m *Model) allEntries() []*catalog.Entry {
	var all []*catalog.Entry
	var walk func(parent string)
	walk = func(parent string) {
		for _, kid := range m.kids[parent] {
			all = append(all, kid)
			walk(kid.ID)
		}
	}
	walk("")
	return all
}

// statusText is the status bar's two lines: the test running, then the counts and how the run is going.
func (m *Model) statusText() []string {
	if m.runStarted.IsZero() {
		return []string{style.Muted.Render("No run yet: select tests, then ") + style.AccentBold.Render("Enter") +
			style.Muted.Render(" twice to run them"), ""}
	}
	s := m.runSummary()
	var first string
	switch {
	case len(s.running) > 0:
		r := s.running[0]
		name := r.entry.Method
		if name == "" {
			name = r.entry.Name
		}
		first = style.Cyan.Render(m.spinnerFrame()+" ") + style.BoldCyan.Render(cleanText(name))
		if r.entry.Method != "" && r.entry.Name != r.entry.Method {
			first += style.Cyan.Render("  " + cleanText(r.entry.Name))
		}
		took, _ := r.state.Took(nowMillis())
		first += style.Dim.Render("  in ") + style.Muted.Render(m.groupOf(r.entry)) + style.Dim.Render("  "+duration(took))
		if len(s.running) > 1 {
			first += style.Dim.Render(fmt.Sprintf("  +%d more running", len(s.running)-1))
		}
	case m.portConflict != "":
		first = m.portConflict
	case m.runFinished.IsZero():
		first = m.activity()
	default:
		first = style.Muted.Render("Run finished")
	}
	return []string{first, m.countPills(s) + "  " + m.stateBadge()}
}

// countPills are the run's counts as pills, lit when not zero, and its progress.
func (m *Model) countPills(s summary) string {
	var b strings.Builder
	for _, part := range []struct {
		n      int
		label  string
		status string
		color  color.Color
	}{{s.passed, "passed", "passed", style.Pass}, {s.failed, "failed", "failed", style.Fail},
		{s.skipped, "skipped", "skipped", style.Warn}, {s.left, "left", "pending", style.MutedText}} {
		mark, _ := icon(part.status)
		text := fmt.Sprintf("%s %d %s", mark, part.n, part.label)
		if part.n > 0 {
			b.WriteString(style.Pill(text, part.color, style.Tint(part.color)))
		} else {
			b.WriteString(style.Pill(text, style.SubtleText, style.Raised))
		}
		b.WriteString(" ")
	}
	done := s.passed + s.failed + s.skipped
	fraction := 0.0
	if s.total > 0 {
		fraction = float64(done) / float64(s.total)
	}
	b.WriteString(" " + style.Bar(20, fraction, m.barColors(s)...) + style.Muted.Render(fmt.Sprintf(" %d/%d", done, s.total)))
	return b.String()
}

// barColors are a progress bar's gradient: green to teal while all is well, amber to red once a test failed.
func (m *Model) barColors(s summary) []color.Color {
	if s.failed > 0 {
		return []color.Color{style.Warn, style.Fail}
	}
	return []color.Color{style.Teal, style.Pass}
}

// groupOf is the classes a test sits in, outermost first, by their display names: Outer › Nested.
func (m *Model) groupOf(entry *catalog.Entry) string {
	var names []string
	for parent := m.entries[entry.Parent]; parent != nil; parent = m.entries[parent.Parent] {
		if parent.Kind == "class" {
			names = append([]string{cleanText(parent.Name)}, names...)
		}
	}
	if len(names) == 0 {
		return entry.ShortClass()
	}
	return strings.Join(names, " › ")
}

// titleBar is the top line: the wordmark, the repo and task, how the run is going, where you are, and a hint.
func (m *Model) titleBar(place, hint string) string {
	left := " " + style.Gradient("◆ testdash", true, style.Primary, style.Pink) + "  " +
		style.Muted.Render(filepath.Base(m.settings.Workdir)) + "  "
	if m.testTask != nil && m.settings.Command == nil {
		left += style.Pill(m.testTask.Path, style.Teal, style.Raised) + "  "
	}
	left += m.stateBadge()
	if place != "" {
		left += "  " + style.AccentText.Render("›") + " " + style.Bold.Render(place)
	}
	right := style.Dim.Render(hint) + " "
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	line := left
	if gap > 1 {
		line += strings.Repeat(" ", gap) + right
	}
	return style.WithBg(fit(line, m.width, true), style.FgBg(style.Text, style.Surface))
}

// services (the watcher's messages)

func (m *Model) serviceEntry(service *services.Service) *svcEntry {
	for _, entry := range m.svcs {
		if entry.svc == service {
			return entry
		}
	}
	return nil
}

func (m *Model) serviceAdded(service *services.Service) {
	if m.serviceEntry(service) != nil {
		return
	}
	slug := notSlug.ReplaceAllString(service.Key, "-")
	entry := &svcEntry{svc: service, slug: slug, viewID: "detail-svc-" + slug,
		lines: &lineBuffer{max: maxServiceLines}}
	source := func() []*logformat.Line { return entry.lines.all() }
	info := func() string { return m.serviceInfo(service) }
	switch {
	case service.Kind == "wiremock":
		entry.full = newWiremockView(m, entry, info)
	case service.Kind == "kafka":
		entry.full = newKafkaView(m, entry, info)
	case databases.QueryKinds[service.Kind]:
		entry.full = newDBView(m, entry, info)
	default:
		entry.full = newLogView(source, info, "")
	}
	entry.ovLog = newLogView(source, info, "")
	m.svcs = append(m.svcs, entry)
	m.run.addService(m, entry)
}

func (m *Model) serviceChanged(service *services.Service) {
	entry := m.serviceEntry(service)
	if entry == nil {
		return
	}
	if service.State() == "previous" {
		entry.full.clear()
		entry.ovLog.clear()
	}
}

func (m *Model) serviceLines(msg services.Lines) {
	if msg.Gen != m.gen {
		return
	}
	m.addServiceLines(m.serviceEntry(msg.Service), msg.Lines)
}

func (m *Model) addServiceLines(entry *svcEntry, lines []*logformat.Line) {
	if entry == nil {
		return
	}
	for _, line := range lines {
		if line.IsLoggedProblem() {
			entry.problems++
		}
	}
	entry.lines.add(lines...)
	entry.full.append(m, lines)
	entry.ovLog.append(m, lines)
}

func serviceStateName(state string) string {
	switch state {
	case "running":
		return "running"
	case "stopped":
		return "stopped"
	case "previous":
		return "from the last run"
	}
	return ""
}

func (m *Model) serviceInfo(service *services.Service) string {
	role := service.Kind
	if service.IsApp() {
		role = "the application"
	}
	id := service.ContainerID()
	state := service.State()
	badge := style.Pill(serviceStateName(state), style.SubtleText, style.Raised)
	if state == "running" {
		badge = style.Pill("● running", style.Pass, style.Tint(style.Pass))
	}
	return style.Bold.Render(cleanText(service.Key)) + "  " + style.Muted.Render(role) + "  " +
		style.Dim.Render(cleanText(service.Image())+"  "+id[:min(12, len(id))]) + "  " + badge
}

type extraLinesMsg struct {
	entry *svcEntry
	lines []string
}

type extraStoppedMsg struct{ entry *svcEntry }

// followExtra shows a log from .testdash.json, a file or a command's output, like a container's.
func (m *Model) followExtra(extra ExtraLog) {
	kind, source := "file", extra.File
	if extra.File == "" {
		kind, source = "command", extra.Command
	}
	service := services.NewService(extra.Name, kind, source)
	m.serviceAdded(service)
	entry := m.serviceEntry(service)
	send, ctx, workdir := m.send, m.ctx, m.settings.Workdir
	if extra.Command != "" && extra.File == "" {
		go followCommand(ctx, extra.Command, workdir, func(lines []string) { send(extraLinesMsg{entry, lines}) },
			func() { send(extraStoppedMsg{entry}) })
		return
	}
	path := extra.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	go followFile(ctx, path, func(lines []string) { send(extraLinesMsg{entry, lines}) })
}

func (m *Model) extraLines(msg extraLinesMsg) {
	now := time.Now()
	lines := make([]*logformat.Line, 0, len(msg.lines))
	for _, text := range msg.lines {
		lines = append(lines, logformat.NewLine(now, text))
	}
	m.addServiceLines(msg.entry, lines)
}

// refreshing

func (m *Model) tick() tea.Cmd {
	m.expireToasts()
	if m.progress == nil {
		return nil
	}
	changed := m.progress.Poll()
	planned := map[string]bool{}
	for _, e := range m.progress.Planned {
		if e.IsLeaf() {
			planned[e.ID] = true
		}
	}
	for _, e := range m.progress.Registered {
		planned[e.ID] = true
	}
	grew := false
	if m.inRun != nil {
		for id := range planned {
			if !m.inRun[id] {
				m.inRun[id] = true
				changed[id] = true
				grew = true
			}
		}
	}
	if grew {
		m.index()
	}
	var cmds []tea.Cmd
	for id := range changed {
		if state := m.progress.States[id]; state != nil && (state.Status == "failed" || state.Status == "aborted") &&
			state.Message != "" {
			cmds = append(cmds, m.checkPorts(state.Message))
		}
	}
	m.updateClaims()
	if len(changed) > 0 {
		for _, tv := range []*testsView{m.picker.tests, m.run.tests} {
			if grew {
				tv.rebuild(m, !tv.picking && tv.empty())
			}
			tv.changed(m, changed)
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) rebuildTrees(reset bool) {
	m.picker.tests.rebuild(m, reset)
	m.run.tests.rebuild(m, reset)
}

// helpers

func savedTask(root string) string {
	data, err := os.ReadFile(stateFile())
	if err != nil {
		return ""
	}
	var state map[string]string
	if json.Unmarshal(data, &state) != nil {
		return ""
	}
	return state[root]
}

func saveTask(root, task string) {
	state := map[string]string{}
	if data, err := os.ReadFile(stateFile()); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	state[root] = task
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(stateFile()), 0o755) == nil {
		_ = os.WriteFile(stateFile(), data, 0o644)
	}
}

func stateFile() string {
	return filepath.Join(catalog.CacheDir(), "state.json")
}

func firstN[T any](items []T, n int) []T {
	return items[:min(n, len(items))]
}

func lastN[T any](items []T, n int) []T {
	return items[max(0, len(items)-n):]
}

func styleLines(text string, s style.Style) string {
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		parts[i] = s.Render(part)
	}
	return strings.Join(parts, "\n")
}
