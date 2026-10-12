package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/catalog"
	"testdash/internal/logformat"
	"testdash/internal/progress"
	"testdash/internal/services"
)

func entry(id, parent, kind, class, method, name string) *catalog.Entry {
	return &catalog.Entry{ID: id, Parent: parent, Kind: kind, ClassName: class, Method: method, Name: name}
}

func testModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(ctx, Settings{Workdir: t.TempDir(), RunDir: t.TempDir()})
	m.catalog = &catalog.Catalog{Paths: []string{":test", ":it"}, Tasks: map[string]*catalog.TestTask{
		":test": {Path: ":test", Entries: []*catalog.Entry{
			entry("c1", "", "class", "com.a.FooTest", "", "FooTest"),
			entry("t1", "c1", "test", "com.a.FooTest", "one", "one"),
			entry("t2", "c1", "template", "com.a.FooTest", "each", "each"),
			entry("n1", "c1", "class", "com.a.FooTest$Inner", "", "when inner"),
			entry("t3", "n1", "test", "com.a.FooTest$Inner", "three", "three"),
			entry("c2", "", "class", "com.a.BarTest", "", "BarTest"),
			entry("t4", "c2", "test", "com.a.BarTest", "sign_up", "a sign-up stores a user"),
		}},
		":it": {Path: ":it", Entries: []*catalog.Entry{entry("c9", "", "class", "com.a.ItTest", "", "ItTest"),
			entry("t9", "c9", "test", "com.a.ItTest", "nine", "nine")}},
	}}
	m.chooseTask(":test", true)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	return m
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	}
	if rest, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(rest[0]), Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		m.Update(key(k))
		m.View()
	}
}

func screen(m *Model) string {
	return ansi.Strip(m.View().Content)
}

// clickEverywhere clicks the middle of every zone the last frame drew, redrawing after each; the footer's keys,
// which run and quit, are left alone.
func clickEverywhere(t *testing.T, m *Model) {
	t.Helper()
	zones := append([]zone(nil), m.zones...)
	for _, z := range zones {
		if z.r.y == m.height-1 {
			continue
		}
		m.Update(tea.MouseClickMsg{X: z.r.x + z.r.w/2, Y: z.r.y + z.r.h/2, Button: tea.MouseLeft})
		m.Update(tea.MouseWheelMsg{X: z.r.x + z.r.w/2, Y: z.r.y + z.r.h/2, Button: tea.MouseWheelDown})
		m.Update(tea.MouseMotionMsg{X: z.r.x, Y: z.r.y})
		m.palette = nil
		m.picker.selectOpen = false
		m.View()
	}
}

func TestPickerFiltersAndSelects(t *testing.T) {
	m := testModel(t)
	if s := screen(m); !strings.Contains(s, "FooTest") || !strings.Contains(s, "Run all 4 tests") {
		t.Fatalf("picker:\n%s", s)
	}
	for _, c := range "signup" {
		m.Update(key(string(c)))
	}
	view := m.picker.tests
	view.find(m)
	if !view.visible["t4"] || view.visible["t1"] {
		t.Fatalf("a filter ignoring spaces and punctuation finds the sign-up test: %v", view.visible)
	}
	press(m, "ctrl+a")
	if !m.selected["t4"] || len(m.selected) != 1 {
		t.Fatalf("^a selects what the filter shows: %v", m.selected)
	}
	press(m, "esc")
	if view.text != "" || !view.visible["t1"] {
		t.Fatal("Esc clears the filter")
	}
	press(m, "home", "space")
	if len(m.selected) != 4 || !m.selected["t3"] {
		t.Fatalf("space on a class selects its tests, nested ones too: %v", m.selected)
	}
	if s := screen(m); !strings.Contains(s, "Run 4 tests") {
		t.Fatalf("the button counts the selection:\n%s", s)
	}
	press(m, "ctrl+t", "enter", "down", "enter")
	if m.testTask.Path != ":it" || m.picker.focus != m.picker.tests {
		t.Fatalf("the task dropdown chose %s", m.testTask.Path)
	}
	clickEverywhere(t, m)
}

