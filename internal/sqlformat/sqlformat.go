// Package sqlformat lays SQL out over lines with sql-formatter (github.com/sql-formatter-org/sql-formatter), run
// in goja, a JavaScript engine written in Go, as no Go formatter knows MySQL and MariaDB.
//
// sql-formatter.min.js is dist/sql-formatter.min.js of sql-formatter 15.9.0 from npm (sha256 f95ac5bd…a35b58c0),
// under the MIT licence in LICENSE.sql-formatter.
package sqlformat

import (
	_ "embed"
	"errors"
	"sync"
	"time"

	"github.com/dop251/goja"
)

//go:embed sql-formatter.min.js
var bundle string

const (
	maxLength  = 20_000 // longer queries, such as a bulk insert, stay as they are
	timeout    = 2 * time.Second
	cacheLimit = 2_000
)

var (
	errTooLong = errors.New("too long to format")
	errTimeout = errors.New("formatting took too long")
)

type result struct {
	text string
	err  error
}

var (
	load    sync.Once
	loadErr error
	mu      sync.Mutex // a goja runtime runs one call at a time
	vm      *goja.Runtime
	format  goja.Callable
	cache   = map[string]result{}
)

// Prepare loads the formatter in the background, as that takes a moment.
func Prepare() {
	go load.Do(start)
}

func start() {
	runtime := goja.New()
	if _, err := runtime.RunString(bundle); err != nil {
		loadErr = err
		return
	}
	fn, ok := goja.AssertFunction(runtime.Get("sqlFormatter").ToObject(runtime).Get("format"))
	if !ok {
		loadErr = errors.New("sql-formatter has no format function")
		return
	}
	vm, format = runtime, fn
}

// Format lays a query out in a dialect sql-formatter knows, such as mariadb, mysql or postgresql, keeping the case
// of its words. What it cannot parse, and very long queries, come back as an error. Results are kept, so asking
// again for a query on screen costs nothing.
func Format(query, dialect string) (string, error) {
	if len(query) > maxLength {
		return "", errTooLong
	}
	load.Do(start)
	if loadErr != nil {
		return "", loadErr
	}
	mu.Lock()
	defer mu.Unlock()
	key := dialect + "\x00" + query
	r, ok := cache[key]
	if !ok {
		r = run(query, dialect)
		if len(cache) >= cacheLimit {
			cache = map[string]result{}
		}
		cache[key] = r
	}
	return r.text, r.err
}

func run(query, dialect string) result {
	options := vm.NewObject()
	_ = options.Set("language", dialect)
	_ = options.Set("keywordCase", "preserve")
	timer := time.AfterFunc(timeout, func() { vm.Interrupt(errTimeout) })
	value, err := format(goja.Undefined(), vm.ToValue(query), options)
	timer.Stop()
	vm.ClearInterrupt()
	if err != nil {
		return result{err: err}
	}
	return result{text: value.String()}
}
