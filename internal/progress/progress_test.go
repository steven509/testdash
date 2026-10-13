package progress

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPollReadsCompleteLinesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress-42.jsonl")
	write := func(text string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	p := New(dir)
	write(`{"event":"plan","at":1,"entries":[{"id":"c","kind":"class","className":"A"},` +
		`{"id":"t","parent":"c","kind":"test","className":"A","methodName":"x","name":"x()"}]}` + "\n")
	write(`{"event":"started","at":1000,"id":"t"}` + "\n" + `{"event":"finished","at":1500,"id":"t","status":"FAI`)
	changed := p.Poll()
	if !changed["t"] || p.State("t").Status != "running" {
		t.Fatalf("after the start: %v %+v", changed, p.State("t"))
	}
	if len(p.Planned) != 2 {
		t.Fatalf("planned %d", len(p.Planned))
	}
	write(`LED","message":"boom"}` + "\n" + `{"event":"containers","testcontainers":true,"labels":["a=b"]}` + "\n")
	changed = p.Poll()
	state := p.State("t")
	if !changed["t"] || state.Status != "failed" || state.Message != "boom" {
		t.Fatalf("after the finish: %v %+v", changed, state)
	}
	if took, ok := state.Took(9999); !ok || took != 500 {
		t.Errorf("took %d", took)
	}
	if p.Testcontainers == nil || !*p.Testcontainers || !p.ContainerLabels["a=b"] {
		t.Errorf("containers: %v %v", p.Testcontainers, p.ContainerLabels)
	}
	if p.State("unknown").Status != "pending" {
		t.Errorf("an unknown test is pending")
	}
}

func TestARetryThatPassesIsFlaky(t *testing.T) {
	dir := t.TempDir()
	events := `{"event":"started","at":1,"id":"t"}
{"event":"finished","at":2,"id":"t","status":"FAILED","message":"boom","trace":"java.lang.AssertionError: boom"}
{"event":"started","at":3,"id":"t"}
{"event":"finished","at":4,"id":"t","status":"SUCCESSFUL"}
`
	if err := os.WriteFile(filepath.Join(dir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(dir)
	p.Poll()
	if state := p.State("t"); state.Status != "passed" || !state.Retried || state.Trace != "" {
		t.Errorf("a pass after a failure in one run: %+v", state)
	}
}