func TestRunScreenFollowsProgressAndServices(t *testing.T) {
	m := testModel(t)
	m.settings.Command = []string{"true"}
	m.runStarted = time.Now()
	m.progress = progress.New(m.settings.RunDir)
	m.inRun = map[string]bool{}
	m.screen = runScreen
	at := time.Now().UnixMilli() - 2000
	events := fmt.Sprintf(`{"event":"plan","at":1,"entries":[`+
		`{"id":"c1","kind":"class","className":"com.a.FooTest","name":"FooTest"},`+
		`{"id":"t1","parent":"c1","kind":"test","className":"com.a.FooTest","methodName":"one","name":"one()"},`+
		`{"id":"t2","parent":"c1","kind":"template","className":"com.a.FooTest","methodName":"each","name":"each"}]}
{"event":"started","at":%[1]d,"id":"t1"}
{"event":"finished","at":%[2]d,"id":"t1","status":"FAILED","message":"expected: <1> but was: <2>"}
{"event":"registered","at":%[3]d,"entry":{"id":"t2-1","parent":"t2","kind":"test","className":"com.a.FooTest","methodName":"each","name":"[1] a"}}
{"event":"started","at":%[3]d,"id":"t2-1"}
`, at, at+400, at+500)
	if err := os.WriteFile(filepath.Join(m.settings.RunDir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(tickMsg{})
	if s := screen(m); !strings.Contains(s, "expected: <1> but was: <2>") || !strings.Contains(s, "✘ 1") ||
		!strings.Contains(s, "[1] a  1.5s") {
		t.Fatalf("overview:\n%s", s)
	}
	app := services.NewService("orders", "app", "orders:latest")
	m.Update(services.Added{Service: app})
	m.Update(services.Lines{Service: app, Gen: m.gen, Lines: []*logformat.Line{
		logformat.NewLine(time.UnixMilli(at+200), "2026-10-12 15:45:51,006 ERROR [main] c.l.p.Foo - it broke"),
		logformat.NewLine(time.UnixMilli(at+300), "\tat com.a.Foo.bar(Foo.java:1)"),
	}})
	if s := screen(m); !strings.Contains(s, "★ orders") || !strings.Contains(s, "ERROR it broke") {
		t.Fatalf("the application's log is the first tab:\n%s", s)
	}
	if entry := m.serviceEntry(app); entry.problems != 1 {
		t.Errorf("problems %d", entry.problems)
	}
	press(m, "2")
	if m.run.current != "detail-tests" {
		t.Fatalf("2 shows the tests, at %s", m.run.current)
	}
	m.run.tests.cursor = "t1"
	press(m, "l")
	if m.run.current != "detail-svc-orders" {
		t.Fatalf("l shows the application's log during the test, at %s", m.run.current)
	}
	logs := m.serviceEntry(app).full.(*logView)
	if logs.window == nil || len(logs.rows) != 2 {
		t.Fatalf("the log shows the test's time: %v %d", logs.window, len(logs.rows))
	}
	press(m, "/", "b", "a", "r", "enter", "e", "w", "v", "t", "f", "esc", "esc")
	if m.run.current != "overview" {
		t.Fatalf("Esc goes back to the overview once the filter is clear, at %s", m.run.current)
	}
	for _, k := range []string{"tab", "tab", "tab", "enter", "esc", "]", "[", "3", "up", "down", "tab", "ctrl+p", "esc"} {
		press(m, k)
	}
	clickEverywhere(t, m)
	m.run.showView(m, "overview", true)
	m.Update(runDoneMsg{code: 1})
	if s := screen(m); !strings.Contains(s, "FAILED") {
		t.Fatalf("the finished run:\n%s", s)
	}
	press(m, "esc")
	if m.screen != pickerScreen {
		t.Fatal("Esc on the overview goes to the picker")
	}
	press(m, "ctrl+f")
	if !m.selected["t1"] {
		t.Fatalf("^f selects the failed tests: %v", m.selected)
	}
}

func TestPaletteRunsCommands(t *testing.T) {
	m := testModel(t)
	press(m, "ctrl+p", "s", "h", "o", "w", " ", "t", "e", "s", "t", "s", "enter")
	if m.screen != runScreen || m.run.current != "detail-tests" {
		t.Fatalf("the palette shows the tests: screen %v view %s", m.screen, m.run.current)
	}
}

func TestEndpointsGroupIDs(t *testing.T) {
	for url, want := range map[string]string{
		"/orders/42?x=1":           "/orders/{id}",
		"/v2/users/abc123/items":   "/v2/users/{id}/items",
		"/health":                  "/health",
		"/a/550e8400-e29b-41d4-a7": "/a/{id}",
	} {
		if got := endpoint(url); got != want {
			t.Errorf("endpoint(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestDuration(t *testing.T) {
	for millis, want := range map[int64]string{400: "0.4s", 9_950: "9.9s", 12_400: "12s", 75_000: "1:15"} {
		if got := duration(millis); got != want {
			t.Errorf("duration(%d) = %q, want %q", millis, got, want)
		}
	}
}

func TestLogViewScrollsAndFollows(t *testing.T) {
	m := testModel(t)
	var lines []*logformat.Line
	for i := 0; i < 100; i++ {
		lines = append(lines, logformat.NewLine(time.Time{}, strings.Repeat("x", i%7)))
	}
	v := newLogView(func() []*logformat.Line { return lines }, func() string { return "" }, "")
	v.append(m, lines)
	v.visible(40, 10)
	v.scroll(-5)
	if v.follow {
		t.Fatal("scrolling up pauses following")
	}
	v.scroll(100)
	if !v.follow {
		t.Fatal("scrolling back to the end follows again")
	}
	v.wrap = true
	v.append(m, []*logformat.Line{logformat.NewLine(time.Time{}, strings.Repeat("y", 100))})
	if got := v.visible(40, 10); !strings.Contains(ansi.Strip(got[9]), "y") {
		t.Fatalf("a wrapped last line ends the view: %q", got)
	}
}
