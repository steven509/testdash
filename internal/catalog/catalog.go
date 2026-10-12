// Package catalog holds a build's test tasks and their tests, as JUnit Platform discovery reports them (see
// package gradle), cached so the picker opens at once and refreshes in the background.
//
// Entries form the plan's tree: classes (nested classes under their outer class), tests, and test templates such
// as parameterised tests. Tests and templates are what a user picks; a selection becomes Gradle --tests filters.
package catalog

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"testdash/internal/jsonx"
)

type Entry struct {
	ID        string
	Parent    string // empty for a top-level class
	Kind      string // class | test | template
	ClassName string // binary name, so a nested class is Outer$Inner, as --tests matches it
	Method    string // empty for a class
	Name      string // display name
}

// RawEntry is an entry as discovery, the listener and the cache write it.
type RawEntry struct {
	ID         string  `json:"id"`
	Parent     *string `json:"parent"`
	Kind       *string `json:"kind,omitempty"`
	ClassName  *string `json:"className"`
	MethodName *string `json:"methodName"`
	Name       *string `json:"name"`
}

func FromRaw(raw RawEntry) *Entry {
	kind := "test"
	if raw.Kind != nil {
		kind = *raw.Kind
	}
	name := deref(raw.Name)
	if name == "" {
		name = raw.ID
	}
	rawKind := ""
	if raw.Kind != nil {
		rawKind = *raw.Kind
	}
	return &Entry{
		ID:        raw.ID,
		Parent:    deref(raw.Parent),
		Kind:      kind,
		ClassName: deref(raw.ClassName),
		Method:    deref(raw.MethodName),
		Name:      display(name, rawKind),
	}
}

func (e *Entry) raw() RawEntry {
	kind := e.Kind
	return RawEntry{ID: e.ID, Parent: orNil(e.Parent), Kind: &kind, ClassName: &e.ClassName,
		MethodName: orNil(e.Method), Name: &e.Name}
}

func (e *Entry) IsLeaf() bool {
	return e.Kind == "test" || e.Kind == "template"
}

func (e *Entry) ShortClass() string {
	name := e.ClassName[strings.LastIndex(e.ClassName, ".")+1:]
	return strings.ReplaceAll(name, "$", ".")
}

type TestTask struct {
	Path    string // e.g. ":app:blackBoxTest"
	Entries []*Entry
}

// Children maps each entry's id to its children, in order; top-level entries are under "".
func (t *TestTask) Children() map[string][]*Entry {
	ids := make(map[string]bool, len(t.Entries))
	for _, e := range t.Entries {
		ids[e.ID] = true
	}
	tree := map[string][]*Entry{}
	for _, e := range t.Entries {
		parent := e.Parent
		if !ids[parent] {
			parent = ""
		}
		tree[parent] = append(tree[parent], e)
	}
	return tree
}

func (t *TestTask) Leaves() []*Entry {
	var leaves []*Entry
	for _, e := range t.Entries {
		if e.IsLeaf() {
			leaves = append(leaves, e)
		}
	}
	return leaves
}

type Catalog struct {
	Root  string
	Tasks map[string]*TestTask
	Paths []string // the tasks in order
}

func CacheDir() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "testdash")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "testdash")
}

type cacheFile struct {
	Tasks orderedTasks `json:"tasks"`
}

// orderedTasks keeps the cache's task order, which is the order discovery sorted them in.
type orderedTasks struct {
	paths []string
	tasks map[string][]RawEntry
}

func (o *orderedTasks) UnmarshalJSON(data []byte) error {
	o.tasks = map[string][]RawEntry{}
	if err := json.Unmarshal(data, &o.tasks); err != nil {
		return err
	}
	keys, err := jsonx.ObjectKeys(data)
	if err != nil {
		return err
	}
	o.paths = keys
	return nil
}

func (o orderedTasks) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteString("{")
	for i, path := range o.paths {
		if i > 0 {
			b.WriteString(",")
		}
		key, _ := json.Marshal(path)
		value, err := json.Marshal(o.tasks[path])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteString(":")
		b.Write(value)
	}
	b.WriteString("}")
	return []byte(b.String()), nil
}

func Load(root string) *Catalog {
	c := &Catalog{Root: root, Tasks: map[string]*TestTask{}}
	data, err := os.ReadFile(c.file())
	if err != nil {
		return c
	}
	var raw cacheFile
	if json.Unmarshal(data, &raw) != nil {
		return c
	}
	for _, path := range raw.Tasks.paths {
		task := &TestTask{Path: path}
		for _, e := range raw.Tasks.tasks[path] {
			task.Entries = append(task.Entries, FromRaw(e))
		}
		c.Tasks[path] = task
		c.Paths = append(c.Paths, path)
	}
	return c
}

type discovered struct {
	Task    string     `json:"task"`
	Entries []RawEntry `json:"entries"`
}

