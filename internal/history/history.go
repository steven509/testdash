// Package history keeps what each run did, to say what changed since: tests that fail anew, are fixed, got slower
// or are flaky. Each run is a gzipped JSON file under the cache directory, one directory per build; the newest 200
// runs of the last 90 days are kept.
package history

import (
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	keepRuns = 200
	keepFor  = 90 * 24 * time.Hour
)

// Run is one run: what ran, how it ended, the code it ran against, and each test's result.
type Run struct {
	Started time.Time `json:"started"`
	Took    int64     `json:"took"` // millis
	Task    string    `json:"task"`
	Filters []string  `json:"filters,omitempty"` // the --tests filters; none ran every test
	Exit    int       `json:"exit"`
	Stopped bool      `json:"stopped,omitempty"`
	Commit  string    `json:"commit,omitempty"`
	Dirty   bool      `json:"dirty,omitempty"` // the working tree had changes
	Tests   []Result  `json:"tests"`
}

// Result is a test's result in a run.
type Result struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"` // as the run had it: its classes, then its name
	Status  string `json:"status"`         // passed, failed, aborted or skipped
	Took    int64  `json:"took,omitempty"`
	Retried bool   `json:"retried,omitempty"` // failed, then passed when run again in the same run
	Failure string `json:"failure,omitempty"` // the failure's first line
}

// Store is one build's runs.
type Store struct {
	dir string
}

// Open is the store for the build at root, under cacheDir.
func Open(cacheDir, root string) *Store {
	sum := sha1.Sum([]byte(root))
	return &Store{dir: filepath.Join(cacheDir, "history", hex.EncodeToString(sum[:])[:16])}
}

// Save writes a run, and drops the runs past keeping.
func (s *Store) Save(run Run) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "run.*.tmp")
	if err != nil {
		return err
	}
	zipped := gzip.NewWriter(tmp)
	err = json.NewEncoder(zipped).Encode(run)
	if closeErr := zipped.Close(); err == nil {
		err = closeErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(s.dir, fileName(run.Started)))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	s.prune()
	return nil
}

// Load reads the newest runs, newest first.
func (s *Store) Load(limit int) []Run {
	names := s.files()
	var runs []Run
	for i := len(names) - 1; i >= 0 && len(runs) < limit; i-- {
		if run, ok := read(filepath.Join(s.dir, names[i])); ok {
			runs = append(runs, run)
		}
	}
	return runs
}

func (s *Store) prune() {
	names := s.files()
	cutoff := fileName(time.Now().Add(-keepFor))
	for i, name := range names {
		if i < len(names)-keepRuns || name < cutoff {
			os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// files are the runs' file names, oldest first: they sort by time.
func (s *Store) files() []string {
	entries, _ := os.ReadDir(s.dir)
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json.gz") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func fileName(started time.Time) string {
	return started.UTC().Format("20060102T150405.000Z") + ".json.gz"
}

func read(path string) (Run, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Run{}, false
	}
	defer f.Close()
	unzipped, err := gzip.NewReader(f)
	if err != nil {
		return Run{}, false
	}
	var run Run
	if json.NewDecoder(unzipped).Decode(&run) != nil {
		return Run{}, false
	}
	return run, true
}
