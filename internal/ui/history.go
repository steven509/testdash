package ui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/history"
	"github.com/steven509/testdash/internal/style"
)

const keepRuns = 200 // as many as the store keeps

// historyMsg brings the build's past runs, newest first.
type historyMsg struct {
	runs []history.Run
}

// commitMsg is the commit a run's code was at as it started.
type commitMsg struct {
	started time.Time
	commit  string
	dirty   bool
}

func (m *Model) loadHistory() tea.Cmd {
	if m.history == nil {
		return nil
	}
	store := m.history
	return func() tea.Msg { return historyMsg{runs: store.Load(keepRuns)} }
}

func (m *Model) historyLoaded(msg historyMsg) {
	seen := map[time.Time]bool{}
	var runs []history.Run
	for _, run := range append(append([]history.Run(nil), m.recorded...), msg.runs...) {
		if !seen[run.Started] {
			seen[run.Started] = true
			runs = append(runs, run)
		}
	}
	m.setRuns(runs)
}

func (m *Model) setRuns(runs []history.Run) {
	m.runs = runs[:min(len(runs), keepRuns)]
	m.historyIndex = history.NewIndex(m.runs)
	m.flaky = map[string]bool{}
	for id, points := range m.historyIndex {
		if history.Summarise(points).Flaky {
			m.flaky[id] = true
		}
	}
	if m.tree.show == "flaky" {
		m.tree.rebuild(m, false)
	}
	m.tree.flatDirty = true
	m.version++
}

// readCommit asks git which commit the build is at, and whether its working tree has changes.
func (m *Model) readCommit() tea.Cmd {
	root := m.opts.BuildRoot
	if root == "" {
		return nil
	}
	started := m.runStarted
	return func() tea.Msg {
		head, err := exec.Command("git", "-C", root, "rev-parse", "--short=12", "HEAD").Output()
		if err != nil {
			return nil
		}
		changes, _ := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=no").Output()
		return commitMsg{started: started, commit: strings.TrimSpace(string(head)),
			dirty: strings.TrimSpace(string(changes)) != ""}
	}
}

// recordRun keeps a finished run's results: in the history shown, and in the store.
func (m *Model) recordRun(code int) tea.Cmd {
	if m.history == nil || m.testTask == nil || !m.listening {
		return nil
	}
	run := history.Run{Started: m.runStarted, Took: m.runFinished.Sub(m.runStarted).Milliseconds(),
		Task: m.testTask.Path, Filters: m.runFilters, Exit: code, Stopped: m.stopRequested, Commit: m.runCommit,
		Dirty: m.runDirty}
	for _, entry := range m.runLeaves() {
		state := m.state(entry.ID)
		switch state.Status {
		case "passed", "failed", "aborted", "skipped":
		default:
			continue // a stopped run never got to it
		}
		took, _ := state.Took(state.FinishedAt)
		failure := state.Message
		if failure == "" {
			failure = state.Trace
		}
		failure, _, _ = strings.Cut(strings.TrimSpace(failure), "\n")
		run.Tests = append(run.Tests, history.Result{ID: entry.ID, Name: m.fullName(entry), Status: state.Status,
			Took: took, Retried: state.Retried, Failure: failure})
	}
	if len(run.Tests) == 0 {
		return nil
	}
	m.recorded = append([]history.Run{run}, m.recorded...)
	m.setRuns(append([]history.Run{run}, m.runs...))
	store := m.history
	return func() tea.Msg {
		if err := store.Save(run); err != nil {
			return notifyMsg{text: "Could not keep the run's results: " + err.Error(), severity: "warning"}
		}
		return nil
	}
}

