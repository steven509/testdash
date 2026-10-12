// Package progress reads a run's live test state from the progress-*.jsonl files testdash's JUnit listener
// writes.
//
// Each file is one test JVM, a JSON object per line, each with "event" and "at" (epoch millis):
//
//	plan        {"entries": [entry, ...]}  every class, test and test template, in run order
//	registered  {"entry": entry}           a test created while running, e.g. a parameterised invocation
//	started     {"id"}
//	finished    {"id", "status": "SUCCESSFUL" | "FAILED" | "ABORTED", "message"}
//	skipped     {"id", "message"}
//	containers  {"testcontainers": bool, "labels": ["key=value", ...]}
//	done        {}
//
// An entry is {"id", "parent", "kind": "class" | "test" | "template", "className", "methodName", "name"}. Ids are
// JUnit unique ids, the same ones discovery reports, so states map onto the catalog's tree.
//
// Other test runners can feed the progress views too, by writing these events to progress-<id>.jsonl in the
// directory JVM tests get as the testdash.dir system property.
package progress

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"

	"testdash/internal/catalog"
)

var statuses = map[string]string{"SUCCESSFUL": "passed", "FAILED": "failed", "ABORTED": "aborted"}

type State struct {
	Status     string // pending | running | passed | failed | aborted | skipped
	StartedAt  int64  // epoch millis, 0 until it starts
	FinishedAt int64
	Message    string
}

// Took is how long it ran, up to now while it runs; ok is false before it starts.
func (s State) Took(now int64) (millis int64, ok bool) {
	if s.StartedAt == 0 {
		return 0, false
	}
	end := s.FinishedAt
	if end == 0 {
		end = now
	}
	return end - s.StartedAt, true
}

// Progress is everything the run reported so far; Poll reads only what was appended since the last call.
type Progress struct {
	runDir     string
	States     map[string]*State
	Planned    []*catalog.Entry
	Registered []*catalog.Entry // tests created while running
	JVMsDone   int
	// Whether the tests use Testcontainers (nil until a test JVM says), and the labels on the containers they
	// started: Testcontainers' session id and each Compose project's name, as "key=value".
	Testcontainers  *bool
	ContainerLabels map[string]bool
	offsets         map[string]int64
}

func New(runDir string) *Progress {
	return &Progress{runDir: runDir, States: map[string]*State{}, ContainerLabels: map[string]bool{},
		offsets: map[string]int64{}}
}

type event struct {
	Event          string             `json:"event"`
	At             float64            `json:"at"`
	ID             string             `json:"id"`
	Status         string             `json:"status"`
	Message        *string            `json:"message"`
	Entries        []catalog.RawEntry `json:"entries"`
	Entry          *catalog.RawEntry  `json:"entry"`
	Testcontainers bool               `json:"testcontainers"`
	Labels         []string           `json:"labels"`
}

// Poll applies new events and returns the ids whose state changed.
func (p *Progress) Poll() map[string]bool {
	changed := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(p.runDir, "progress-*.jsonl"))
	sort.Strings(files)
	for _, path := range files {
		offset := p.offsets[path]
		data, err := readFrom(path, offset)
		if err != nil {
			continue
		}
		complete := data[:bytes.LastIndexByte(data, '\n')+1] // leave a line still being written for next time
		p.offsets[path] = offset + int64(len(complete))
		for _, line := range bytes.Split(complete, []byte("\n")) {
			var e event
			if len(line) == 0 || json.Unmarshal(line, &e) != nil {
				continue
			}
			p.apply(e, changed)
		}
	}
	return changed
}

func readFrom(path string, offset int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func (p *Progress) apply(e event, changed map[string]bool) {
	switch e.Event {
	case "plan":
		for _, raw := range e.Entries {
			p.Planned = append(p.Planned, catalog.FromRaw(raw))
		}
	case "registered":
		if e.Entry == nil {
			return
		}
		entry := catalog.FromRaw(*e.Entry)
		p.Registered = append(p.Registered, entry)
		changed[entry.ID] = true
	case "done":
		p.JVMsDone++
	case "containers":
		uses := (p.Testcontainers != nil && *p.Testcontainers) || e.Testcontainers
		p.Testcontainers = &uses
		for _, label := range e.Labels {
			p.ContainerLabels[label] = true
		}
	case "started", "finished", "skipped":
		if e.ID == "" {
			return
		}
		state := p.States[e.ID]
		if state == nil {
			state = &State{Status: "pending"}
			p.States[e.ID] = state
		}
		at := int64(e.At)
		message := ""
		if e.Message != nil {
			message = *e.Message
		}
		switch e.Event {
		case "started":
			state.Status, state.StartedAt = "running", at
		case "finished":
			status, ok := statuses[e.Status]
			if !ok {
				status = "failed"
			}
			state.Status, state.FinishedAt, state.Message = status, at, message
		default:
			state.Status, state.Message, state.FinishedAt = "skipped", message, at
		}
		changed[e.ID] = true
	}
}

// State is an entry's state, pending when the run has not said anything about it.
func (p *Progress) State(id string) State {
	if state := p.States[id]; state != nil {
		return *state
	}
	return State{Status: "pending"}
}
