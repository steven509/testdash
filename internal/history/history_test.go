package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadAndPrune(t *testing.T) {
	store := Open(t.TempDir(), "/repo")
	now := time.Now()
	for i := range 3 {
		run := Run{Started: now.Add(time.Duration(i) * time.Minute), Task: ":test",
			Tests: []Result{{ID: "t", Status: "passed", Took: int64(100 + i)}}}
		if err := store.Save(run); err != nil {
			t.Fatal(err)
		}
	}
	old := Run{Started: now.Add(-100 * 24 * time.Hour), Task: ":test"}
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}
	runs := store.Load(10)
	if len(runs) != 3 || runs[0].Tests[0].Took != 102 || !runs[0].Started.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("the newest first, and none past 90 days: %+v", runs)
	}
	if got := store.Load(2); len(got) != 2 {
		t.Errorf("a limit: %d", len(got))
	}
	entries, _ := os.ReadDir(store.dir)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Errorf("a leftover %s", entry.Name())
		}
	}
}

func TestSummarise(t *testing.T) {
	run := func(commit string, dirty bool, status string, took int64, retried bool) Run {
		return Run{Commit: commit, Dirty: dirty, Tests: []Result{{ID: "t", Status: status, Took: took, Retried: retried}}}
	}
	summarise := func(runs ...Run) Summary { return Summarise(NewIndex(runs)["t"]) }

	s := summarise(run("c3", false, "failed", 0, false), run("c3", false, "failed", 0, false),
		run("c2", false, "passed", 100, false))
	if s.FailingFor != 2 || s.NewFailure || s.Fixed || s.Flaky {
		t.Errorf("failing for two runs: %+v", s)
	}
	if s = summarise(run("c2", false, "failed", 0, false), run("c1", false, "passed", 100, false)); !s.NewFailure {
		t.Errorf("a new failure: %+v", s)
	}
	if s = summarise(run("c2", false, "passed", 100, false), run("c1", false, "failed", 0, false)); !s.Fixed {
		t.Errorf("fixed: %+v", s)
	}
	if s = summarise(run("c1", false, "passed", 100, false), run("c1", false, "failed", 0, false)); !s.Flaky {
		t.Errorf("passing and failing on one clean commit is flaky: %+v", s)
	}
	if s = summarise(run("c1", true, "passed", 100, false), run("c1", true, "failed", 0, false)); s.Flaky {
		t.Errorf("with changes in the working tree it is not: %+v", s)
	}
	if s = summarise(run("c1", false, "passed", 100, true)); !s.Flaky {
		t.Errorf("passing when retried is flaky: %+v", s)
	}
	s = summarise(run("c1", false, "passed", 900, false), run("c1", false, "passed", 300, false),
		run("c1", false, "passed", 250, false), run("c1", false, "passed", 280, false))
	if s.Usually != 280 || !s.Slower {
		t.Errorf("slower than its usual 280ms: %+v", s)
	}
	if s = summarise(run("c1", false, "passed", 400, false), run("c1", false, "passed", 300, false),
		run("c1", false, "passed", 250, false), run("c1", false, "passed", 280, false)); s.Slower {
		t.Errorf("not slower by enough: %+v", s)
	}
}