// testHistory is what a test's past runs say: its last results, oldest first, what changed, and its last failure
// unless it is failing now.
func (m *Model) testHistory(id string, width int) string {
	points := m.historyIndex[id]
	if len(points) == 0 {
		return ""
	}
	recent := points[:min(len(points), 20)]
	var strip strings.Builder
	for i := len(recent) - 1; i >= 0; i-- {
		strip.WriteString(resultStyle(recent[i].Result.Status).Render("■"))
	}
	lines := []string{section("History", width, count(len(recent), "run")+", newest last · H every run"),
		strip.String()}
	s := history.Summarise(points)
	switch {
	case s.FailingFor > 1:
		lines = append(lines, style.Red.Render(fmt.Sprintf("Failing for %d runs, since %s", s.FailingFor,
			runLabel(s.Since))))
	case s.NewFailure:
		lines = append(lines, style.Red.Render("A new failure: it passed in the run before"))
	case s.Fixed:
		lines = append(lines, style.Green.Render("Fixed: it failed in the run before"))
	}
	if s.Flaky {
		lines = append(lines, style.Yellow.Render("Flaky: it has passed and failed on the same code"))
	}
	switch {
	case s.Slower:
		lines = append(lines, style.Yellow.Render(fmt.Sprintf("Slower: %s, usually %s",
			duration(points[0].Result.Took), duration(s.Usually))))
	case s.Usually > 0:
		lines[1] += style.Dim.Render("  usually " + duration(s.Usually))
	}
	if status := m.status(id); status == "failed" || status == "aborted" {
		return strings.Join(lines, "\n")
	}
	for _, point := range points {
		if point.Result.Status == "failed" || point.Result.Status == "aborted" {
			lines = append(lines, style.Muted.Render("Last failed "+runLabel(point.Run)))
			if point.Result.Failure != "" {
				lines = append(lines, "  "+style.Dim.Render(fit(cleanText(point.Result.Failure), width-2, true)))
			}
			break
		}
	}
	return strings.Join(lines, "\n")
}

// resultName names a run's test: as the build has it now, else as the run had it, else by its id.
func (m *Model) resultName(result history.Result) string {
	switch {
	case m.entries[result.ID] != nil:
		return m.fullName(m.entries[result.ID])
	case result.Name != "":
		return result.Name
	}
	if cut := strings.LastIndex(result.ID, "/["); cut > 0 && m.entries[result.ID[:cut]] != nil {
		return m.fullName(m.entries[result.ID[:cut]]) + " › " + idName(result.ID[cut+1:]) // an invocation
	}
	return idName(result.ID)
}

// idName names a test by its JUnit unique id, for a run that kept no name:
// [engine:junit-jupiter]/[class:com.shop.QuoteTest]/[test-template:quotes(String)]/[test-template-invocation:#3]
// is QuoteTest › quotes(String) › [3].
func idName(id string) string {
	var names []string
	for _, segment := range strings.Split(id, "/[") {
		kind, value, ok := strings.Cut(strings.Trim(segment, "[]"), ":")
		switch {
		case !ok || kind == "engine":
		case kind == "class":
			names = append(names, value[strings.LastIndex(value, ".")+1:])
		case kind == "test-template-invocation" || kind == "dynamic-test":
			names = append(names, "["+strings.TrimPrefix(value, "#")+"]")
		default:
			names = append(names, value)
		}
	}
	if len(names) == 0 {
		return id
	}
	return strings.Join(names, " › ")
}

// runLabel names a run by when it ran and the commit it ran.
func runLabel(run *history.Run) string {
	label := run.Started.Local().Format("2 Jan 15:04")
	if run.Commit != "" {
		label += " (" + run.Commit[:min(7, len(run.Commit))]
		if run.Dirty {
			label += " with changes"
		}
		label += ")"
	}
	return label
}

func resultStyle(status string) style.Style {
	switch status {
	case "passed":
		return style.Green
	case "failed", "aborted":
		return style.Red
	case "skipped":
		return style.Yellow
	}
	return style.Dim
}

