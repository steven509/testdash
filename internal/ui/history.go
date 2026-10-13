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
		run.Tests = append(run.Tests, history.Result{ID: entry.ID, Status: state.Status, Took: took,
			Retried: state.Retried, Failure: failure})
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

// testHistory is what a test's past runs say: its last results, oldest first, and what changed.
func (m *Model) testHistory(id string) string {
	points := m.historyIndex[id]
	if len(points) == 0 {
		return ""
	}
	recent := points[:min(len(points), 20)]
	var strip strings.Builder
	for i := len(recent) - 1; i >= 0; i-- {
		strip.WriteString(resultStyle(recent[i].Result.Status).Render("■"))
	}
	lines := []string{style.Dim.Render("History ") + strip.String() + style.Dim.Render("  "+
		count(len(recent), "run")+", newest last")}
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
		lines = append(lines, style.Dim.Render("Usually "+duration(s.Usually)))
	}
	return strings.Join(lines, "\n")
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
