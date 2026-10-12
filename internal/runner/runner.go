// Package runner runs a command in its own session and process group and streams its output in batches of
// lines.
package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Runner struct {
	mu      sync.Mutex
	process *os.Process
	running bool
	stops   int
}

func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Run runs command in cwd until it exits, feeding its output (stdout and stderr together) to onLines, and
// returns its exit code: negative for the signal that ended it, 127 when it could not start.
func (r *Runner) Run(command []string, cwd string, onLines func([]string)) int {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	// A process it started that keeps the output open (a daemon, say) must not keep the run going.
	cmd.WaitDelay = 2 * time.Second
	r.mu.Lock()
	r.stops = 0
	if err := cmd.Start(); err != nil {
		r.mu.Unlock()
		writer.Close()
		onLines([]string{"testdash: cannot run " + command[0] + ": " + err.Error()})
		return 127
	}
	r.process, r.running = cmd.Process, true
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		StreamLines(reader, onLines)
		close(done)
	}()
	_ = cmd.Wait()
	writer.Close()
	<-done
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
	return exitCode(cmd.ProcessState)
}

func exitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return -int(status.Signal())
	}
	return state.ExitCode()
}

// Stop interrupts the command (Gradle cancels its build cleanly); a second Stop kills it.
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return
	}
	r.stops++
	signal := syscall.SIGINT
	if r.stops > 1 {
		signal = syscall.SIGKILL
	}
	_ = syscall.Kill(-r.process.Pid, signal)
}

// Kill kills the command's whole process group at once.
func (r *Runner) Kill() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		_ = syscall.Kill(-r.process.Pid, syscall.SIGKILL)
	}
}

const (
	batchWait  = 50 * time.Millisecond
	batchLines = 1000
)

// StreamLines reads lines until the reader ends, handing them over in batches: what arrived within 50ms, or
// 1000 lines, whichever comes first. Line endings, including a carriage return, are removed.
func StreamLines(reader io.Reader, emit func([]string)) {
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		buffered := bufio.NewReaderSize(reader, 64*1024)
		for {
			line, err := buffered.ReadString('\n')
			if line != "" {
				lines <- strings.TrimRight(line, "\r\n")
			}
			if err != nil {
				return
			}
		}
	}()
	var batch []string
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				if len(batch) > 0 {
					emit(batch)
				}
				return
			}
			if len(batch) == 0 {
				timer.Reset(batchWait)
			}
			batch = append(batch, line)
			if len(batch) >= batchLines {
				timer.Stop()
				emit(batch)
				batch = nil
			}
		case <-timer.C:
			if len(batch) > 0 {
				emit(batch)
				batch = nil
			}
		}
	}
}

// Batched runs a command that prints one item per line and hands the items over in batches: what arrived
// within 0.3s of the first, or 500. It stops early, killing the command, once current reports false. It returns
// whether any item came, the command's stderr, and its exit code.
func Batched[T any](ctx context.Context, cmd *exec.Cmd, parse func([]byte) (T, bool), current func() bool,
	emit func([]T)) (bool, string, int) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, "", 1
	}
	if err := cmd.Start(); err != nil {
		return false, err.Error(), 1
	}
	lines := make(chan []byte, 256)
	go func() {
		defer close(lines)
		reader := bufio.NewReaderSize(stdout, 64*1024)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				lines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	gotAny := false
	var batch []T
	flush := func() {
		if len(batch) > 0 {
			gotAny = true
			emit(batch)
			batch = nil
		}
	}
	for done := false; !done; {
		wait := time.Second
		if len(batch) > 0 {
			wait = 300 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case line, ok := <-lines:
			timer.Stop()
			if !ok {
				done = true
				break
			}
			if item, ok := parse(line); ok {
				batch = append(batch, item)
				if len(batch) < 500 {
					continue
				}
			}
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return gotAny, stderr.String(), 1
		}
		if !current() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return gotAny, stderr.String(), 1
		}
		flush()
	}
	flush()
	err = cmd.Wait()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		code = 1
	}
	return gotAny, stderr.String(), code
}
