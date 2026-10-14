package profile

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestParseStats(t *testing.T) {
	line := "\x1b[H\x1b[J" + `{"BlockIO":"934kB / 1.2MB","CPUPerc":"142.50%","Container":"2d5f","ID":"2d5f",` +
		`"MemPerc":"6.4%","MemUsage":"512MiB / 7.746GiB","Name":"shop","NetIO":"5.4kB / 0B","PIDs":"41"}`
	u, ok := ParseStats(line, time.Unix(0, 0))
	if !ok || u.CPU != 142.5 || u.Memory != 512<<20 || u.MemoryLimit != Size("7.746GiB") ||
		u.NetIn != 5400 || u.NetOut != 0 || u.DiskRead != 934000 || u.DiskWritten != 1200000 || u.Processes != 41 {
		t.Errorf("a docker stats line, after the escape codes streaming puts before it: %+v %v", u, ok)
	}
	if _, ok := ParseStats("\x1b[H", time.Now()); ok {
		t.Error("escape codes alone are no sample")
	}
}

// hsperfdata builds a JVM's counters file as HotSpot writes it: a prologue, then entries of a name and a long.
func hsperfdata(order binary.ByteOrder, counters map[string]int64) []byte {
	var entries bytes.Buffer
	for name, value := range counters {
		nameBytes := append([]byte(name), 0)
		for len(nameBytes)%8 != 4 { // the long after it lands on 8 bytes
			nameBytes = append(nameBytes, 0)
		}
		header := make([]byte, 20)
		length := 20 + len(nameBytes) + 8
		order.PutUint32(header[0:], uint32(length))
		order.PutUint32(header[4:], 20)
		order.PutUint32(header[8:], 0)
		header[12] = 'J'
		order.PutUint32(header[16:], uint32(20+len(nameBytes)))
		value8 := make([]byte, 8)
		order.PutUint64(value8, uint64(value))
		entries.Write(header)
		entries.Write(nameBytes)
		entries.Write(value8)
	}
	prologue := make([]byte, 32)
	binary.BigEndian.PutUint32(prologue, hsperfMagic)
	if order == binary.LittleEndian {
		prologue[4] = 1
	}
	order.PutUint32(prologue[24:], 32)
	order.PutUint32(prologue[28:], uint32(len(counters)))
	return append(prologue, entries.Bytes()...)
}

func TestParseJVM(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		data := hsperfdata(order, map[string]int64{
			"sun.os.hrt.frequency":                  1_000_000_000,
			"sun.gc.generation.0.space.0.used":      60 << 20,
			"sun.gc.generation.0.space.1.used":      4 << 20,
			"sun.gc.generation.1.space.0.used":      100 << 20,
			"sun.gc.generation.0.capacity":          128 << 20,
			"sun.gc.generation.1.capacity":          256 << 20,
			"sun.gc.generation.0.maxCapacity":       256 << 20,
			"sun.gc.generation.1.maxCapacity":       768 << 20,
			"sun.gc.metaspace.used":                 90 << 20,
			"sun.gc.collector.0.invocations":        31,
			"sun.gc.collector.0.time":               90_000_000,
			"sun.gc.collector.1.invocations":        2,
			"sun.gc.collector.1.time":               150_000_000,
			"java.threads.live":                     42,
			"java.threads.daemon":                   30,
			"java.cls.loadedClasses":                12000,
			"sun.cls.sharedLoadedClasses":           3000,
			"sun.gc.generation.0.space.0.something": 7,
		})
		j, err := ParseJVM(data, time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		if j.HeapUsed != 164<<20 || j.YoungUsed != 64<<20 || j.OldUsed != 100<<20 || j.HeapCommitted != 384<<20 ||
			j.HeapMax != 1<<30 || j.MetaspaceUsed != 90<<20 || j.GCs() != 33 || j.GCTime() != 240*time.Millisecond ||
			j.Threads != 42 || j.DaemonThreads != 30 || j.ClassesLoaded != 15000 {
			t.Errorf("a %v JVM's heap, collections, threads and classes: %+v", order, j)
		}
	}
	if _, err := ParseJVM([]byte("not counters at all, but long enough to have a prologue"), time.Now()); err == nil {
		t.Error("a file without the magic is not counters")
	}
}

func TestSize(t *testing.T) {
	for text, want := range map[string]int64{"0B": 0, "934kB": 934000, "1.5MiB": 3 << 19, "2GiB": 2 << 30, "12": 12, "": 0} {
		if got := Size(text); got != want {
			t.Errorf("Size(%q) = %d, want %d", text, got, want)
		}
	}
}
