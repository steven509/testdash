package ui

import (
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/history"
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
