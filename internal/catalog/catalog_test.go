package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func entries() []*Entry {
	raw := []string{
		`{"id": "c1", "parent": null, "kind": "class", "className": "com.a.FooTest", "name": "FooTest"}`,
		`{"id": "t1", "parent": "c1", "kind": "test", "className": "com.a.FooTest", "methodName": "one", "name": "one()"}`,
		`{"id": "t2", "parent": "c1", "kind": "template", "className": "com.a.FooTest", "methodName": "each", "name": "each(String)"}`,
		`{"id": "n1", "parent": "c1", "kind": "class", "className": "com.a.FooTest$Inner", "name": "Inner"}`,
		`{"id": "t3", "parent": "n1", "kind": "test", "className": "com.a.FooTest$Inner", "methodName": "three", "name": "three()"}`,
		`{"id": "c2", "parent": null, "kind": "class", "className": "com.a.BarTest", "name": "BarTest"}`,
		`{"id": "t4", "parent": "c2", "kind": "test", "className": "com.a.BarTest", "methodName": "four", "name": "four()"}`,
	}
	var out []*Entry
	for _, r := range raw {
		var e RawEntry
		if err := json.Unmarshal([]byte(r), &e); err != nil {
			panic(err)
		}
		out = append(out, FromRaw(e))
	}
	return out
}

func set(ids ...string) map[string]bool {
	s := map[string]bool{}
	for _, id := range ids {
		s[id] = true
	}
	return s
}

