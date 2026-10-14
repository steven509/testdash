// Package ui is testdash's terminal interface: one workspace with the tests on the left, the services the run
// started below them, and on the right the details of whatever is selected, and the work behind it (listing
// tests, running them, following Docker).
//
// Everything the screen shows lives on the Model and changes only in Update. Work that waits (Gradle, Docker,
// the services' own tools) runs in goroutines that send their results in as messages.
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/catalog"
	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/gradle"
	"github.com/steven509/testdash/internal/history"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/noise"
	"github.com/steven509/testdash/internal/progress"
	"github.com/steven509/testdash/internal/runner"
	"github.com/steven509/testdash/internal/services"
	"github.com/steven509/testdash/internal/settings"
	"github.com/steven509/testdash/internal/style"
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

// Options are what testdash was started with.
type Options struct {
	Workdir              string   // where the command runs
	BuildRoot            string   // the Gradle build, when there is one
	Command              []string // an explicit or configured command; nil runs the chosen Gradle task
	ExtraArgs            []string
	Tests                []string // Gradle --tests patterns from the command line, run at once
	Task                 string
	ShowTestOutput       bool
	IncludeAllContainers bool
	RunDir               string
	ExtraLogs            []ExtraLog   // from .testdash.json
	Noise                []noise.Rule // the build's noise rules, from .testdash.json
	ConfigPath           string       // the build's .testdash.json, where its noise rules are saved
	Keycast              string       // shows the keys pressed, for recordings and demos: all, or only shortcuts
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

// svcEntry is a service and what the UI keeps for it: its log, its problems, its view.
type svcEntry struct {
	svc      *services.Service
	slug     string
	lines    *lineBuffer
	problems int // warnings and errors logged this run, counted as lines arrive
	view     view
	profile  *appProfile // what profiling has sampled of an application
}

type Model struct {
	ctx   context.Context
	send  func(tea.Msg)
	opts  Options
	prefs settings.Settings

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
	runCommand    string // what the last run ran, as shown
	runAtStart    bool   // the command line named tests (a task or --tests), so they run straight away
	requestedTask string // a task path the command line named, run even before discovery knows it
	discoverAfter bool   // listing the tests waits until the first run is done
	pendingCmd    tea.Cmd
	listening     bool
	claims        *services.Claims
	watcher       *services.Watcher
	gen           int
	svcs          []*svcEntry
	logSimple     bool // every log shows a line's level and message only
	logTime       bool
	logWrap       bool
	portConflict  string // why the run could not start its containers
	explained     map[string]bool
	version       int // goes up whenever what a test's details show may have changed
	activityKey   string
	fullCommand   bool   // the dashboard shows the run's whole command, not one line of it
	activityText  string // the dashboard's activity, drawn when the key changes

	activityChartKey, activityChartText string // the activity at full size, likewise

	width, height   int
	spinner         spinner.Model
	spinning        bool
	tree            *testTree
	serviceList     *serviceList
	inspector       *inspector
	outputView      *logView
	focus           any
	detailFrom      panel // the panel whose selection the detail shows
	zoomed          bool
	away            bool // the terminal is in the background, as it reports
	history         *history.Store
	runs            []history.Run // the build's runs, newest first
	recorded        []history.Run // the runs since testdash started
	historyIndex    history.Index
	flaky           map[string]bool // the tests the history says are flaky
	noise           *noise.Filter
	noiseGeneration int
	traceFilter     string // the trace every service's view is narrowed to, if one
	runCommit       string // the commit the run's code was at, and whether the working tree had changes
	runDirty        bool
	runFilters      []string
	stopRequested   bool
	popup           popup
	toasts          []*toast
	toastSeq        int
	keycast         keycast
	keyLabel        string // what the last key did, as its binding names it
	keyShortcut     bool   // whether the last key ran a binding, rather than moving, typing or answering a dialog
	zones           []zone
	dirty           bool
	frame           string
}

func New(ctx context.Context, opts Options, prefs settings.Settings) *Model {
	m := &Model{
		ctx: ctx, opts: opts, prefs: prefs, send: func(tea.Msg) {},
		selected: map[string]bool{}, lastSelection: map[string]bool{}, selectable: map[string]bool{},
		runner: &runner.Runner{}, discovery: &runner.Runner{},
		output: &lineBuffer{max: maxOutputLines}, claims: &services.Claims{},
		logSimple: !prefs.FullLogLines, logTime: prefs.LogTime, logWrap: prefs.WrapLogs,
		explained: map[string]bool{}, dirty: true,
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	style.ApplyTheme(prefs.Theme)
	useIcons(prefs.NerdFont, prefs.IconSpacing)
	if opts.BuildRoot != "" {
		m.catalog = catalog.Load(opts.BuildRoot)
		m.history = history.Open(catalog.CacheDir(), opts.BuildRoot)
	}
	m.takeTaskArgs()
	var scope []string
	for _, dir := range []string{opts.Workdir, opts.BuildRoot} {
		if dir != "" {
			scope = append(scope, dir)
		}
	}
	m.watcher = services.NewWatcher(ctx, func(msg any) { m.send(msg) }, scope, opts.IncludeAllContainers, m.claims)
	m.watcher.SetHidden(prefs.HiddenServices)
	m.tree = newTestTree()
	m.serviceList = &serviceList{}
	m.inspector = newInspector()
	m.outputView = newLogView(func() []*logformat.Line { return m.output.all() }, m.outputInfo)
	m.outputView.noise.rules, m.outputView.noise.gen = m.noiseFor("gradle"), func() int { return m.noiseGeneration }
	m.outputView.service = "gradle"
	m.outputView.wrap = m.logWrap
	m.focus = m.tree
	m.chooseTask(m.opts.Task, true)
	m.setNoise()
	return m
}

// takeTaskArgs finds task paths among the arguments for Gradle, as gradlew takes them: they choose the task, and
// like --tests, they mean the run starts at once.
func (m *Model) takeTaskArgs() {
	var rest []string
	for _, arg := range m.opts.ExtraArgs {
		if path := m.taskArg(arg); path != "" && m.opts.Command == nil {
			m.opts.Task, m.requestedTask, m.runAtStart = path, path, true
			continue
		}
		rest = append(rest, arg)
	}
	m.opts.ExtraArgs = rest
	if len(m.opts.Tests) > 0 {
		m.runAtStart = true
	}
}

// taskArg is the task path an argument names, or "": a path such as :app:test, or a known task's name.
func (m *Model) taskArg(arg string) string {
	if strings.HasPrefix(arg, ":") {
		return arg
	}
	if m.catalog == nil || strings.HasPrefix(arg, "-") {
		return ""
	}
	for _, path := range m.catalog.Paths {
		if arg == path[strings.LastIndex(path, ":")+1:] {
			return path
		}
	}
	return ""
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
	cmds := []tea.Cmd{tick(), m.loadHistory()}
	canDiscover := m.opts.BuildRoot != "" && (m.opts.Command == nil || m.commandIsGradle())
	switch {
	case m.runAtStart && m.baseCommand() != nil:
		m.runAtStart = false
		m.discoverAfter = canDiscover
		cmds = append(cmds, m.startCommandLineRun())
	case canDiscover:
		cmds = append(cmds, m.discover())
	}
	if services.Available() {
		go m.watcher.Run()
	} else {
		m.notify("docker is not on PATH: no service views", "", "warning")
	}
	for _, extra := range m.opts.ExtraLogs {
		m.followExtra(extra)
	}
	return tea.Batch(cmds...)
}

// Shutdown stops what testdash started, once the program has ended.
func (m *Model) Shutdown() {
	m.runner.Kill()
	m.discovery.Kill()
	databases.SwitchOff()
	_ = os.RemoveAll(m.opts.RunDir)
}

// the tests

func (m *Model) chooseTask(wanted string, quiet bool) {
	tasks := map[string]*catalog.TestTask{}
	var paths []string
	if m.catalog != nil {
		tasks, paths = m.catalog.Tasks, m.catalog.Paths
	}
	path := ""
	for _, candidate := range []string{wanted, m.taskInCommand(), savedTask(m.opts.BuildRoot), ":test", ":app:test"} {
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
	if m.testTask != nil && m.opts.BuildRoot != "" {
		saveTask(m.opts.BuildRoot, m.testTask.Path)
	}
	m.index()
	for id := range m.selected {
		if !m.selectable[id] {
			delete(m.selected, id)
		}
	}
	if changed && !quiet && m.testTask != nil {
		m.notify(m.testTask.Path+": "+count(len(m.selectable), "test"), "", "information")
	}
	m.tree.expanded = nil // another task's tree opens as it starts
	m.tree.rebuild(m, true)
	m.version++
}

// index builds the tree: the task's tests, plus the tests the current run created.
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
		for _, e := range m.progress.Planned { // tests the run has that the listed ones lack
			if _, seen := m.entries[e.ID]; !seen {
				m.entries[e.ID] = e
				order = append(order, e)
			}
		}
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
	status := m.progress.State(id).Status
	if status == "passed" { // a parameterised test passes as a container even when one of its invocations fails
		for _, invocation := range m.kids[id] {
			if s := m.progress.State(invocation.ID).Status; s == "failed" || s == "aborted" {
				return "failed"
			}
		}
	}
	return status
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

// countedUnder is what a row's counts count: its tests, with a parameterised test's invocations in its place
// once a run has registered them, as the run's own counts do. "" is every test.
func (m *Model) countedUnder(id string) []string {
	var leaves []string
	if id == "" {
		for leaf := range m.selectable {
			leaves = append(leaves, leaf)
		}
	} else {
		leaves = m.leavesUnder(id)
	}
	var tests []string
	for _, leaf := range leaves {
		if invocations := m.kids[leaf]; len(invocations) > 0 {
			for _, invocation := range invocations {
				tests = append(tests, invocation.ID)
			}
			continue
		}
		tests = append(tests, leaf)
	}
	return tests
}

// selectionFor is what runs one tree node again: a test, a class's tests, or for a parameterised test's
// invocation, which Gradle cannot filter on, the whole parameterised test. "" is every test.
func (m *Model) selectionFor(id string) map[string]bool {
	selection := map[string]bool{}
	if id == "" {
		return selection
	}
	entry := m.entries[id]
	for entry != nil && !m.selectable[entry.ID] && entry.Kind != "class" {
		entry = m.entries[entry.Parent]
	}
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
	w := &window{start: time.UnixMilli(state.StartedAt).Add(-correlationSlack),
		end: time.UnixMilli(end).Add(correlationSlack), label: "during " + cleanText(name)}
	for _, other := range m.runLeaves() { // the slack stops where the tests before and after it ran
		if other.ID == id {
			continue
		}
		neighbour := m.state(other.ID)
		if finished := time.UnixMilli(neighbour.FinishedAt); neighbour.FinishedAt != 0 &&
			neighbour.FinishedAt <= state.StartedAt && finished.After(w.start) {
			w.start = finished
		}
		if started := time.UnixMilli(neighbour.StartedAt); neighbour.StartedAt != 0 && neighbour.StartedAt >= end &&
			started.Before(w.end) {
			w.end = started
		}
	}
	return w
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
// rerunnable is what F runs: the tests that failed in this session, or before its first run, in the last run.
func (m *Model) rerunnable() map[string]bool {
	if !m.runStarted.IsZero() {
		return m.failedIDs()
	}
	if last := m.lastRun(); last != nil {
		return m.failuresOf(last)
	}
	return nil
}

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

// noTaskReason says why there is no test list, as lines to show.
func (m *Model) noTaskReason() []string {
	switch {
	case m.opts.Command != nil && !m.commandIsGradle():
		return []string{style.Muted.Render("Not a Gradle command, so there is no test list. r runs the command.")}
	case m.discovering:
		return []string{style.Muted.Render("Listing the build's tests with Gradle…")}
	case m.opts.BuildRoot == "":
		return []string{style.Muted.Render("No Gradle build here, and no command to run. Start testdash in a Gradle " +
			"repo, or give it a command to run, like this:"), "", style.AccentText.Render("testdash -- npm test")}
	}
	return []string{style.Muted.Render("No tests found. List them again from the palette (ctrl+p).")}
}

type outputMsg struct{ lines []string }

type discoveredMsg struct {
	code      int
	outputDir string
	err       error
}

func (m *Model) discover() tea.Cmd {
	root := m.opts.BuildRoot
	if root == "" {
		return nil
	}
	if m.discovering {
		m.notify("Already listing the tests", "", "information")
		return nil
	}
	outputDir := filepath.Join(m.opts.RunDir, "discovery")
	_ = os.RemoveAll(outputDir)
	_ = os.MkdirAll(outputDir, 0o755)
	m.discovering = true
	m.addOutput("── listing tests with Gradle ──", "bold")
	send, discovery, properties := m.send, m.discovery, gradle.BuildProperties(m.opts.ExtraArgs)
	return tea.Batch(m.spin(), func() tea.Msg {
		command, err := gradle.DiscoverCommand(filepath.Join(root, "gradlew"), outputDir, properties)
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
	current := m.opts.Task
	if m.testTask != nil {
		current = m.testTask.Path
	}
	m.chooseTask(current, true)
	if msg.code != 0 && len(m.catalog.Tasks) == 0 {
		m.notify("Listing the tests failed: see Gradle's output", "", "error")
	}
	if m.runAtStart {
		m.runAtStart = false
		m.pendingCmd = m.startCommandLineRun()
	}
}

func (m *Model) taskInCommand() string {
	if len(m.opts.Command) == 0 || m.catalog == nil {
		return ""
	}
	for _, argument := range m.opts.Command[1:] {
		for _, path := range m.catalog.Paths {
			if argument == path || argument == path[strings.LastIndex(path, ":")+1:] {
				return path
			}
		}
	}
	return ""
}

func (m *Model) commandIsGradle() bool {
	return len(m.opts.Command) > 0 && gradle.IsGradle(m.opts.Command)
}

// running

func (m *Model) baseCommand() []string {
	switch {
	case m.opts.Command != nil:
		return m.opts.Command
	case m.opts.BuildRoot == "":
		return nil
	case m.requestedTask != "" && (m.testTask == nil || m.testTask.Path != m.requestedTask):
		return []string{filepath.Join(m.opts.BuildRoot, "gradlew"), m.requestedTask}
	case m.testTask != nil:
		return []string{filepath.Join(m.opts.BuildRoot, "gradlew"), m.testTask.Path}
	}
	return nil
}

// startCommandLineRun runs what the command line named: its --tests patterns as Gradle matches them, which
// also become the selection, so r runs them again.
func (m *Model) startCommandLineRun() tea.Cmd {
	if len(m.opts.Tests) == 0 {
		return m.startRun(nil)
	}
	if m.testTask != nil {
		m.selected = catalog.MatchTests(m.testTask, m.opts.Tests)
	}
	var filters []string
	for _, pattern := range m.opts.Tests {
		filters = append(filters, "--tests", pattern)
	}
	return m.launch(filters, m.selected)
}

type runDoneMsg struct{ code int }

type listeningMsg struct{ on bool }

func (m *Model) running() bool {
	return !m.runStarted.IsZero() && m.runFinished.IsZero()
}

// overviewName names the tree's first row: the run's overview while it runs, else all the tests to pick from.
func (m *Model) overviewName() string {
	if m.running() {
		return "Overview"
	}
	return "All tests"
}

// startRun runs a selection of tests; an empty one runs them all.
func (m *Model) startRun(selection map[string]bool) tea.Cmd {
	if m.runner.Running() {
		m.notify("Already running: x stops it", "", "warning")
		return nil
	}
	base := m.baseCommand()
	if base == nil {
		m.notify("Nothing to run: no Gradle test task and no command", "", "warning")
		return nil
	}
	picked := map[string]bool{}
	for id := range selection {
		if m.selectable[id] {
			picked[id] = true
		}
	}
	var filters []string
	if len(picked) > 0 && len(picked) < len(m.selectable) && m.testTask != nil && gradle.IsGradle(base) {
		filters = catalog.GradleFilters(m.testTask, picked) // every test selected runs the task as it is
	}
	return m.launch(filters, picked)
}

// launch runs the command with test filters added.
func (m *Model) launch(filters []string, picked map[string]bool) tea.Cmd {
	if m.runner.Running() {
		m.notify("Already running: x stops it", "", "warning")
		return nil
	}
	base := m.baseCommand()
	if base == nil {
		m.notify("Nothing to run: no Gradle test task and no command", "", "warning")
		return nil
	}
	m.lastSelection = picked
	if m.discovering {
		m.discovery.Stop()
	}
	command := append(append([]string{}, base...), m.opts.ExtraArgs...)
	if gradle.IsGradle(command) {
		command = append(command, filters...)
	}
	m.runCommand = strings.Join(command, " ")
	m.listening = gradle.IsGradle(command)
	if files, _ := filepath.Glob(filepath.Join(m.opts.RunDir, "progress-*.jsonl")); len(files) > 0 {
		for _, stale := range files {
			_ = os.Remove(stale)
		}
	}
	_ = os.RemoveAll(filepath.Join(m.opts.RunDir, "wiremock-journal"))
	m.runStarted, m.runFinished, m.exitCode = time.Now(), time.Time{}, 0
	m.runFilters, m.stopRequested, m.runCommit, m.runDirty = filters, false, "", false
	m.portConflict, m.explained = "", map[string]bool{}
	m.progress = progress.New(m.opts.RunDir)
	m.inRun = map[string]bool{}
	m.index()
	m.updateClaims()
	m.gen = m.watcher.NewRun(m.runStarted)
	for _, entry := range m.svcs {
		entry.lines.clear()
		entry.problems = 0
		entry.view.clear()
	}
	m.output.clear()
	m.outputView.clear()
	m.addOutput("▶ "+m.displayCommand(), "bold")
	m.tree.rebuild(m, false)
	m.version++
	m.showRun()
	spin := tea.Batch(m.spin(), m.readCommit())

	send, runDir, quiet := m.send, m.opts.RunDir, !m.opts.ShowTestOutput
	workdir, run := m.opts.Workdir, m.runner
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

// showRun turns the details to the run's dashboard, as a run starts.
func (m *Model) showRun() {
	m.tree.cursor, m.tree.want = "", ""
	m.tree.scrollToCursor = true
	m.setFocus(m.tree)
}

// unselectPassed drops the tests that passed (or were skipped) from the selection, keeping those that failed
// or have not run.
func (m *Model) unselectPassed() {
	dropped := 0
	for id := range m.selected {
		if s := m.status(id); s == "passed" || s == "skipped" {
			delete(m.selected, id)
			dropped++
		}
	}
	if dropped > 0 {
		m.notify(fmt.Sprintf("Unselected %s; %s still selected", count(dropped, "passed test"),
			number(len(m.selected))),
			"", "information")
	}
}

// selectionPassed is whether any selected test passed or was skipped, for u to unselect.
func (m *Model) selectionPassed() bool {
	for id := range m.selected {
		if s := m.status(id); s == "passed" || s == "skipped" {
			return true
		}
	}
	return false
}

// runLabel says what r runs.
func (m *Model) runLabel() string {
	if n := len(m.selected); n > 0 {
		return "Run " + count(n, "selected test")
	}
	return "Run all"
}

// updateClaims tells the watcher what the run's tests said about their containers.
func (m *Model) updateClaims() {
	state := services.ClaimState{Listening: m.listening, RunActive: m.running()}
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
	m.portConflict = "Port " + msg.port + " is taken" + holder +
		". Another run is probably using it: wait for it to finish, or stop it, then run again."
	m.notifyFor(m.portConflict, "The run could not start its containers", "error", 20*time.Second)
}

func (m *Model) runDone(code int) tea.Cmd {
	m.exitCode, m.runFinished = code, time.Now()
	m.watcher.EndRun(m.runFinished)
	cmd := tea.Batch(m.tick(), m.recordRun(code))
	took := duration(m.runFinished.Sub(m.runStarted).Milliseconds())
	summary := m.runSummary()
	if code == 0 {
		m.addOutput("passed in "+took, "green")
		m.notify(fmt.Sprintf("%s passed in %s", count(summary.passed, "test"), took), "Passed", "success")
		cmd = tea.Batch(cmd, m.notifyDesktop(fmt.Sprintf("%s passed in %s", count(summary.passed, "test"), took)))
	} else {
		m.addOutput(fmt.Sprintf("failed (exit %d) in %s", code, took), "red")
		m.notify(fmt.Sprintf("%s of %s tests failed (exit %d)", number(summary.failed), number(summary.total), code),
			"Failed", "error")
		cmd = tea.Batch(cmd, m.notifyDesktop(fmt.Sprintf("%s of %s tests failed", number(summary.failed),
			number(summary.total))))
	}
	m.updateClaims()
	m.version++
	if m.discoverAfter {
		m.discoverAfter = false
		return tea.Batch(cmd, m.discover())
	}
	return cmd
}

func (m *Model) stop() {
	if m.runner.Running() {
		m.runner.Stop()
		m.stopRequested = true
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
	m.outputView.append(m, []*logformat.Line{line})
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
	m.outputView.append(m, lines)
	return tea.Batch(cmds...)
}

func (m *Model) outputInfo() string {
	return style.Muted.Render(m.displayCommand()) + style.Dim.Render("   in "+m.opts.Workdir)
}

// what the screen summarises

type runEntry struct {
	entry *catalog.Entry
	state progress.State
}

type summary struct {
	total, passed, failed, skipped, left int
	running                              []runEntry
	recent                               []runEntry // finished, the failures first, latest first
}

func (s summary) done() int {
	return s.passed + s.failed + s.skipped
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

func sortByFinished(entries []runEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].state.FinishedAt > entries[j].state.FinishedAt })
}

func (m *Model) elapsed() string {
	end := m.runFinished
	if end.IsZero() {
		end = time.Now()
	}
	return duration(end.Sub(m.runStarted).Milliseconds())
}

// activity is what a run is doing when no test is running: building, setting up a class, or finishing.
func (m *Model) activity() string {
	var settingUp *catalog.Entry
	for _, e := range m.allEntries() {
		if e.Kind == "class" && m.state(e.ID).Status == "running" {
			settingUp = e
		}
	}
	switch {
	case m.portConflict != "":
		return "can't start its containers"
	case settingUp != nil:
		return "setting up " + settingUp.ShortClass()
	case len(m.runLeaves()) > 0:
		return "finishing…"
	}
	return "building…"
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

// groupOf is what a test sits in, outermost first: Outer › Nested, and the parameterised test of an invocation.
func (m *Model) groupOf(entry *catalog.Entry) []string {
	var names []string
	for parent := m.entries[entry.Parent]; parent != nil; parent = m.entries[parent.Parent] {
		name := cleanText(parent.Name)
		if grand := m.entries[parent.Parent]; parent.Kind == "class" && (grand == nil || grand.Kind != "class") {
			name = parent.ShortClass()
		}
		names = append([]string{name}, names...)
	}
	return names
}

// fullName is a test's name after its classes' and its parameterised test's: QuoteTest › quotes › [1] JP.
func (m *Model) fullName(entry *catalog.Entry) string {
	return strings.Join(append(m.groupOf(entry), cleanText(entry.Name)), " › ")
}

// testName is a test's name, with its parameterised test's in front of an invocation's: positive › [1] 1.
func (m *Model) testName(entry *catalog.Entry) string {
	name := cleanText(entry.Name)
	if parent := m.entries[entry.Parent]; parent != nil && parent.Kind == "template" {
		name = cleanText(parent.Name) + " › " + name
	}
	return name
}

// displayCommand is the run's command with the build's directory left out, as it was typed.
func (m *Model) displayCommand() string {
	command := m.runCommand
	if command == "" {
		command = strings.Join(m.baseCommand(), " ")
	}
	for _, dir := range []string{m.opts.BuildRoot, m.opts.Workdir} {
		if dir != "" {
			command = strings.ReplaceAll(command, dir+"/", "./")
		}
	}
	return cleanText(command)
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

// orderedServices lists the services the same way every run, whichever started first: the application, then
// mocks, brokers, databases and the rest, each by name. Kinds hidden in the settings are left out.
func (m *Model) orderedServices() []*svcEntry {
	var ordered []*svcEntry
	for _, entry := range m.svcs {
		if !m.prefs.Hidden(entry.svc.Kind) {
			ordered = append(ordered, entry)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := serviceRank(ordered[i].svc), serviceRank(ordered[j].svc)
		if a != b {
			return a < b
		}
		return ordered[i].svc.Key < ordered[j].svc.Key
	})
	return ordered
}

func serviceRank(service *services.Service) int {
	switch service.Kind {
	case "app":
		return 0
	case "wiremock", "mockserver":
		return 1
	case "kafka", "rabbitmq":
		return 2
	case "mariadb", "mysql", "mongodb", "postgres", "redis", "elasticsearch", "opensearch":
		return 3
	}
	return 4
}

func (m *Model) serviceAdded(service *services.Service) {
	if m.serviceEntry(service) != nil {
		return
	}
	entry := &svcEntry{svc: service, slug: notSlug.ReplaceAllString(service.Key, "-"),
		lines: &lineBuffer{max: maxServiceLines}}
	source := func() []*logformat.Line { return entry.lines.all() }
	info := func() string { return m.serviceInfo(service) }
	switch {
	case service.Kind == "wiremock":
		entry.view = newWiremockView(m, entry, info)
	case service.Kind == "kafka":
		entry.view = newKafkaView(m, entry, info)
	case databases.QueryKinds[service.Kind]:
		entry.view = newDBView(m, entry, info)
	default:
		log := newLogView(source, info)
		log.wrap = m.logWrap
		entry.view = log
	}
	m.followNoise(entry)
	m.svcs = append(m.svcs, entry)
	m.followProfile(entry)
	m.version++
}

func (m *Model) serviceChanged(service *services.Service) {
	entry := m.serviceEntry(service)
	if entry != nil && service.State() == "previous" {
		entry.view.clear()
	}
}

func (m *Model) serviceLines(msg services.Lines) {
	if msg.Gen == m.gen {
		m.addServiceLines(m.serviceEntry(msg.Service), msg.Lines)
	}
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
	entry.view.append(m, lines)
	m.version++
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
	return style.Muted.Render(role) + style.Dim.Render("  "+cleanText(service.Image())+"  "+id[:min(12, len(id))])
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
	send, ctx, workdir := m.send, m.ctx, m.opts.Workdir
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
		m.tree.rebuild(m, false)
	}
	var cmds []tea.Cmd
	for id := range changed {
		if state := m.progress.States[id]; state != nil && (state.Status == "failed" || state.Status == "aborted") &&
			state.Message != "" {
			cmds = append(cmds, m.checkPorts(state.Message))
		}
	}
	if len(changed) > 0 {
		m.version++
		m.tree.flatDirty = true
		if m.tree.show != "all" && !grew { // which tests show depends on how they did
			m.tree.rebuild(m, false)
		}
	}
	m.updateClaims()
	return tea.Batch(cmds...)
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
