package profile

import (
	"bufio"
	"context"
	"os/exec"
	"path"
	"strings"
	"time"
)

// JVMEvery is how often the JVM's counters are read: a docker exec each time.
const JVMEvery = 2 * time.Second

// Follow samples a container until ctx ends or still says it is no longer the one to follow: its usage as docker
// stats streams it, about each second, and when it runs a JVM, the JVM's counters every two seconds.
func Follow(ctx context.Context, container string, still func() bool, usage func(Usage), jvm func(JVM)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if !still() {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	go followJVM(ctx, container, jvm)
	stats := exec.CommandContext(ctx, "docker", "stats", "--no-trunc", "--format", "{{json .}}", container)
	out, err := stats.StdoutPipe()
	if err != nil || stats.Start() != nil {
		return
	}
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		if u, ok := ParseStats(scanner.Text(), time.Now()); ok {
			usage(u)
		}
	}
	_ = stats.Wait()
}

// followJVM reads the JVM's counters while there is a JVM to read, looking for one again now and then, as the
// application may start its JVM some time after its container, and adds up what it allocates.
func followJVM(ctx context.Context, container string, jvm func(JVM)) {
	file, last := "", JVM{}
	for wait := time.Duration(0); ; wait = JVMEvery {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if file == "" {
			if file = CountersFile(ctx, container); file == "" {
				wait = 5 * time.Second
				continue
			}
		}
		data, err := exec.CommandContext(ctx, "docker", "exec", container, "cat", file).Output()
		j, parseErr := ParseJVM(data, time.Now())
		if err != nil || parseErr != nil {
			file = "" // the JVM went, or another took its place
			continue
		}
		if !last.At.IsZero() && last.Started.Equal(j.Started) {
			j.Allocated = last.Allocated + Allocation(last, j)
		}
		jvm(j)
		last = j
	}
}

// CountersFile finds the JVM's counters in a container: the file of process 1 when the JVM is the container's
// process, else the first JVM's. Empty when there is none, or no shell to look with.
func CountersFile(ctx context.Context, container string) string {
	out, err := exec.CommandContext(ctx, "docker", "exec", container, "sh", "-c",
		"ls -1 /tmp/hsperfdata_*/* 2>/dev/null").Output()
	if err != nil {
		return ""
	}
	files := strings.Fields(string(out))
	for _, file := range files {
		if path.Base(file) == "1" {
			return file
		}
	}
	if len(files) > 0 {
		return files[0]
	}
	return ""
}

// ThreadDump asks the container's JVM for a thread dump, which it writes to its output, so to the application's
// log: SIGQUIT to the JVM's process, as its counters file is named after it, else to the container's.
func ThreadDump(ctx context.Context, container string) error {
	if file := CountersFile(ctx, container); file != "" && path.Base(file) != "1" {
		return exec.CommandContext(ctx, "docker", "exec", container, "kill", "-QUIT", path.Base(file)).Run()
	}
	return exec.CommandContext(ctx, "docker", "kill", "--signal", "QUIT", container).Run()
}
