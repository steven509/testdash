package noise

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestFilter(t *testing.T) {
	f, err := New([]Rule{{Service: "wiremock", Match: `^GET /health`}}, []Rule{{Match: `(?i)hikari`}, {Match: `(`}})
	if err == nil || !strings.Contains(err.Error(), `noise rule "("`) {
		t.Errorf("a rule that does not compile says so: %v", err)
	}
	for _, c := range []struct {
		service, text string
		want          bool
	}{{"wiremock", "GET /health 200", true}, {"shop", "GET /health", false}, {"shop", "HikariPool-1 housekeeping", true},
		{"postgres", "SELECT 1", false}} {
		if got := f.Noise(c.service, c.text); got != c.want {
			t.Errorf("%s %q: %v", c.service, c.text, got)
		}
	}
	var none *Filter
	if none.Noise("x", "y") {
		t.Error("no filter, no noise")
	}
}

func TestSuggest(t *testing.T) {
	for text, want := range map[string]string{
		"GET /orders/1042 200":                       `^GET /orders/\d+ \d+$`,
		"SELECT * FROM t WHERE id = 7":               `^SELECT \* FROM t WHERE id = \d+$`,
		"Trace 4bf92f3577b34da6 done\nat Foo.java:1": `^Trace [0-9a-f]+ done`,
		"cart:deadbeef12":                            `^cart:[0-9a-f]+$`,
	} {
		got := Suggest(text)
		if got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
		if !regexp.MustCompile(got).MatchString(strings.Split(text, "\n")[0]) {
			t.Errorf("%s does not match %q", got, text)
		}
	}
}

func TestConfigKeepsTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".testdash.json")
	original := "{\n  \"task\": \":app:test\",\n  \"logs\": [{\"name\": \"app\", \"file\": \"app.log\"}],\n  \"command\": \"make\"\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(path, []Rule{{Service: "postgres", Match: "^SET "}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := `{
  "task": ":app:test",
  "logs": [
    {
      "name": "app",
      "file": "app.log"
    }
  ],
  "command": "make",
  "noise": [
    {
      "service": "postgres",
      "match": "^SET "
    }
  ]
}
`
	if string(data) != want {
		t.Errorf("the rest stays, in its order:\n%s", data)
	}
	rules, err := LoadConfig(path)
	if err != nil || len(rules) != 1 || rules[0].Match != "^SET " {
		t.Errorf("and the rules read back: %+v %v", rules, err)
	}
	fresh := filepath.Join(t.TempDir(), ".testdash.json")
	if err := SaveConfig(fresh, []Rule{{Match: "x"}}); err != nil {
		t.Fatal(err)
	}
	if rules, _ := LoadConfig(fresh); len(rules) != 1 {
		t.Error("a build without a config gets one")
	}
}
