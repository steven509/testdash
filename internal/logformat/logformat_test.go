package logformat

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// testdata/parsed.json is what the Python testdash made of these lines, which the port must keep.
type parsedLine struct {
	Line    string   `json:"line"`
	Record  []string `json:"record"`
	Problem bool     `json:"problem"`
	Logged  bool     `json:"logged"`
	Simple  string   `json:"simple"`
}

func TestParsesLikeThePythonVersion(t *testing.T) {
	data, err := os.ReadFile("testdata/parsed.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []parsedLine
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		line := NewLine(time.Time{}, c.Line)
		record := line.Record()
		switch {
		case c.Record == nil && record != nil:
			t.Errorf("%q: parsed as %+v, want no record", c.Line, *record)
		case c.Record != nil && record == nil:
			t.Errorf("%q: no record, want %q", c.Line, c.Record)
		case c.Record != nil:
			got := []string{record.Level, record.Message, record.Context}
			if got[0] != c.Record[0] || got[1] != c.Record[1] || got[2] != c.Record[2] {
				t.Errorf("%q: got %q, want %q", c.Line, got, c.Record)
			}
		}
		if got := line.IsProblem(); got != c.Problem {
			t.Errorf("%q: IsProblem %v, want %v", c.Line, got, c.Problem)
		}
		if got := line.IsLoggedProblem(); got != c.Logged {
			t.Errorf("%q: IsLoggedProblem %v, want %v", c.Line, got, c.Logged)
		}
		if got := ansi.Strip(line.Render(true, false)); got != c.Simple {
			t.Errorf("%q: simple render %q, want %q", c.Line, got, c.Simple)
		}
	}
}

func TestSanitizeKeepsStylesAndDropsControls(t *testing.T) {
	for input, want := range map[string]string{
		"plain":                          "plain",
		"\x1b[31mred\x1b[0m":             "\x1b[31mred\x1b[0m",
		"a\x1b[2Kb":                      "ab",
		"title\x1b]0;hi\x07 after":       "title after",
		"carriage\r":                     "carriage",
		"tab\there":                      "tab     here",
		"bell\x07 and \x1b[1;32mx\x1b[m": "bell and \x1b[1;32mx\x1b[m",
	} {
		if got := Sanitize(input); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRenderFullLinesAndTime(t *testing.T) {
	at := time.Date(2026, 10, 12, 15, 45, 51, 0, time.Local)
	line := NewLine(at, "2026-10-12 15:45:51,006 ERROR [main] c.l.p.Foo - it broke")
	full := ansi.Strip(line.Render(false, true))
	if !strings.HasPrefix(full, "15:45:51 2026-10-12") {
		t.Errorf("full line with time = %q", full)
	}
	if untimed := ansi.Strip(NewLine(time.Time{}, "x").Render(true, true)); untimed != strings.Repeat(" ", 9)+"x" {
		t.Errorf("a line without a time keeps the column: %q", untimed)
	}
}