// sinceLastRun compares the run that just finished with the one before it of the same task: the tests that fail
// anew, the ones it fixed, and the ones that got slower.
func (m *Model) sinceLastRun(width int) string {
	if len(m.runs) < 2 || m.running() || !m.runs[0].Started.Equal(m.runStarted) {
		return ""
	}
	latest := m.runs[0]
	var before *history.Run
	for i := 1; i < len(m.runs) && before == nil; i++ {
		if m.runs[i].Task == latest.Task {
			before = &m.runs[i]
		}
	}
	if before == nil {
		return ""
	}
	previous := map[string]history.Result{}
	for _, result := range before.Tests {
		previous[result.ID] = result
	}
	var failing, fixed, slower []string
	for _, result := range latest.Tests {
		earlier := previous[result.ID]
		if earlier.Status == "" {
			continue
		}
		name := m.resultName(result)
		switch {
		case (result.Status == "failed" || result.Status == "aborted") && earlier.Status == "passed":
			failing = append(failing, name)
		case result.Status == "passed" && (earlier.Status == "failed" || earlier.Status == "aborted"):
			fixed = append(fixed, name)
		}
		if s := history.Summarise(m.historyIndex[result.ID]); s.Slower {
			slower = append(slower, name+style.Dim.Render(fmt.Sprintf("  %s, usually %s", duration(result.Took),
				duration(s.Usually))))
		}
	}
	var b strings.Builder
	b.WriteString(section("Since the last run", width, runLabel(before)) + "\n")
	if len(failing)+len(fixed)+len(slower) == 0 {
		return b.String() + style.Muted.Render("The same results, and no test got slower") + "\n"
	}
	for _, group := range []struct {
		mark, label string
		style       style.Style
		names       []string
	}{{ic.failed, "Fail anew", style.Red, failing}, {ic.passed, "Fixed", style.Green, fixed},
		{ic.clock, "Slower", style.Yellow, slower}} {
		if len(group.names) == 0 {
			continue
		}
		b.WriteString(group.style.Render(group.mark+" "+group.label) + style.Dim.Render("  "+number(len(group.names))) + "\n")
		for i, name := range group.names {
			if i == 5 {
				b.WriteString(style.Dim.Render(fmt.Sprintf("    and %s more", number(len(group.names)-5))) + "\n")
				break
			}
			b.WriteString("    " + fit(name, width-4, true) + "\n")
		}
	}
	return b.String()
}

// lastRun is the newest kept run of the test task.
func (m *Model) lastRun() *history.Run {
	if m.testTask == nil {
		return nil
	}
	for i := range m.runs {
		if m.runs[i].Task == m.testTask.Path {
			return &m.runs[i]
		}
	}
	return nil
}

// failuresOf is what to run to run a run's failures again: each failed test the build still has, or for an
// invocation of a parameterised test, the parameterised test.
func (m *Model) failuresOf(run *history.Run) map[string]bool {
	failed := map[string]bool{}
	for _, result := range run.Tests {
		if result.Status != "failed" && result.Status != "aborted" {
			continue
		}
		id := result.ID
		for !m.selectable[id] && strings.LastIndex(id, "/[") > 0 {
			id = id[:strings.LastIndex(id, "/[")]
		}
		if m.selectable[id] {
			failed[id] = true
		}
	}
	return failed
}

// lastRunSummary is the last run on the dashboard before the first run: how it went, and what failed.
func (m *Model) lastRunSummary(width int) string {
	run := m.lastRun()
	if run == nil {
		return ""
	}
	counts := map[string]int{}
	var failures []history.Result
	for _, result := range run.Tests {
		counts[result.Status]++
		if result.Status == "failed" || result.Status == "aborted" {
			failures = append(failures, result)
		}
	}
	var b strings.Builder
	b.WriteString(section("Last run", width, runLabel(run)) + "\n")
	tally := []string{style.Green.Render(fmt.Sprintf("%s %s passed", ic.passed, number(counts["passed"])))}
	if len(failures) > 0 {
		tally = append(tally, style.Red.Render(fmt.Sprintf("%s %s failed", ic.failed, number(len(failures)))))
	}
	if counts["skipped"] > 0 {
		tally = append(tally, style.Yellow.Render(fmt.Sprintf("%s %s skipped", ic.skipped, number(counts["skipped"]))))
	}
	if run.Stopped {
		tally = append(tally, style.Yellow.Render("stopped"))
	}
	b.WriteString(strings.Join(tally, "  ") + style.Dim.Render("  in "+duration(run.Took)) + "\n")
	for i, result := range failures {
		if i == 5 {
			b.WriteString(style.Dim.Render(fmt.Sprintf("  and %s more", number(len(failures)-5))) + "\n")
			break
		}
		b.WriteString(style.Red.Render(ic.failed+" ") + fit(m.resultName(result), width-2, true) + "\n")
		if result.Failure != "" {
			b.WriteString("    " + style.Dim.Render(fit(cleanText(result.Failure), width-4, true)) + "\n")
		}
	}
	var keys []string
	if len(m.failuresOf(run)) > 0 {
		keys = append(keys, keyCap("F")+" "+style.Muted.Render("runs its failures again"))
	}
	keys = append(keys, keyCap("H")+" "+style.Muted.Render("every run"))
	if len(m.flaky) > 0 {
		keys = append(keys, keyCap("s")+" "+style.Muted.Render(count(len(m.flaky), "flaky test")))
	}
	return b.String() + "\n" + strings.Join(keys, "   ") + "\n"
}

