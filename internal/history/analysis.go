package history

import "sort"

const (
	window      = 20  // the runs a test's summary looks back over
	usualPasses = 10  // the passes its usual time is the median of
	slowerBy    = 2.0 // a run this many times its usual time is slower...
	slowerMs    = 200 // ...when that is also this much longer
)

// Point is a test's result in a run.
type Point struct {
	Run    *Run
	Result Result
}

// Index is each test's results, newest first.
type Index map[string][]Point

// NewIndex indexes runs, given newest first.
func NewIndex(runs []Run) Index {
	index := Index{}
	for i := range runs {
		for _, result := range runs[i].Tests {
			index[result.ID] = append(index[result.ID], Point{Run: &runs[i], Result: result})
		}
	}
	return index
}

// Summary is what a test's results say about it, the newest being the latest run.
type Summary struct {
	Flaky      bool  // it passed when retried, or passed and failed on one commit with a clean working tree
	FailingFor int   // how many runs in a row it has failed, the latest included
	Since      *Run  // the first of those runs
	NewFailure bool  // it failed in the latest run, and passed in the one before
	Fixed      bool  // it passed in the latest run, and failed in the one before
	Usually    int64 // the median time of its passes before the latest, in millis; 0 with fewer than 3
	Slower     bool  // the latest pass took more than twice as long as usually, and 200ms more
}

func Summarise(points []Point) Summary {
	var s Summary
	if len(points) == 0 {
		return s
	}
	points = points[:min(len(points), window)]
	s.Flaky = flaky(points)
	for _, p := range points {
		if !failed(p.Result.Status) {
			break
		}
		s.FailingFor++
		s.Since = p.Run
	}
	if len(points) > 1 {
		latest, before := points[0].Result.Status, points[1].Result.Status
		s.NewFailure = failed(latest) && before == "passed"
		s.Fixed = latest == "passed" && failed(before)
	}
	var passes []int64
	for _, p := range points[1:] {
		if p.Result.Status == "passed" && len(passes) < usualPasses {
			passes = append(passes, p.Result.Took)
		}
	}
	if len(passes) >= 3 {
		sort.Slice(passes, func(i, j int) bool { return passes[i] < passes[j] })
		s.Usually = passes[len(passes)/2]
		took := points[0].Result.Took
		s.Slower = points[0].Result.Status == "passed" && float64(took) > slowerBy*float64(s.Usually) &&
			took-s.Usually > slowerMs
	}
	return s
}

func flaky(points []Point) bool {
	type outcome struct{ passed, failed bool }
	byCommit := map[string]*outcome{}
	for _, p := range points {
		if p.Result.Retried {
			return true
		}
		if p.Run.Commit == "" || p.Run.Dirty {
			continue
		}
		o := byCommit[p.Run.Commit]
		if o == nil {
			o = &outcome{}
			byCommit[p.Run.Commit] = o
		}
		o.passed = o.passed || p.Result.Status == "passed"
		o.failed = o.failed || failed(p.Result.Status)
		if o.passed && o.failed {
			return true
		}
	}
	return false
}

func failed(status string) bool {
	return status == "failed" || status == "aborted"
}
