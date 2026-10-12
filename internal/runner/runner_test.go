package runner

import (
	"strings"
	"testing"
	"time"
)

func TestRunStreamsOutputAndExitCode(t *testing.T) {
	var r Runner
	var got []string
	code := r.Run([]string{"sh", "-c", "echo one; echo two >&2; printf 'three\r\n'; exit 3"}, t.TempDir(),
		func(lines []string) { got = append(got, lines...) })
	if code != 3 || strings.Join(got, ",") != "one,two,three" {
		t.Errorf("code %d, lines %q", code, got)
	}
	if code := r.Run([]string{"/no/such/command"}, t.TempDir(), func([]string) {}); code != 127 {
		t.Errorf("a missing command exits 127, got %d", code)
	}
}

func TestStopInterruptsThenKills(t *testing.T) {
	var r Runner
	done := make(chan int)
	go func() {
		done <- r.Run([]string{"sh", "-c", "trap '' INT; sleep 30"}, t.TempDir(), func([]string) {})
	}()
	for !r.Running() {
		time.Sleep(10 * time.Millisecond)
	}
	r.Stop()
	select {
	case <-done:
		t.Fatal("the first stop only interrupts, which this command ignores")
	case <-time.After(300 * time.Millisecond):
	}
	r.Stop()
	select {
	case code := <-done:
		if code != -9 {
			t.Errorf("killed, exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second stop kills it")
	}
}