// UpdateFromDiscovery replaces every task with what a discovery run wrote: one <task>.json per Test task.
func (c *Catalog) UpdateFromDiscovery(outputDir string) {
	files, _ := filepath.Glob(filepath.Join(outputDir, "*.json"))
	sort.Strings(files)
	found := map[string]*TestTask{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var raw discovered
		if json.Unmarshal(data, &raw) != nil || raw.Task == "" {
			continue
		}
		task := &TestTask{Path: raw.Task}
		hasLeaf := false
		for _, e := range raw.Entries {
			entry := FromRaw(e)
			hasLeaf = hasLeaf || entry.IsLeaf()
			task.Entries = append(task.Entries, entry)
		}
		if hasLeaf {
			found[raw.Task] = task
		}
	}
	if len(found) == 0 {
		return
	}
	c.Tasks = found
	c.Paths = c.Paths[:0]
	for path := range found {
		c.Paths = append(c.Paths, path)
	}
	sort.Strings(c.Paths)
	c.save()
}

func (c *Catalog) save() {
	raw := cacheFile{Tasks: orderedTasks{paths: c.Paths, tasks: map[string][]RawEntry{}}}
	for _, path := range c.Paths {
		entries := make([]RawEntry, 0, len(c.Tasks[path].Entries))
		for _, e := range c.Tasks[path].Entries {
			entries = append(entries, e.raw())
		}
		raw.Tasks.tasks[path] = entries
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(c.file()), 0o755) == nil {
		_ = os.WriteFile(c.file(), data, 0o644)
	}
}

func (c *Catalog) file() string {
	root := c.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	sum := sha1.Sum([]byte(root))
	return filepath.Join(CacheDir(), "catalog", hex.EncodeToString(sum[:])[:16]+".json")
}

// GradleFilters returns `--tests` arguments for the selected leaf ids, naming a whole class where all of it is
// selected.
func GradleFilters(task *TestTask, selected map[string]bool) []string {
	tree := task.Children()
	var filters []string
	var leavesUnder func(e *Entry) []*Entry
	leavesUnder = func(e *Entry) []*Entry {
		if e.IsLeaf() {
			return []*Entry{e}
		}
		var leaves []*Entry
		for _, child := range tree[e.ID] {
			leaves = append(leaves, leavesUnder(child)...)
		}
		return leaves
	}
	var visit func(e *Entry)
	visit = func(e *Entry) {
		under := leavesUnder(e)
		picked := 0
		for _, leaf := range under {
			if selected[leaf.ID] {
				picked++
			}
		}
		switch {
		case picked == 0:
		case e.Kind == "class" && picked == len(under):
			filters = append(filters, "--tests", e.ClassName, "--tests", e.ClassName+"$*") // and its @Nested classes
		case e.IsLeaf():
			filters = append(filters, "--tests", e.ClassName+"."+e.Method)
		default:
			for _, child := range tree[e.ID] {
				visit(child)
			}
		}
	}
	for _, top := range tree[""] {
		visit(top)
	}
	return filters
}

// BuildRoot is the nearest directory up from start with a Gradle wrapper, or "".
func BuildRoot(start string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, "gradlew")); err == nil && info.Mode().IsRegular() {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

// display reads `stores values()` as `stores values`, and a template `each(String)` as `each`.
func display(name, kind string) string {
	if strings.HasSuffix(name, "()") {
		return name[:len(name)-2]
	}
	if kind == "template" && strings.HasSuffix(name, ")") && strings.Contains(name, "(") {
		return name[:strings.LastIndex(name, "(")]
	}
	return name
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// MatchTests returns the leaves a run with Gradle's --tests patterns would run, as Gradle matches them: a
// pattern matches a test's class, or its class and method, in full; * matches anything, dots included; and a
// pattern that starts with an upper-case letter also matches simple class names, without their package.
func MatchTests(task *TestTask, patterns []string) map[string]bool {
	var matchers []*regexp.Regexp
	var simple []bool
	for _, pattern := range patterns {
		quoted := regexp.QuoteMeta(pattern)
		matchers = append(matchers, regexp.MustCompile("^"+strings.ReplaceAll(quoted, `\*`, ".*")+"$"))
		first, _ := utf8.DecodeRuneInString(pattern)
		simple = append(simple, unicode.IsUpper(first))
	}
	matched := map[string]bool{}
	for _, e := range task.Entries {
		if !e.IsLeaf() {
			continue
		}
		candidates := []string{e.ClassName, e.ClassName + "." + e.Method}
		shortName := e.ClassName[strings.LastIndex(e.ClassName, ".")+1:]
		for i, matcher := range matchers {
			names := candidates
			if simple[i] {
				names = append(names, shortName, shortName+"."+e.Method)
			}
			for _, name := range names {
				if matcher.MatchString(name) {
					matched[e.ID] = true
				}
			}
		}
	}
	return matched
}
