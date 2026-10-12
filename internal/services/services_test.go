package services

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	for image, want := range map[string]string{
		"wiremock/wiremock:3.9.1":              "wiremock",
		"docker.io/library/mariadb:11.8":       "mariadb",
		"mongo:8.2":                            "mongodb",
		"confluentinc/cp-kafka:7.6.0":          "kafka",
		"testcontainers/ryuk:0.11.0":           "",
		"alpine/socat:1.7.4.3-r0":              "",
		"registry.example.com/team/orders-api": "app",
		"postgres@sha256:abc":                  "postgres",
	} {
		if got := Classify(image); got != want {
			t.Errorf("Classify(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestDockerTimestamps(t *testing.T) {
	line := logLine("2026-10-12T15:45:51.006123456Z hello world")
	want := time.Date(2026, 10, 12, 15, 45, 51, 6123456, time.UTC)
	if !line.At.Equal(want) || line.Text != "hello world" {
		t.Errorf("got %v %q", line.At, line.Text)
	}
	if untimed := logLine("no stamp here"); !untimed.At.IsZero() || untimed.Text != "no stamp here" {
		t.Errorf("got %v %q", untimed.At, untimed.Text)
	}
}

func TestBelongs(t *testing.T) {
	claims := &Claims{}
	started := time.Now()
	w := NewWatcher(t.Context(), func(any) {}, []string{"/repo"}, false, claims)
	found := func(created time.Time, labels map[string]string) details {
		var d details
		d.Created = created.UTC().Format(time.RFC3339Nano)
		d.Config.Labels = labels
		return d
	}
	repoCompose := map[string]string{"com.docker.compose.project.working_dir": "/repo/docker"}
	if got := w.belongs("early", found(started, repoCompose)); got != notOurs {
		t.Errorf("before the first run no container is the run's, even the repo's own: %v", got)
	}
	w.NewRun(started)
	if got := w.belongs("old", found(started.Add(-time.Minute), map[string]string{"org.testcontainers": "true"})); got != notOurs {
		t.Errorf("a container from before the run is not ours: %v", got)
	}
	if got := w.belongs("tc", found(started, map[string]string{"org.testcontainers": "true"})); got != ours {
		t.Errorf("a Testcontainers container from the run is ours: %v", got)
	}
	if got := w.belongs("compose", found(started, repoCompose)); got != ours {
		t.Errorf("a container of the repo's Compose file is ours: %v", got)
	}
	if got := w.belongs("files", found(started, map[string]string{
		"com.docker.compose.project.config_files": "/elsewhere/base.yaml,/repo/compose.yaml"})); got != ours {
		t.Errorf("a container of a Compose file in the repo is ours, whichever file it is in the list: %v", got)
	}
	if got := w.belongs("sibling", found(started, map[string]string{
		"com.docker.compose.project.working_dir": "/repo2/docker"})); got != notOurs {
		t.Errorf("a repo whose name starts with this one's is another repo: %v", got)
	}
	session := map[string]string{"org.testcontainers.sessionId": "abc", "org.testcontainers": "true"}
	claims.Set(ClaimState{Listening: true, RunActive: true})
	if got := w.belongs("waiting", found(started, session)); got != undecided {
		t.Errorf("an unclaimed container waits while the tests have not said: %v", got)
	}
	uses := true
	claims.Set(ClaimState{Listening: true, RunActive: true, Testcontainers: &uses,
		Labels: map[string]bool{"org.testcontainers.sessionId=abc": true}})
	if got := w.belongs("claimed", found(started, session)); got != ours {
		t.Errorf("a claimed container is ours: %v", got)
	}
	other := map[string]string{"org.testcontainers.sessionId": "other", "org.testcontainers": "true"}
	claims.Set(ClaimState{Listening: true, Testcontainers: &uses})
	if got := w.belongs("theirs", found(started, other)); got != notOurs {
		t.Errorf("another run's container is not ours once the run ends: %v", got)
	}
	finished := started.Add(time.Minute)
	w.EndRun(finished)
	claims.Set(ClaimState{})
	if got := w.belongs("during", found(finished.Add(-time.Second), repoCompose)); got != ours {
		t.Errorf("a container created while the run went stays ours after it: %v", got)
	}
	if got := w.belongs("after", found(finished.Add(time.Minute), repoCompose)); got != notOurs {
		t.Errorf("a container created after the run is not the run's: %v", got)
	}
}
