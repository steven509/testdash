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

// hsperfdata builds a JVM's counters file as HotSpot writes it: a prologue, then entries of a name and a long, or
// a name and its text, in a byte array longer than it, as HotSpot leaves room.
func hsperfdata(order binary.ByteOrder, counters map[string]int64, texts map[string]string) []byte {
	var entries bytes.Buffer
	entry := func(name string, kind byte, vector int, value []byte) {
		nameBytes := append([]byte(name), 0)
		for len(nameBytes)%8 != 4 { // the value after it lands on 8 bytes
			nameBytes = append(nameBytes, 0)
		}
		header := make([]byte, 20)
		order.PutUint32(header[0:], uint32(20+len(nameBytes)+len(value)))
		order.PutUint32(header[4:], 20)
		order.PutUint32(header[8:], uint32(vector))
		header[12] = kind
		order.PutUint32(header[16:], uint32(20+len(nameBytes)))
		entries.Write(header)
		entries.Write(nameBytes)
		entries.Write(value)
	}
	for name, value := range counters {
		value8 := make([]byte, 8)
		order.PutUint64(value8, uint64(value))
		entry(name, 'J', 0, value8)
	}
	for name, text := range texts {
		value := make([]byte, len(text)+9)
		copy(value, text)
		entry(name, 'B', len(value), value)
	}
	prologue := make([]byte, 32)
	binary.BigEndian.PutUint32(prologue, hsperfMagic)
	if order == binary.LittleEndian {
		prologue[4] = 1
	}
	order.PutUint32(prologue[24:], 32)
	order.PutUint32(prologue[28:], uint32(len(counters)+len(texts)))
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
			"java.cls.sharedLoadedClasses":          3000,
			"java.cls.unloadedClasses":              20,
			"sun.gc.generation.0.space.0.capacity":  96 << 20,
			"sun.gc.collector.2.invocations":        4,
			"java.threads.started":                  57,
			"sun.rt.safepoints":                     80,
			"sun.ci.totalCompiles":                  5000,
			"sun.rt.createVmBeginTime":              1_700_000_000_000,
			"sun.gc.generation.0.space.0.something": 7,
		}, map[string]string{
			"java.property.java.vm.name":    "OpenJDK 64-Bit Server VM",
			"java.property.java.vm.version": "21.0.4+7-LTS",
			"java.property.java.vm.vendor":  "Eclipse Adoptium",
			"sun.rt.javaCommand":            "com.example.shop.ShopApplication --spring.profiles.active=test",
			"java.rt.vmArgs":                "-Xmx1g",
			"java.rt.vmFlags":               "-XX:+UseG1GC",
			"sun.gc.policy.name":            "GarbageFirst",
			"sun.gc.lastCause":              "G1 Evacuation Pause",
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
		if j.VM != "OpenJDK 64-Bit Server VM 21.0.4+7-LTS (Eclipse Adoptium)" || j.Collector != "GarbageFirst" ||
			j.Command != "com.example.shop.ShopApplication --spring.profiles.active=test" ||
			j.Options != "-Xmx1g -XX:+UseG1GC" || j.LastCause != "G1 Evacuation Pause" ||
			!j.Started.Equal(time.UnixMilli(1_700_000_000_000)) {
			t.Errorf("a %v JVM's text, to the nul HotSpot pads it with: %+v", order, j)
		}
		if j.EdenUsed != 60<<20 || j.EdenCapacity != 96<<20 || j.ConcurrentCycles != 4 || j.ThreadsStarted != 57 ||
			j.ClassesUnloaded != 20 || j.Safepoints != 80 || j.Compiles != 5000 {
			t.Errorf("a %v JVM's eden, cycles, threads started, safepoints and compiles: %+v", order, j)
		}
	}
	if _, err := ParseJVM([]byte("not counters at all, but long enough to have a prologue"), time.Now()); err == nil {
		t.Error("a file without the magic is not counters")
	}
}

func TestAllocation(t *testing.T) {
	started := time.Unix(100, 0)
	at := func(gcs, used int64) JVM {
		return JVM{Started: started, YoungGCs: gcs, EdenUsed: used << 20, EdenCapacity: 100 << 20}
	}
	for _, c := range []struct {
		name          string
		before, after JVM
		want          int64
	}{
		{"eden fills", at(3, 20), at(3, 70), 50 << 20},
		{"a collection empties it", at(3, 70), at(4, 10), (30 + 10) << 20},
		{"three collections, two whole edens", at(3, 70), at(6, 10), (30 + 200 + 10) << 20},
		{"another JVM", at(3, 20), JVM{Started: started.Add(time.Minute), EdenUsed: 70 << 20}, 0},
		{"fewer collections, another JVM", at(3, 20), at(1, 70), 0},
	} {
		if got := Allocation(c.before, c.after); got != c.want {
			t.Errorf("%s: %d MiB, want %d MiB", c.name, got>>20, c.want>>20)
		}
	}
}

func TestSize(t *testing.T) {
	for text, want := range map[string]int64{"0B": 0, "934kB": 934000, "1.5MiB": 3 << 19, "2GiB": 2 << 30, "12": 12, "": 0} {
		if got := Size(text); got != want {
			t.Errorf("Size(%q) = %d, want %d", text, got, want)
		}
	}
}