// runList is the build's runs, newest first, with the failures of the one under the cursor, which enter runs again.
type runList struct {
	cursor int
}

func (m *Model) openRuns() {
	if len(m.runs) == 0 {
		m.notify("No runs kept yet: the history starts with the next run", "", "information")
		return
	}
	m.popup = &runList{}
}

func (l *runList) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", "H":
		m.popup = nil
	case "up", "k":
		l.cursor = max(0, l.cursor-1)
	case "down", "j":
		l.cursor = min(len(m.runs)-1, l.cursor+1)
	case "enter", "r":
		return l.rerun(m)
	case "ctrl+c":
		m.popup = &confirmQuit{}
	}
	return nil
}

// rerun runs a run's failed tests again, those the build still has.
func (l *runList) rerun(m *Model) tea.Cmd {
	failed := m.failuresOf(&m.runs[l.cursor])
	if len(failed) == 0 {
		m.notify("No failures of that run to run again", "", "information")
		return nil
	}
	m.popup = nil
	return m.startRun(failed)
}

func (l *runList) render(m *Model, screen []string) {
	width := min(100, m.width-4)
	inner := width - 4
	rows := min(len(m.runs), max(3, m.height/2-6))
	start := max(0, l.cursor-rows+1)
	var content []string
	for i := start; i < start+rows; i++ {
		run := m.runs[i]
		counts := map[string]int{}
		for _, result := range run.Tests {
			counts[result.Status]++
		}
		failed := counts["failed"] + counts["aborted"]
		result := style.Green.Render(ic.passed + " " + compact(counts["passed"]))
		if failed > 0 {
			result += "  " + style.Red.Render(ic.failed+" "+compact(failed))
		}
		if run.Stopped {
			result += "  " + style.Yellow.Render("stopped")
		}
		left := fit(runLabel(&run), 36, true) + "  " + fit(cleanText(run.Task), 18, true) + "  " + result
		content = append(content, menuRow(left, duration(run.Took), inner, i == l.cursor))
	}
	content = append(content, "", section("Failures", inner, ""))
	var failures []string
	for _, result := range m.runs[l.cursor].Tests {
		if result.Status != "failed" && result.Status != "aborted" {
			continue
		}
		failures = append(failures, style.Red.Render(ic.failed+" ")+fit(m.resultName(result), inner-2, true),
			"  "+style.Dim.Render(fit(cleanText(result.Failure), inner-2, true)))
	}
	switch {
	case len(failures) == 0:
		content = append(content, style.Muted.Render("None"))
	case len(failures) > 12:
		content = append(append(content, failures[:12]...),
			style.Dim.Render(fmt.Sprintf("and %s more", number(len(failures)/2-6))))
	default:
		content = append(content, failures...)
	}
	content = append(content, "", style.Dim.Render("enter runs its failures again · esc closes"))
	area := m.dialog(screen, content, width, "Runs", -1)
	m.addZone(zone{r: area, click: func(_, y int) tea.Cmd {
		if i := start + y - 1; y >= 1 && i < start+rows {
			if i == l.cursor {
				return l.rerun(m)
			}
			l.cursor = i
		}
		return nil
	}})
}
