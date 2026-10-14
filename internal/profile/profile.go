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
	HeapUsed, HeapCommitted      int64 // bytes, every generation
	HeapMax                      int64
	YoungUsed, OldUsed           int64
	MetaspaceUsed                int64
	YoungGCs, OldGCs             int64 // collections since the JVM started
	YoungGCTime, OldGCTime       time.Duration
	Threads, DaemonThreads, Peak int64
	ClassesLoaded                int64
	SafepointTime                time.Duration
	CompileTime                  time.Duration
}

// GCTime is the time the JVM has spent collecting garbage.
func (j JVM) GCTime() time.Duration {
	return j.YoungGCTime + j.OldGCTime
}

// GCs is how many collections the JVM has made.
func (j JVM) GCs() int64 {
	return j.YoungGCs + j.OldGCs
}
