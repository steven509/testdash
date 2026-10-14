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
	if len(data) < 32 || binary.BigEndian.Uint32(data) != hsperfMagic {
		return nil, errors.New("not a JVM's performance counters")
	}
	var order binary.ByteOrder = binary.BigEndian
	if data[4] == 1 {
		order = binary.LittleEndian
	}
	entryOffset, entries := int(order.Uint32(data[24:])), int(order.Uint32(data[28:]))
	counters := make(map[string]int64, entries)
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
		if kind == 'J' && vector == 0 && dataAt+8 <= len(data) {
			counters[string(name)] = int64(order.Uint64(data[dataAt:]))
		}
		at += length
	}
	return counters, nil
}

// ParseJVM reads the counters a JVM's heap, collections, threads and classes are made of. A collector's time is in
// ticks of the JVM's clock, whose frequency is a counter too.
func ParseJVM(data []byte, at time.Time) (JVM, error) {
	c, err := Counters(data)
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
	j := JVM{At: at, MetaspaceUsed: c["sun.gc.metaspace.used"], Threads: c["java.threads.live"],
		DaemonThreads: c["java.threads.daemon"], Peak: c["java.threads.livePeak"],
		ClassesLoaded: c["java.cls.loadedClasses"] + c["sun.cls.sharedLoadedClasses"],
		SafepointTime: ticks("sun.rt.safepointTime"), CompileTime: ticks("sun.ci.totalTime")}
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