func TestGradleFilters(t *testing.T) {
	task := &TestTask{Path: ":test", Entries: entries()}
	for name, c := range map[string]struct {
		selected map[string]bool
		want     []string
	}{
		"one test":            {set("t1"), []string{"--tests", "com.a.FooTest.one"}},
		"a whole class":       {set("t1", "t2", "t3"), []string{"--tests", "com.a.FooTest", "--tests", "com.a.FooTest$*"}},
		"a nested class":      {set("t3"), []string{"--tests", "com.a.FooTest$Inner", "--tests", "com.a.FooTest$Inner$*"}},
		"a template, a class": {set("t2", "t4"), []string{"--tests", "com.a.FooTest.each", "--tests", "com.a.BarTest", "--tests", "com.a.BarTest$*"}},
	} {
		if got := GradleFilters(task, c.selected); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestDisplayNames(t *testing.T) {
	var names []string
	for _, e := range entries() {
		names = append(names, e.Name)
	}
	want := []string{"FooTest", "one", "each", "Inner", "three", "BarTest", "four"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names %q, want %q", names, want)
	}
	if short := entries()[3].ShortClass(); short != "FooTest.Inner" {
		t.Errorf("short class %q", short)
	}
	if name := display("quotes each country (String, String)", "template"); name != "quotes each country" {
		t.Errorf("a template's name with underscores replaced is %q", name)
	}
}

func TestDiscoveryIsCachedInTaskOrder(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root, out := t.TempDir(), t.TempDir()
	write := func(name, task string) {
		var raw []json.RawMessage
		for _, e := range entries() {
			data, _ := json.Marshal(e.raw())
			raw = append(raw, data)
		}
		data, _ := json.Marshal(map[string]any{"task": task, "entries": raw})
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("_app_test.json", ":app:test")
	write("_app_blackBoxTest.json", ":app:blackBoxTest")
	c := Load(root)
	c.UpdateFromDiscovery(out)
	loaded := Load(root)
	if !reflect.DeepEqual(loaded.Paths, []string{":app:blackBoxTest", ":app:test"}) {
		t.Fatalf("paths %q", loaded.Paths)
	}
	if got := len(loaded.Tasks[":app:test"].Leaves()); got != 4 {
		t.Errorf("%d leaves, want 4", got)
	}
	if got := loaded.Tasks[":app:test"].Entries[2].Name; got != "each" {
		t.Errorf("cached name %q", got)
	}
}

func TestBuildRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gradlew"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := BuildRoot(nested); got != root {
		t.Errorf("BuildRoot = %q, want %q", got, root)
	}
}

func TestMatchTestsLikeGradle(t *testing.T) {
	task := &TestTask{Path: ":test", Entries: entries()}
	for _, c := range []struct {
		patterns []string
		want     []string
	}{
		{[]string{"com.a.FooTest"}, []string{"t1", "t2"}},
		{[]string{"com.a.FooTest.one"}, []string{"t1"}},
		{[]string{"FooTest"}, []string{"t1", "t2"}},
		{[]string{"*Inner"}, []string{"t3"}},
		{[]string{"com.a.*"}, []string{"t1", "t2", "t3", "t4"}},
		{[]string{"*.four", "BarTest"}, []string{"t4"}},
		{[]string{"fooTest"}, nil},
	} {
		got := MatchTests(task, c.patterns)
		var ids []string
		for _, id := range []string{"t1", "t2", "t3", "t4"} {
			if got[id] {
				ids = append(ids, id)
			}
		}
		if !reflect.DeepEqual(ids, c.want) {
			t.Errorf("%q matched %v, want %v", c.patterns, ids, c.want)
		}
	}
}

// cucumberEntries are a Cucumber suite as the listener writes it: the suite class, its features, a rule and a
// scenario outline as groups, and scenarios with their lines, beside a JUnit class.
func cucumberEntries() []*Entry {
	checkout, refunds := `"file": "classpath:features/checkout.feature"`, `"file": "classpath:features/refunds.feature"`
	raw := []string{
		`{"id": "s", "parent": null, "kind": "class", "className": "com.a.RunCucumberTests", "name": "RunCucumberTests"}`,
		`{"id": "f1", "parent": "s", "kind": "class", ` + checkout + `, "name": "Checkout"}`,
		`{"id": "a", "parent": "f1", "kind": "test", ` + checkout + `, "line": 6, "name": "A cart is checked out"}`,
		`{"id": "r", "parent": "f1", "kind": "class", ` + checkout + `, "line": 12, "name": "Carts take known products"}`,
		`{"id": "b", "parent": "r", "kind": "test", ` + checkout + `, "line": 14, "name": "An unknown product"}`,
		`{"id": "o", "parent": "f1", "kind": "class", ` + checkout + `, "line": 20, "name": "Delivery is quoted"}`,
		`{"id": "e", "parent": "o", "kind": "class", ` + checkout + `, "line": 24, "name": "Examples"}`,
		`{"id": "c", "parent": "e", "kind": "test", ` + checkout + `, "line": 26, "name": "Example #1.1"}`,
		`{"id": "d", "parent": "e", "kind": "test", ` + checkout + `, "line": 27, "name": "Example #1.2"}`,
		`{"id": "f2", "parent": "s", "kind": "class", ` + refunds + `, "name": "Refunds"}`,
		`{"id": "g", "parent": "f2", "kind": "test", ` + refunds + `, "line": 3, "name": "A refund"}`,
		`{"id": "j", "parent": null, "kind": "class", "className": "com.a.FooTest", "name": "FooTest"}`,
		`{"id": "m", "parent": "j", "kind": "test", "className": "com.a.FooTest", "methodName": "one", "name": "one()"}`,
		`{"id": "n", "parent": "j", "kind": "test", "className": "com.a.FooTest", "methodName": "two", "name": "two()"}`,
	}
	var out []*Entry
	for _, r := range raw {
		var e RawEntry
		if err := json.Unmarshal([]byte(r), &e); err != nil {
			panic(err)
		}
		out = append(out, FromRaw(e))
	}
	return out
}

func TestGradleFiltersForCucumber(t *testing.T) {
	task := &TestTask{Path: ":test", Entries: cucumberEntries()}
	suite, inSuite := []string{"--tests", "com.a.RunCucumberTests"}, "-Ptestdash.cucumberInSuite=true"
	for name, c := range map[string]struct {
		selected map[string]bool
		want     []string
	}{
		"scenarios": {set("a", "c"),
			append(suite, "-Ptestdash.cucumberFeatures=classpath:features/checkout.feature:6:26", inSuite)},
		"an outline and a whole feature": {set("c", "d", "g"),
			append(suite, "-Ptestdash.cucumberFeatures=classpath:features/checkout.feature:26:27,classpath:features/refunds.feature", inSuite)},
		"the whole suite": {set("a", "b", "c", "d", "g"),
			[]string{"--tests", "com.a.RunCucumberTests", "--tests", "com.a.RunCucumberTests$*"}},
		"a test beside a scenario": {set("m", "b"),
			append([]string{"--tests", "com.a.FooTest.one"}, append(suite,
				"-Ptestdash.cucumberFeatures=classpath:features/checkout.feature:14", inSuite)...)},
	} {
		if got := GradleFilters(task, c.selected); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if matched := MatchTests(task, []string{"RunCucumberTests"}); len(matched) != 5 || !matched["g"] {
		t.Errorf("--tests on the suite matches its scenarios: %v", matched)
	}
	if name := task.Entries[1].ShortClass(); name != "Checkout" {
		t.Errorf("a feature is named by its name, not %q", name)
	}
}
