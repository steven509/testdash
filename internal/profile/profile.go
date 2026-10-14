// Package profile samples how hard an application works while tests run: its container's CPU, memory, network and
// disk from docker stats, and for a JVM, its heap, garbage collection, threads and classes from the performance
// counters every HotSpot JVM writes to /tmp/hsperfdata_<user>/<pid>, as jstat reads them, with nothing to set up.
package profile

import (
	"time"
)

// Usage is a container's resources at a moment, as docker stats reports them.
type Usage struct {
	At                    time.Time
	CPU                   float64 // percent of one CPU, so above 100 with more than one in use
	Memory, MemoryLimit   int64   // bytes
	NetIn, NetOut         int64   // bytes since the container started
	DiskRead, DiskWritten int64   // bytes since the container started
	Processes             int
}

// JVM is a JVM's counters at a moment.
type JVM struct {
	At                           time.Time
	VM, Command, Options         string    // its name, version and vendor; main class and arguments; JVM options
	Started                      time.Time // when the JVM started
	HeapUsed, HeapCommitted      int64     // bytes, every generation
	HeapMax                      int64
	YoungUsed, OldUsed           int64
	EdenUsed, EdenCapacity       int64
	MetaspaceUsed                int64
	MetaspaceCommitted           int64
	Collector                    string // the collector, as GarbageFirst
	YoungGCs, OldGCs             int64  // collections since the JVM started, the old ones full collections
	YoungGCTime, OldGCTime       time.Duration
	ConcurrentCycles             int64  // G1's and ZGC's concurrent cycles
	LastCause                    string // what caused the last collection
	GCTimeLimitExceeded          bool   // the Parallel collector spends nearly all its time collecting
	Threads, DaemonThreads, Peak int64
	ThreadsStarted               int64
	ClassesLoaded                int64
	ClassesUnloaded              int64
	Safepoints                   int64
	SafepointTime                time.Duration
	Compiles                     int64 // methods the JIT compiled
	// Allocated is how much the JVM has allocated since sampling began, as its eden filled; sampling works it out.
	Allocated int64
}

// GCTime is the time the JVM has spent collecting garbage.
func (j JVM) GCTime() time.Duration {
	return j.YoungGCTime + j.OldGCTime
}

// Allocation is how much a JVM allocated between two samples, as its eden filled: what eden gained, or with
// collections between them, what was left in eden before them, each eden the collections emptied after the first,
// and what eden holds now. Nothing when the later sample is another JVM's.
func Allocation(before, after JVM) int64 {
	if after.YoungGCs < before.YoungGCs || !after.Started.Equal(before.Started) {
		return 0
	}
	collections := after.YoungGCs - before.YoungGCs
	if collections == 0 {
		return max(0, after.EdenUsed-before.EdenUsed)
	}
	return max(0, before.EdenCapacity-before.EdenUsed) + (collections-1)*after.EdenCapacity + after.EdenUsed
}

// GCs is how many collections the JVM has made.
func (j JVM) GCs() int64 {
	return j.YoungGCs + j.OldGCs
}
