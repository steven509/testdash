package profile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

const hsperfMagic = 0xcafec0c0

// Counters reads a JVM's hsperfdata file: its counters by name, the numbers only.
func Counters(data []byte) (map[string]int64, error) {
	counters, _, err := read(data)
	return counters, err
}

// read reads a JVM's hsperfdata file: its numbers, and its text, as its version, by name.
func read(data []byte) (map[string]int64, map[string]string, error) {
	if len(data) < 32 || binary.BigEndian.Uint32(data) != hsperfMagic {
		return nil, nil, errors.New("not a JVM's performance counters")
	}
	var order binary.ByteOrder = binary.BigEndian
	if data[4] == 1 {
		order = binary.LittleEndian
	}
	entryOffset, entries := int(order.Uint32(data[24:])), int(order.Uint32(data[28:]))
	counters, texts := make(map[string]int64, entries), map[string]string{}
	for i, at := 0, entryOffset; i < entries && at+20 <= len(data); i++ {
		length := int(order.Uint32(data[at:]))
		nameAt, vector := at+int(order.Uint32(data[at+4:])), order.Uint32(data[at+8:])
		kind, dataAt := data[at+12], at+int(order.Uint32(data[at+16:]))
		if length <= 0 || nameAt >= len(data) {
			break
		}
		name := data[nameAt:]
		if end := bytes.IndexByte(name, 0); end >= 0 {
			name = name[:end]
		}
		switch {
		case kind == 'J' && vector == 0 && dataAt+8 <= len(data):
			counters[string(name)] = int64(order.Uint64(data[dataAt:]))
		case kind == 'B' && vector > 0 && dataAt+int(vector) <= len(data):
			text := data[dataAt : dataAt+int(vector)]
			if end := bytes.IndexByte(text, 0); end >= 0 {
				text = text[:end]
			}
			texts[string(name)] = string(text)
		}
		at += length
	}
	return counters, texts, nil
}

// ParseJVM reads the counters a JVM's heap, collections, threads and classes are made of. A collector's time is in
// ticks of the JVM's clock, whose frequency is a counter too.
func ParseJVM(data []byte, at time.Time) (JVM, error) {
	c, t, err := read(data)
	if err != nil {
		return JVM{}, err
	}
	ticks := func(name string) time.Duration {
		frequency := c["sun.os.hrt.frequency"]
		if frequency <= 0 {
			return 0
		}
		return time.Duration(float64(c[name]) / float64(frequency) * float64(time.Second))
	}
	vm := strings.TrimSpace(t["java.property.java.vm.name"] + " " + t["java.property.java.vm.version"])
	if vendor := t["java.property.java.vm.vendor"]; vendor != "" {
		vm += " (" + vendor + ")"
	}
	j := JVM{At: at, VM: vm, Command: t["sun.rt.javaCommand"],
		Options:       strings.TrimSpace(t["java.rt.vmArgs"] + " " + t["java.rt.vmFlags"]),
		MetaspaceUsed: c["sun.gc.metaspace.used"], MetaspaceCommitted: c["sun.gc.metaspace.capacity"],
		Collector: t["sun.gc.policy.name"], LastCause: t["sun.gc.lastCause"],
		GCTimeLimitExceeded: c["sun.gc.policy.gcTimeLimitExceeded"] > 0,
		ConcurrentCycles:    c["sun.gc.collector.2.invocations"],
		Threads:             c["java.threads.live"], DaemonThreads: c["java.threads.daemon"], Peak: c["java.threads.livePeak"],
		ThreadsStarted:  c["java.threads.started"],
		ClassesLoaded:   c["java.cls.loadedClasses"] + c["java.cls.sharedLoadedClasses"],
		ClassesUnloaded: c["java.cls.unloadedClasses"] + c["java.cls.sharedUnloadedClasses"],
		Safepoints:      c["sun.rt.safepoints"], SafepointTime: ticks("sun.rt.safepointTime"),
		Compiles: c["sun.ci.totalCompiles"],
		EdenUsed: c["sun.gc.generation.0.space.0.used"], EdenCapacity: c["sun.gc.generation.0.space.0.capacity"]}
	if started := c["sun.rt.createVmBeginTime"]; started > 0 {
		j.Started = time.UnixMilli(started)
	}
	for name, value := range c {
		if !strings.HasPrefix(name, "sun.gc.generation.") {
			continue
		}
		generation, rest, _ := strings.Cut(strings.TrimPrefix(name, "sun.gc.generation."), ".")
		switch {
		case strings.HasPrefix(rest, "space.") && strings.HasSuffix(rest, ".used"):
			j.HeapUsed += value
			if generation == "0" {
				j.YoungUsed += value
			} else {
				j.OldUsed += value
			}
		case rest == "capacity":
			j.HeapCommitted += value
		case rest == "maxCapacity":
			j.HeapMax += value
		}
	}
	// the young collector is the first; the others are of the old generation, or for G1, concurrent cycles
	j.YoungGCs, j.YoungGCTime = c["sun.gc.collector.0.invocations"], ticks("sun.gc.collector.0.time")
	j.OldGCs, j.OldGCTime = c["sun.gc.collector.1.invocations"], ticks("sun.gc.collector.1.time")
	return j, nil
}
