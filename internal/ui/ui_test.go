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

	"github.com/steven509/testdash/internal/catalog"
	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/history"
	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/kafka"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/noise"
	"github.com/steven509/testdash/internal/progress"
	"github.com/steven509/testdash/internal/services"
	"github.com/steven509/testdash/internal/settings"
	"github.com/steven509/testdash/internal/style"
	"github.com/steven509/testdash/internal/wiremock"
)

func entry(id, parent, kind, class, method, name string) *catalog.Entry {
	return &catalog.Entry{ID: id, Parent: parent, Kind: kind, ClassName: class, Method: method, Name: name}
}

func testModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := New(ctx, Options{Workdir: t.TempDir(), RunDir: t.TempDir()}, settings.Defaults())
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

func typeText(m *Model, text string) {
	for _, c := range text {
		press(m, string(c))
	}
}

func screen(m *Model) string {
	return ansi.Strip(m.View().Content)
}

// clickEverywhere clicks the middle of every zone the last frame drew, redrawing after each; the header's and
// footer's, which run, quit and open dialogs, are left alone.
func clickEverywhere(t *testing.T, m *Model) {
	t.Helper()
	m.View()
	zones := append([]zone(nil), m.zones...)
	for _, z := range zones {
		if z.r.y == m.height-1 || z.r.y == 0 {
			continue
		}
		m.Update(tea.MouseClickMsg{X: z.r.x + z.r.w/2, Y: z.r.y + z.r.h/2, Button: tea.MouseLeft})
		m.Update(tea.MouseWheelMsg{X: z.r.x + z.r.w/2, Y: z.r.y + z.r.h/2, Button: tea.MouseWheelDown})
		m.popup = nil
		m.View()
	}
}

func TestTreeFiltersSelectsAndRuns(t *testing.T) {
	m := testModel(t)
	if s := screen(m); !strings.Contains(s, "All tests") || !strings.Contains(s, "Ready when you are") {
		t.Fatalf("start:\n%s", s)
	}
	press(m, "/")
	typeText(m, "signup")
	if !m.tree.visible["t4"] || m.tree.visible["t1"] {
		t.Fatalf("a filter ignoring spaces and punctuation finds the sign-up test: %v", m.tree.visible)
	}
	press(m, "enter")
	if m.focus != m.tree || m.tree.cursor == "" {
		t.Fatalf("enter leaves the filter on its first match, so another enter runs that, not everything: %q",
			m.tree.cursor)
	}
	press(m, "a")
	if !m.selected["t4"] || len(m.selected) != 1 {
		t.Fatalf("a selects what the filter shows: %v", m.selected)
	}
	press(m, "esc")
	if m.tree.text != "" || !m.tree.visible["t1"] {
		t.Fatal("Esc clears the filter")
	}
	press(m, "esc")
	if len(m.selected) != 0 {
		t.Fatal("a second Esc clears the selection")
	}
	press(m, "home", "down", "space")
	if len(m.selected) != 3 || !m.selected["t3"] {
		t.Fatalf("space on a class selects its tests, nested ones too: %v", m.selected)
	}
	if m.tree.cursor != "c1" {
		t.Fatalf("the cursor stays on the class it selected, not %s", m.tree.cursor)
	}
	if s := screen(m); strings.Contains(s, "[38;") {
		t.Fatalf("the class's details show escape codes as text:\n%s", s)
	}
	if footer := screen(m); !strings.Contains(footer, "enter  Run 3 selected tests") ||
		strings.Count(footer, "Run 3 selected tests") != 1 {
		t.Fatalf("Enter runs the selection:\n%s", footer)
	}
	if s := screen(m); !strings.Contains(s, "Run 3 selected tests") || !strings.Contains(s, "3 selected") {
		t.Fatalf("r says it runs the selection:\n%s", s)
	}
	press(m, "ctrl+t", "down", "enter")
	if m.testTask.Path != ":it" || m.focus != m.tree {
		t.Fatalf("the task chooser chose %s", m.testTask.Path)
	}
	clickEverywhere(t, m)
}

func TestClearingAFilterKeepsWhatWasOpen(t *testing.T) {
	m := testModel(t)
	press(m, "down", "left")
	if m.tree.cursor != "c1" || m.tree.expansion()["c1"] {
		t.Fatalf("left closes the class under the cursor: %s %v", m.tree.cursor, m.tree.expansion())
	}
	press(m, "/")
	typeText(m, "one")
	if !m.tree.expansion()["c1"] {
		t.Error("a filter opens the classes it matches in")
	}
	press(m, "esc")
	if m.tree.text != "" || m.tree.expansion()["c1"] {
		t.Error("clearing the filter closes the class again, as it was")
	}
}

func TestParameterisedTestFailsWithAnInvocation(t *testing.T) {
	m := testModel(t)
	m.runStarted, m.runFinished = time.Now(), time.Now()
	m.progress = progress.New(m.opts.RunDir)
	m.inRun = map[string]bool{"t1": true, "t2": true, "t3": true}
	events := `{"event":"registered","at":1,"entry":{"id":"t2-1","parent":"t2","kind":"test","className":"com.a.FooTest","methodName":"each","name":"[1] a"}}
{"event":"registered","at":1,"entry":{"id":"t2-2","parent":"t2","kind":"test","className":"com.a.FooTest","methodName":"each","name":"[2] b"}}
{"event":"finished","at":2,"id":"t1","status":"SUCCESSFUL"}
{"event":"finished","at":2,"id":"t2-1","status":"SUCCESSFUL"}
{"event":"finished","at":3,"id":"t2-2","status":"FAILED","message":"boom"}
{"event":"finished","at":4,"id":"t2","status":"SUCCESSFUL"}
{"event":"finished","at":4,"id":"t3","status":"SUCCESSFUL"}
`
	if err := os.WriteFile(filepath.Join(m.opts.RunDir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(tickMsg{})
	if got := m.status("t2"); got != "failed" {
		t.Errorf("a parameterised test with a failed invocation is %s", got)
	}
	if got := ansi.Strip(m.tree.right(m, "c1")); !strings.Contains(got, "1  4/4") {
		t.Errorf("the class counts each invocation and the failure: %q", got)
	}
	summary := ansi.Strip(m.classSummary("c1", 80))
	for _, want := range []string{"3 passed", "1 failed", "4 tests"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the class summary lacks %q:\n%s", want, summary)
		}
	}
}

func TestWindowStopsAtTheNeighbouringTests(t *testing.T) {
	m := testModel(t)
	m.runStarted = time.Now()
	m.progress = progress.New(m.opts.RunDir)
	m.inRun = map[string]bool{"t1": true, "t3": true, "t4": true}
	at := time.Now().UnixMilli() - 5000
	events := fmt.Sprintf(`{"event":"plan","at":1,"entries":[`+
		`{"id":"t1","parent":"c1","kind":"test","className":"com.a.FooTest","methodName":"one","name":"one"},`+
		`{"id":"t3","parent":"n1","kind":"test","className":"com.a.FooTest$Inner","methodName":"three","name":"three"},`+
		`{"id":"t4","parent":"c2","kind":"test","className":"com.a.BarTest","methodName":"sign_up","name":"sign up"}]}
{"event":"started","at":%[1]d,"id":"t1"}
{"event":"finished","at":%[2]d,"id":"t1","status":"SUCCESSFUL"}
{"event":"started","at":%[3]d,"id":"t3"}
{"event":"finished","at":%[4]d,"id":"t3","status":"SUCCESSFUL"}
{"event":"started","at":%[5]d,"id":"t4"}
{"event":"finished","at":%[6]d,"id":"t4","status":"SUCCESSFUL"}
`, at, at+400, at+450, at+900, at+3000, at+3200)
	if err := os.WriteFile(filepath.Join(m.opts.RunDir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(tickMsg{})
	w := m.windowFor("t3")
	if !w.start.Equal(time.UnixMilli(at+400)) || !w.end.Equal(time.UnixMilli(at+900).Add(correlationSlack)) {
		t.Errorf("t3's window starts where t1 finished and keeps its slack before the idle gap: %v to %v",
			w.start.Sub(time.UnixMilli(at)), w.end.Sub(time.UnixMilli(at)))
	}
	if w := m.windowFor("t1"); !w.end.Equal(time.UnixMilli(at+450)) || !w.start.Equal(time.UnixMilli(at).Add(-correlationSlack)) {
		t.Errorf("t1's window ends where t3 started: %v to %v", w.start.Sub(time.UnixMilli(at)), w.end.Sub(time.UnixMilli(at)))
	}
}

func TestTestDetailsShowWhatHappenedDuringIt(t *testing.T) {
	m := testModel(t)
	m.opts.Command = []string{"true"}
	m.runStarted, m.runCommand = time.Now(), "./gradlew :test"
	m.progress = progress.New(m.opts.RunDir)
	m.inRun = map[string]bool{}
	at := time.Now().UnixMilli() - 2000
	events := fmt.Sprintf(`{"event":"plan","at":1,"entries":[`+
		`{"id":"c1","kind":"class","className":"com.a.FooTest","name":"FooTest"},`+
		`{"id":"t1","parent":"c1","kind":"test","className":"com.a.FooTest","methodName":"one","name":"one()"},`+
		`{"id":"t2","parent":"c1","kind":"template","className":"com.a.FooTest","methodName":"each","name":"each"}]}
{"event":"started","at":%[1]d,"id":"t1"}
{"event":"finished","at":%[2]d,"id":"t1","status":"FAILED","message":"AssertionFailedError: expected: <1> but was: <2>","trace":"org.opentest4j.AssertionFailedError: expected: <1> but was: <2>\n\tat org.junit.Assert.fail(Assert.java:1)\n\tat com.a.FooTest.one(FooTest.java:12)"}
{"event":"registered","at":%[3]d,"entry":{"id":"t2-1","parent":"t2","kind":"test","className":"com.a.FooTest","methodName":"each","name":"[1] a"}}
{"event":"started","at":%[3]d,"id":"t2-1"}
`, at, at+400, at+500)
	if err := os.WriteFile(filepath.Join(m.opts.RunDir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(tickMsg{})
	if s := screen(m); !strings.Contains(s, "each › [1] a") || !strings.Contains(s, "RUNNING") ||
		!strings.Contains(s, "expected: <1> but was: <2>") {
		t.Fatalf("the dashboard follows the run:\n%s", s)
	}
	app := services.NewService("orders", "app", "orders:latest")
	m.Update(services.Added{Service: app})
	m.Update(services.Lines{Service: app, Gen: m.gen, Lines: []*logformat.Line{
		logformat.NewLine(time.UnixMilli(at+200), "2026-10-12 15:45:51,006 ERROR [main] c.l.p.Foo - it broke"),
		logformat.NewLine(time.UnixMilli(at+210), "\tat com.a.Foo.bar(Foo.java:1)"),
		logformat.NewLine(time.UnixMilli(at+300), "2026-10-12 15:45:51,106 INFO [main] c.l.p.Foo - fine"),
	}})
	if entry := m.serviceEntry(app); entry.problems != 1 {
		t.Errorf("problems %d", entry.problems)
	}
	m.tree.cursor = "t1"
	s := screen(m)
	for _, want := range []string{"FooTest.one(FooTest.java:12)", "1 library frame", "DURING THIS TEST", "it broke"} {
		if !strings.Contains(s, want) {
			t.Errorf("the test's details lack %q:\n%s", want, s)
		}
	}
	press(m, "3")
	if m.focus != m.inspector.events {
		t.Fatalf("3 focuses the timeline, not %T", m.focus)
	}
	press(m, "enter")
	logs, ok := m.serviceEntry(app).view.(*logView)
	if m.detailFrom != panelServices || !ok || m.focus != any(logs) || logs.window == nil {
		t.Fatalf("Enter opens the event in the application's log during the test: %v %T", m.detailFrom, m.focus)
	}
	press(m, "/", "b", "r", "o", "k", "enter", "e", "w", "v", "t", "f", "esc", "esc")
	if m.focus != m.serviceList {
		t.Fatalf("Esc goes back to the services list once the filter is clear, at %T", m.focus)
	}
	press(m, "tab", "tab", "1", "z", "z", "2", "down", "up", "enter")
	clickEverywhere(t, m)
	m.Update(runDoneMsg{code: 1})
	if s := screen(m); !strings.Contains(s, "FAILED") {
		t.Fatalf("the finished run:\n%s", s)
	}
	if failed := m.failedIDs(); !failed["t1"] {
		t.Fatalf("t1 failed: %v", failed)
	}
}

func TestTreeAnswersTheMouse(t *testing.T) {
	m := testModel(t)
	m.View()
	click := func(x, y int) {
		m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		m.View()
	}
	const left, firstRow = 2, headerHeight + 1 // inside the panel's border and padding; the rows start with All tests
	click(left+10, firstRow+2)
	if m.tree.cursor != "t1" {
		t.Fatalf("a click on a row moves the cursor there, not to %q", m.tree.cursor)
	}
	click(left, firstRow+1)
	if len(m.selected) != 3 {
		t.Fatalf("a click on a class's mark selects its tests: %v", m.selected)
	}
	click(left+2, firstRow+1)
	if m.tree.expanded["c1"] {
		t.Fatal("a click on a class's caret closes it")
	}
	click(left+10, firstRow+1)
	click(left+10, firstRow+1)
	if !m.tree.expanded["c1"] || m.tree.cursor != "c1" {
		t.Fatal("a second click on a class opens it")
	}
	if s := screen(m); !strings.Contains(s, "● ") || !strings.Contains(s, "◐ ") {
		t.Fatalf("selected and partly selected rows are marked:\n%s", s)
	}
}

func TestKafkaMessageHeaders(t *testing.T) {
	m := testModel(t)
	service := services.NewService("kafka", "kafka", "apache/kafka:3.8.0")
	m.Update(services.Added{Service: service})
	view := m.serviceEntry(service).view.(*kafkaView)
	key, value, trace, meta := "k1", `{"id":1}`, "00-abc-01", `{"service":"orders"}`
	m.Update(kafkaMessagesMsg{view: view, batch: []kafka.Message{{At: time.Now(), Topic: "orders", Key: &key,
		Value: &value, Headers: []kafka.Header{{Name: "traceparent", Value: &trace}, {Name: "empty"},
			{Name: "meta", Value: &meta}}}}})
	m.serviceList.selectEntry(m, m.serviceEntry(service))
	m.setFocus(m.serviceList)
	m.setFocus(view.table)
	if s := screen(m); !strings.Contains(s, "Headers 3") || !strings.Contains(s, `"id": 1`) {
		t.Fatalf("the value shows first, the headers counted in their tab:\n%s", s)
	}
	press(m, "h")
	s := screen(m)
	for _, want := range []string{"traceparent  00-abc-01", "empty        null", `"service": "orders"`} {
		if !strings.Contains(s, want) {
			t.Errorf("the headers tab lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, `"id": 1`) {
		t.Error("the headers tab shows the headers, not the value")
	}
}

func TestSQLShowsFormattedThenRaw(t *testing.T) {
	m := testModel(t)
	service := services.NewService("mariadb", "mariadb", "mariadb:11")
	m.Update(services.Added{Service: service})
	view := m.serviceEntry(service).view.(*dbView)
	query := "UPDATE products SET stock = stock - 2 WHERE sku = 'MP-030' AND stock >= 2"
	m.Update(queriesMsg{view: view, batch: []databases.Query{{At: time.Now(), Connection: "7", Kind: "Query",
		Text: query}}})
	m.serviceList.selectEntry(m, m.serviceEntry(service))
	m.setFocus(m.serviceList)
	m.setFocus(view.table)
	s := screen(m)
	for _, want := range []string{"Formatted", "Raw", "UPDATE products", "SET", "stock = stock - 2", "AND stock >= 2"} {
		if !strings.Contains(s, want) {
			t.Errorf("the query is laid out first, with a tab for it raw; %q is missing:\n%s", want, s)
		}
	}
	if strings.Contains(s, "SET stock = stock - 2 WHERE") {
		t.Errorf("the formatted tab breaks the query over lines:\n%s", s)
	}
	press(m, "v")
	if s := screen(m); !strings.Contains(s, "UPDATE products SET stock =") {
		t.Errorf("v shows the query as logged:\n%s", s)
	}
}

func TestPostgresQueryShowsItsTimeAndParameters(t *testing.T) {
	m := testModel(t)
	service := services.NewService("postgres", "postgres", "postgres:16")
	m.Update(services.Added{Service: service})
	view := m.serviceEntry(service).view.(*dbView)
	m.Update(queriesMsg{view: view, batch: []databases.Query{{At: time.Now(), Connection: "115", Kind: "Execute",
		Text: "SELECT id FROM orders WHERE customer_id = $1", Params: "$1 = '42'", Millis: "0.120"}}})
	m.serviceList.selectEntry(m, m.serviceEntry(service))
	m.setFocus(m.serviceList)
	m.setFocus(view.table)
	s := screen(m)
	for _, want := range []string{"0.120 ms", "parameters", "$1 = '42'", "WHERE"} {
		if !strings.Contains(s, want) {
			t.Errorf("a prepared statement shows how long it took and its parameters; %q is missing:\n%s", want, s)
		}
	}
	if got := highlightSQL("SELECT 1 WHERE id = $1", "postgres"); !strings.Contains(got, style.PinkText.Render("$1")) {
		t.Errorf("a Postgres parameter is coloured: %q", got)
	}
}

func TestRedisCommands(t *testing.T) {
	var copied []string
	real := systemClipboard
	systemClipboard = func(text string) bool { copied = append(copied, text); return true }
	t.Cleanup(func() { systemClipboard = real })
	m := testModel(t)
	service := services.NewService("redis", "redis", "redis:7")
	m.Update(services.Added{Service: service})
	view := m.serviceEntry(service).view.(*dbView)
	args := []string{"SET", "cart:42", `{"items":[1,2]}`, "EX", "60"}
	m.Update(queriesMsg{view: view, batch: []databases.Query{{At: time.Now(), Connection: "172.18.0.5:52342",
		Kind: "SET", Args: args, Text: databases.RedisCommandLine(args)}}})
	m.serviceList.selectEntry(m, m.serviceEntry(service))
	m.setFocus(m.serviceList)
	m.setFocus(view.table)
	s := screen(m)
	for _, want := range []string{"Commands", "1 command", "Client", "cart:42", "argument 2", `"items": [`, "c  Copy command",
		"1 cmd"} {
		if !strings.Contains(s, want) {
			t.Errorf("Redis's view lacks %q:\n%s", want, s)
		}
	}
	_, cmd := m.Update(key("c"))
	cmd()
	if len(copied) != 1 || copied[0] != `SET cart:42 "{\"items\":[1,2]}" EX 60` {
		t.Errorf("c copies the command as redis-cli takes it: %q", copied)
	}
}

func TestHighlightSQL(t *testing.T) {
	got := highlightSQL("SELECT price FROM products WHERE price > 199.00 AND sku = ?", "mariadb")
	if !strings.Contains(got, style.AccentBold.Render("SELECT")) || !strings.Contains(got, style.PinkText.Render("?")) {
		t.Errorf("keywords and placeholders have their colours: %q", got)
	}
	if !strings.Contains(got, style.Yellow.Render("199")+style.Yellow.Render(".")+style.Yellow.Render("00")) {
		t.Errorf("a decimal is one colour, though the lexer splits it: %q", got)
	}
	if ansi.Strip(got) != "SELECT price FROM products WHERE price > 199.00 AND sku = ?" {
		t.Errorf("colouring keeps the text: %q", ansi.Strip(got))
	}
}

func TestCopyQueriesAndPayloads(t *testing.T) {
	var copied []string
	real := systemClipboard
	systemClipboard = func(text string) bool { copied = append(copied, text); return true }
	t.Cleanup(func() { systemClipboard = real })
	run := func(cmd tea.Cmd) {
		if cmd != nil {
			cmd()
		}
	}
	m := testModel(t)
	db := services.NewService("mariadb", "mariadb", "mariadb:11")
	m.Update(services.Added{Service: db})
	view := m.serviceEntry(db).view.(*dbView)
	m.Update(queriesMsg{view: view, batch: []databases.Query{{At: time.Now(), Connection: "7", Kind: "Query",
		Text: "select 1 from dual"}}})
	m.serviceList.selectEntry(m, m.serviceEntry(db))
	m.setFocus(m.serviceList)
	m.setFocus(view.table)
	if footer := screen(m); !strings.Contains(footer, "c  Copy query") {
		t.Errorf("the footer offers to copy the query:\n%s", footer)
	}
	_, cmd := m.Update(key("c"))
	run(cmd)

	kafkaService := services.NewService("kafka", "kafka", "apache/kafka:3.8.0")
	m.Update(services.Added{Service: kafkaService})
	messages := m.serviceEntry(kafkaService).view.(*kafkaView)
	value := `{"id":1}`
	m.Update(kafkaMessagesMsg{view: messages, batch: []kafka.Message{{At: time.Now(), Topic: "orders", Value: &value}}})
	m.serviceList.selectEntry(m, m.serviceEntry(kafkaService))
	m.setFocus(messages.table)
	_, cmd = m.Update(key("c"))
	run(cmd)

	if strings.Join(copied, " | ") != `select 1 from dual | {"id":1}` {
		t.Errorf("c copies the query, then the payload as it was sent: %q", copied)
	}
	if !strings.Contains(screen(m), "Copied the payload") {
		t.Error("copying says so")
	}
}

func TestCurlCommand(t *testing.T) {
	raw, _ := jsonx.ParseObject([]byte(`{"request":{"method":"POST","url":"/payments/p1/refunds",` +
		`"absoluteUrl":"http://wiremock:8080/payments/p1/refunds","headers":{"Content-Type":"text/plain",` +
		`"Host":"wiremock:8080","X-Tag":["a","b"]},"body":"it's 10"}}`))
	e := wiremock.ServeEvent{Raw: raw}
	want := "curl 'http://127.0.0.1:32768/payments/p1/refunds' \\\n  -X POST \\\n  -H 'Content-Type: text/plain' \\\n" +
		"  -H 'X-Tag: a' \\\n  -H 'X-Tag: b' \\\n  --data-raw 'it'\\''s 10'"
	if got := curlCommand(e, "http://127.0.0.1:32768"); got != want {
		t.Errorf("the request goes to WireMock's published port, as it came:\n%s\nwant\n%s", got, want)
	}
	if got := curlCommand(e, ""); !strings.HasPrefix(got, "curl 'http://wiremock:8080/payments/p1/refunds'") {
		t.Errorf("without a published port it goes where it came to:\n%s", got)
	}
}

func TestCopyFailure(t *testing.T) {
	var copied []string
	real := systemClipboard
	systemClipboard = func(text string) bool { copied = append(copied, text); return true }
	t.Cleanup(func() { systemClipboard = real })
	m := testModel(t)
	m.runStarted, m.runFinished = time.Now(), time.Now()
	m.progress = progress.New(m.opts.RunDir)
	m.inRun = map[string]bool{"t1": true}
	events := `{"event":"finished","at":2,"id":"t1","status":"FAILED","message":"boom",` +
		`"trace":"java.lang.AssertionError: boom\n\tat com.a.FooTest.one(FooTest.java:9)"}` + "\n"
	if err := os.WriteFile(filepath.Join(m.opts.RunDir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(tickMsg{})
	press(m, "home", "down", "down")
	if m.tree.cursor != "t1" || !strings.Contains(screen(m), "c  Copy failure") {
		t.Fatalf("a failed test offers to copy its failure: %s\n%s", m.tree.cursor, screen(m))
	}
	_, cmd := m.Update(key("c"))
	cmd()
	if want := "FooTest › one\njava.lang.AssertionError: boom\n\tat com.a.FooTest.one(FooTest.java:9)"; len(copied) != 1 ||
		copied[0] != want {
		t.Errorf("c copies the test's name and stack trace: %q", copied)
	}
	press(m, "down")
	if strings.Contains(screen(m), "Copy failure") {
		t.Error("a test that did not fail has nothing to copy")
	}
}

func TestHighlightJSON(t *testing.T) {
	got := highlightJSON(`{"id": 1, "ok": true, "name": "x"}`)
	for _, want := range []string{style.Cyan.Render(`"id"`), style.Yellow.Render("1"),
		style.AccentText.Render("true"), style.Green.Render(`"x"`)} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON has its colours; %q is missing from %q", want, got)
		}
	}
}

func TestHidingAnIntegration(t *testing.T) {
	m := testModel(t)
	m.Update(services.Added{Service: services.NewService("redis", "redis", "redis:7")})
	m.Update(services.Added{Service: services.NewService("kafka", "kafka", "apache/kafka:3.8.0")})
	press(m, "ctrl+s")
	form := m.popup.(*settingsOverlay).form
	for form.items()[form.cursor].label != "Redis" {
		press(m, "down")
	}
	press(m, "right")
	if !m.prefs.Hidden("redis") {
		t.Fatal("the Redis row hides Redis")
	}
	press(m, "esc")
	var keys []string
	for _, entry := range m.orderedServices() {
		keys = append(keys, entry.svc.Key)
	}
	if strings.Join(keys, " ") != "kafka" {
		t.Errorf("a hidden kind leaves the services at once: %v", keys)
	}
}

func TestSettingsFitAShortTerminal(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 26})
	press(m, "ctrl+s")
	for range 20 {
		press(m, "down")
	}
	lines := strings.Split(screen(m), "\n")
	for i, line := range lines {
		if strings.Contains(line, "Saved in") {
			if i+1 >= len(lines) || !strings.Contains(lines[i+1], "╰") || !strings.Contains(screen(m), "▌Redis") {
				t.Errorf("the rows scroll so the last one and the dialog's foot show:\n%s", screen(m))
			}
			return
		}
	}
	t.Errorf("the settings' status is missing:\n%s", screen(m))
}

func TestDesktopNotifications(t *testing.T) {
	m := testModel(t)
	for _, c := range []struct {
		notify string
		away   bool
		want   bool
	}{{"away", true, true}, {"away", false, false}, {"always", false, true}, {"off", true, false}} {
		m.prefs.Notify, m.away = c.notify, c.away
		if got := m.notifyDesktop("3 tests passed") != nil; got != c.want {
			t.Errorf("notify %s, away %v: notified %v", c.notify, c.away, got)
		}
	}
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TMUX", "")
	if got := desktopNotification("done"); got != "\x1b]9;done\x07" {
		t.Errorf("most terminals take iTerm2's notification: %q", got)
	}
	t.Setenv("KITTY_WINDOW_ID", "1")
	if got := desktopNotification("done"); !strings.HasPrefix(got, "\x1b]99;") {
		t.Errorf("kitty gets its own: %q", got)
	}
	m.Update(tea.BlurMsg{})
	if !m.away {
		t.Error("the terminal going to the background is noticed")
	}
	m.Update(tea.FocusMsg{})
	if m.away {
		t.Error("and coming back")
	}
}

func TestKafkaMessagesStayInTimeOrder(t *testing.T) {
	m := testModel(t)
	service := services.NewService("kafka", "kafka", "apache/kafka:3.8.0")
	m.Update(services.Added{Service: service})
	view := m.serviceEntry(service).view.(*kafkaView)
	at := time.Now()
	message := func(offset int64, seconds int) kafka.Message {
		return kafka.Message{At: at.Add(time.Duration(seconds) * time.Second), Topic: "orders", Partition: int(offset % 2),
			Offset: offset}
	}
	m.Update(kafkaMessagesMsg{view: view, batch: []kafka.Message{message(3, 3), message(4, 4)}})
	m.Update(kafkaMessagesMsg{view: view, batch: []kafka.Message{message(1, 1), message(2, 2)}}) // the other partition
	var order []string
	for _, row := range view.table.rows {
		order = append(order, row.key)
	}
	if want := "orders:1:1 orders:0:2 orders:1:3 orders:0:4"; strings.Join(order, " ") != want {
		t.Errorf("messages are in time order: %v", order)
	}
	if view.table.cursorKey() != "orders:0:4" {
		t.Errorf("the cursor follows the newest, not %s", view.table.cursorKey())
	}
}

func TestWiremockKeepsRequestsWhenTestsResetIt(t *testing.T) {
	m := testModel(t)
	service := services.NewService("wiremock", "wiremock", "wiremock/wiremock:3.13.1")
	m.Update(services.Added{Service: service})
	view := m.serviceEntry(service).view.(*wiremockView)
	at := time.Now()
	request := func(id string, ms int, url string, matched bool) wiremock.ServeEvent {
		raw, _ := jsonx.ParseObject([]byte(fmt.Sprintf(`{"request":{"method":"POST","url":%q},"wasMatched":%v}`, url, matched)))
		return wiremock.ServeEvent{ID: id, At: at.Add(time.Duration(ms) * time.Millisecond), Method: "POST", URL: url,
			Status: map[bool]int{true: 200, false: 404}[matched], Matched: matched, Raw: raw}
	}
	refund := request("e2", 200, "/payments/p1/refunds", false)
	m.Update(wiremockMsg{view: view, poll: wiremockPoll{hasEvents: true, events: []wiremock.ServeEvent{
		request("e1", 100, "/payments", true), refund}}})
	m.Update(wiremockMsg{view: view, poll: wiremockPoll{hasMisses: true, nearMisses: []wiremock.NearMiss{{Method: "POST",
		URL: "/payments/p1/refunds", Distance: 0.2, StubMapping: []byte(`{"name":"Refund a payment"}`)}}}})
	// a test resets WireMock: its journal and near misses start again
	m.Update(wiremockMsg{view: view, poll: wiremockPoll{hasEvents: true, hasMisses: true,
		events: []wiremock.ServeEvent{request("e3", 300, "/payments", true)}}})
	if len(view.events) != 3 || len(view.requests.rows) != 3 || view.requests.rows[0].key != "r:e1" {
		t.Fatalf("requests from before the reset stay: %d events, %d rows", len(view.events), len(view.requests.rows))
	}
	if len(view.misses.rows) != 1 || !strings.Contains(view.requestDetails("n:e2"), "Refund a payment") {
		t.Errorf("the unmatched request keeps its closest stub:\n%s", view.requestDetails("n:e2"))
	}
	if !strings.Contains(view.requestDetails("r:e2"), "Refund a payment") {
		t.Errorf("the requests tab shows the closest stub too:\n%s", view.requestDetails("r:e2"))
	}
	m.Update(wiremockMsg{view: view, poll: wiremockPoll{hasEvents: true, events: []wiremock.ServeEvent{
		request("e0", 50, "/shipping/quotes", true)}}})
	if view.requests.rows[0].key != "r:e0" {
		t.Errorf("a request read late goes in time order, not last")
	}
}

func TestJournalCopyNames(t *testing.T) {
	if got := targetName("payments-mock 2"); got != "payments_mock_2" {
		t.Errorf("a service's key becomes a file name without dashes, which separate the parts: %q", got)
	}
	for name, want := range map[string]string{"4242-0-wiremock.requests.json": "wiremock requests",
		"4242-17-payments_mock.near-misses.json": "payments_mock near-misses", "4242-1-wiremock.requests.json.partial": ""} {
		got := ""
		if match := journalCopy.FindStringSubmatch(name); match != nil {
			got = match[1] + " " + match[2]
		}
		if got != want {
			t.Errorf("%s is %q, want %q", name, got, want)
		}
	}
}

func TestServicesKeepTheirOrder(t *testing.T) {
	m := testModel(t)
	for _, s := range [][3]string{{"mongodb", "mongodb", "mongo:7"}, {"kafka", "kafka", "apache/kafka:3.8.1"},
		{"zipkin", "server", "openzipkin/zipkin"}, {"shop", "app", "shop:latest"}, {"mariadb", "mariadb", "mariadb:11"},
		{"wiremock", "wiremock", "wiremock/wiremock:3"}} {
		m.Update(services.Added{Service: services.NewService(s[0], s[1], s[2])})
	}
	var keys []string
	for _, entry := range m.orderedServices() {
		keys = append(keys, entry.svc.Key)
	}
	if got := strings.Join(keys, " "); got != "shop wiremock kafka mariadb mongodb zipkin" {
		t.Errorf("the application, mocks, brokers, databases, the rest: %s", got)
	}
}

func TestQuitAsksFirst(t *testing.T) {
	m := testModel(t)
	m.Update(key("q"))
	if _, ok := m.popup.(*confirmQuit); !ok || !strings.Contains(screen(m), "Quit testdash?") {
		t.Fatal("q asks before quitting")
	}
	press(m, "n")
	if m.popup != nil {
		t.Fatal("n stays")
	}
	m.Update(key("q"))
	if _, cmd := m.Update(key("y")); cmd == nil {
		t.Fatal("y quits")
	}
}

func TestRunShowsTheRunAndUnselectsThePassed(t *testing.T) {
	m := testModel(t)
	m.opts.Command = []string{"true"}
	m.tree.cursor = "t3"
	m.selected = map[string]bool{"t1": true, "t3": true, "t4": true}
	press(m, "enter")
	if s := screen(m); m.tree.cursor != "" || m.focus != m.tree || !strings.Contains(s, "Overview · :test") ||
		strings.Contains(s, "All tests") {
		t.Fatalf("starting a run shows its overview, not %q", m.tree.cursor)
	}
	for m.runner.Running() {
		time.Sleep(10 * time.Millisecond)
	}
	m.Update(runDoneMsg{code: 1})
	if s := screen(m); !strings.Contains(s, "All tests") || strings.Contains(s, "Overview") {
		t.Error("once the run is done, the first row is all the tests again")
	}
	m.inRun = map[string]bool{"t1": true, "t3": true, "t4": true}
	m.progress.States = map[string]*progress.State{"t1": {Status: "passed"}, "t3": {Status: "failed"},
		"t4": {Status: "skipped"}}
	press(m, "u")
	if len(m.selected) != 1 || !m.selected["t3"] {
		t.Fatalf("u keeps only the failed test selected: %v", m.selected)
	}
}

func TestLongCommandsFitOneLine(t *testing.T) {
	m := testModel(t)
	m.runStarted, m.runFinished = time.Now(), time.Now()
	m.progress = progress.New(m.opts.RunDir)
	m.runCommand = "./gradlew :test" + strings.Repeat(" --tests com.a.VeryLongTestClassName", 30)
	if line := ansi.Strip(m.commandLine(100)); strings.Contains(line, "VeryLong") || !strings.Contains(line, "30 --tests filters") {
		t.Fatalf("the filters are counted, not listed: %q", line)
	}
	press(m, "c")
	if line := ansi.Strip(m.commandLine(100)); !strings.Contains(line, "VeryLong") {
		t.Fatalf("c shows the whole command: %q", line)
	}
}

func TestCursorStaysHidden(t *testing.T) {
	m := testModel(t)
	hides := func(cmd tea.Cmd) bool {
		var walk func(msg tea.Msg) bool
		walk = func(msg tea.Msg) bool {
			switch msg := msg.(type) {
			case tea.RawMsg:
				return fmt.Sprint(msg.Msg) == ansi.ResetModeTextCursorEnable
			case tea.BatchMsg:
				for _, c := range msg {
					if c != nil {
						if _, isTick := c().(tickMsg); !isTick && walk(c()) {
							return true
						}
					}
				}
			}
			return false
		}
		return cmd != nil && walk(cmd())
	}
	for name, msg := range map[string]tea.Msg{"a resize": tea.WindowSizeMsg{Width: 120, Height: 40},
		"focus": tea.FocusMsg{}} {
		if _, cmd := m.Update(msg); !hides(cmd) {
			t.Errorf("%s hides the cursor again", name)
		}
	}
	if !m.View().ReportFocus {
		t.Error("focus changes are reported, to hide the cursor after them")
	}
}

func TestPaletteRunsCommands(t *testing.T) {
	m := testModel(t)
	press(m, "ctrl+p")
	typeText(m, "zoom")
	press(m, "enter")
	if !m.zoomed || m.popup != nil {
		t.Fatal("the palette zooms the details")
	}
}

func TestSettingsSaveAndApply(t *testing.T) {
	m := testModel(t)
	defer useIcons(false, 1)
	press(m, "ctrl+s")
	if _, ok := m.popup.(*settingsOverlay); !ok {
		t.Fatal("^s opens the settings")
	}
	press(m, "space", "down", "right", "down", "right")
	saved, err := settings.Load()
	if err != nil || !saved.NerdFont || saved.IconSpacing != 2 || saved.Theme != "Ocean" {
		t.Fatalf("the settings were saved: %+v %v", saved, err)
	}
	if ic.caretClosed != "\uf460 " {
		t.Errorf("Nerd Font icons, two spaces apart, are in use at once: %q", ic.caretClosed)
	}
	press(m, "up", "up", "space")
	if saved, _ := settings.Load(); saved.NerdFont || ic.caretClosed != "▸" {
		t.Error("Unicode icons are back at once")
	}
	if s := screen(m); strings.Contains(s, "Icon spacing") {
		t.Error("icon spacing shows only with Nerd Font icons")
	}
	press(m, "esc")
	if m.popup != nil {
		t.Error("Esc closes the settings")
	}
}

func TestSettingsApp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	app := NewSettingsApp(settings.Defaults())
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if s := ansi.Strip(app.View().Content); !strings.Contains(s, "Settings") || !strings.Contains(s, "Preview") {
		t.Fatalf("settings screen:\n%s", s)
	}
	app.Update(key("down"))
	app.Update(key("down"))
	app.Update(key("space"))
	if saved, _ := settings.Load(); saved.Mouse {
		t.Error("the mouse was switched off and saved")
	}
	if _, cmd := app.Update(key("q")); cmd == nil {
		t.Error("q leaves the settings")
	}
}

func TestWithoutABuildItSaysWhatToDo(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := New(context.Background(), Options{Workdir: t.TempDir(), RunDir: t.TempDir()}, settings.Defaults())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	s := screen(m)
	if strings.Count(s, "testdash -- npm test") != 2 || !strings.Contains(s, "Start testdash in a Gradle repo") {
		t.Errorf("the tests and the details both say to start in a Gradle repo or give a whole command:\n%s", s)
	}
	rows := strings.Split(s, "\n")
	for i, row := range rows {
		if tests, _, _ := strings.Cut(row, "││"); strings.Contains(tests, "like this:") {
			if next, _, _ := strings.Cut(rows[i+1], "││"); strings.Trim(next, "│ ") != "" {
				t.Errorf("a blank line comes before the example in the tests: %q", next)
			}
		}
	}
}

func TestFooterShowsZoom(t *testing.T) {
	m := testModel(t)
	lines := strings.Split(screen(m), "\n")
	if footer := lines[len(lines)-1]; !strings.Contains(footer, "z  Zoom") {
		t.Errorf("the footer shows z with the other keys: %q", footer)
	}
}

func TestKeycastShowsTheKeysAndWhatTheyDid(t *testing.T) {
	m := testModel(t)
	m.opts.Keycast = "all"
	castLine := func() string { return strings.Split(screen(m), "\n")[m.height-3] }
	press(m, "down", "down", "space")
	if line := castLine(); !strings.Contains(line, "↓  ×2") || !strings.Contains(line, "space  Select") {
		t.Errorf("the keys and what they did show over the bottom of the screen: %q", line)
	}
	press(m, "/")
	typeText(m, "one")
	var shown []string
	for _, k := range m.keycast.keys {
		shown = append(shown, strings.TrimSpace(k.key+" "+k.label))
	}
	if got := strings.Join(shown, ", "); got != "space Select, / Filter, one" {
		t.Errorf("what is typed shows as one piece, after the last keys: %s", got)
	}
	press(m, "enter")
	if last := m.keycast.keys[len(m.keycast.keys)-1]; last.label != "Done" {
		t.Errorf("a key a field takes itself is named by its own binding: %+v", last)
	}
	m.keycast.until = time.Now().Add(-time.Millisecond)
	m.Update(tickMsg{})
	if line := castLine(); strings.Contains(line, "Filter") {
		t.Errorf("the keys go once nothing is pressed for a moment: %q", line)
	}
	press(m, "ctrl+s", "down")
	if m.popup == nil {
		t.Fatal("ctrl+s opens the settings")
	}
	if last := m.keycast.keys[len(m.keycast.keys)-1]; last.key != "↓" || last.label != "" {
		t.Errorf("a key in a dialog shows without the last key's label: %+v", last)
	}
	m.popup = nil
	m.opts.Keycast = ""
	press(m, "esc")
	if last := m.keycast.keys[len(m.keycast.keys)-1]; last.key != "↓" {
		t.Error("without TESTDASH_KEYCAST no keys are kept")
	}
}

func TestKeycastOfShortcutsLeavesOutMovingAndTyping(t *testing.T) {
	m := testModel(t)
	m.opts.Keycast = "shortcuts"
	press(m, "down", "down", "space", "g", "/")
	typeText(m, "one")
	press(m, "esc", "ctrl+s", "down", "right")
	var shown []string
	for _, k := range m.keycast.keys {
		shown = append(shown, k.key+" "+k.label)
	}
	if got := strings.Join(shown, ", "); got != "/ Filter, esc Clear, ^s Settings" {
		t.Errorf("only the keys that did something by name show: %s", got)
	}
}

// runAll runs a command and the commands it batches, and returns the messages they gave.
func runAll(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, runAll(c)...)
		}
		return msgs
	}
	return []tea.Msg{msg}
}

// finishRun plays a run of the test model's tests to its end: t1 passes, t3 fails, t4 is skipped.
func finishRun(t *testing.T, m *Model, at time.Time) tea.Cmd {
	t.Helper()
	m.runStarted, m.runFinished = at, time.Time{}
	m.listening = true
	m.progress = progress.New(m.opts.RunDir)
	m.inRun = map[string]bool{"t1": true, "t3": true, "t4": true}
	events := fmt.Sprintf(`{"event":"plan","at":1,"entries":[`+
		`{"id":"t1","parent":"c1","kind":"test","className":"com.a.FooTest","methodName":"one","name":"one"},`+
		`{"id":"t3","parent":"n1","kind":"test","className":"com.a.FooTest$Inner","methodName":"three","name":"three"},`+
		`{"id":"t4","parent":"c2","kind":"test","className":"com.a.BarTest","methodName":"sign_up","name":"sign up"}]}
{"event":"started","at":%[1]d,"id":"t1"}
{"event":"finished","at":%[2]d,"id":"t1","status":"SUCCESSFUL"}
{"event":"started","at":%[1]d,"id":"t3"}
{"event":"finished","at":%[2]d,"id":"t3","status":"FAILED","message":"boom\nmore"}
{"event":"skipped","at":%[2]d,"id":"t4"}
`, at.UnixMilli(), at.UnixMilli()+250)
	if err := os.WriteFile(filepath.Join(m.opts.RunDir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(tickMsg{})
	_, cmd := m.Update(runDoneMsg{code: 1})
	return cmd
}

func TestARunIsRecorded(t *testing.T) {
	m := testModel(t)
	cache := t.TempDir()
	m.history = history.Open(cache, "/repo")
	m.Update(commitMsg{started: time.UnixMilli(1000), commit: "abc123"}) // an earlier run's: ignored
	started := time.Now().Add(-time.Second)
	runAll(finishRun(t, m, started))
	if len(m.runs) != 1 {
		t.Fatalf("the run is in the history: %d", len(m.runs))
	}
	run := m.runs[0]
	var got []string
	for _, result := range run.Tests {
		got = append(got, result.ID+" "+result.Status+" "+result.Failure)
	}
	if strings.Join(got, ", ") != "t1 passed , t3 failed boom, t4 skipped " || run.Task != ":test" || run.Exit != 1 ||
		run.Tests[0].Took != 250 || run.Commit != "" {
		t.Errorf("what ran and how: %+v", run)
	}
	saved := history.Open(cache, "/repo").Load(10)
	if len(saved) != 1 || !saved[0].Started.Equal(started) {
		t.Fatalf("the run is saved: %+v", saved)
	}
	again := testModel(t)
	again.history = history.Open(cache, "/repo")
	for _, msg := range runAll(again.loadHistory()) {
		again.Update(msg)
	}
	if len(again.runs) != 1 || len(again.historyIndex["t3"]) != 1 {
		t.Errorf("the next start loads it: %d runs", len(again.runs))
	}
}

func TestATestsHistory(t *testing.T) {
	m := testModel(t)
	at := time.Date(2026, 10, 12, 10, 42, 0, 0, time.Local)
	run := func(minutes int, commit, status string, took int64) history.Run {
		return history.Run{Started: at.Add(time.Duration(minutes) * time.Minute), Commit: commit,
			Tests: []history.Result{{ID: "t3", Status: status, Took: took}}}
	}
	m.setRuns([]history.Run{run(4, "abc1234ff", "failed", 0), run(3, "abc1234ff", "failed", 0),
		run(2, "0def999", "passed", 600), run(1, "0def999", "passed", 640), run(0, "0def999", "passed", 580)})
	press(m, "home")
	for m.tree.cursor != "t3" {
		press(m, "down")
	}
	s := screen(m)
	for _, want := range []string{"History ■■■■■", "5 runs, newest last", "Failing for 2 runs, since 12 Oct 10:45 (abc1234)",
		"Usually 0.6s"} {
		if !strings.Contains(s, want) {
			t.Errorf("the test's details lack %q:\n%s", want, s)
		}
	}
}

func TestFlakyTests(t *testing.T) {
	m := testModel(t)
	run := func(status string) history.Run {
		return history.Run{Started: time.Now(), Commit: "abc", Tests: []history.Result{{ID: "t3", Status: status},
			{ID: "t1", Status: "passed"}}}
	}
	m.setRuns([]history.Run{run("failed"), run("passed")})
	if !strings.Contains(screen(m), "three ≈") || strings.Contains(screen(m), "one ≈") {
		t.Errorf("a test that passed and failed on one commit is marked flaky:\n%s", screen(m))
	}
	press(m, "s", "s", "s")
	if m.tree.show != "flaky" {
		t.Fatalf("s goes on to the flaky tests: %s", m.tree.show)
	}
	var shown []string
	for _, line := range m.tree.lines() {
		if m.selectable[line.id] {
			shown = append(shown, line.id)
		}
	}
	if strings.Join(shown, " ") != "t3" {
		t.Errorf("only the flaky tests show: %v", shown)
	}
}

func TestSinceTheLastRun(t *testing.T) {
	m := testModel(t)
	m.history = history.Open(t.TempDir(), "/repo")
	earlier := time.Date(2026, 10, 12, 9, 30, 0, 0, time.Local)
	m.setRuns([]history.Run{{Started: earlier, Task: ":test", Commit: "fedcba987",
		Tests: []history.Result{{ID: "t1", Status: "failed"}, {ID: "t3", Status: "passed"}, {ID: "t4", Status: "skipped"}}}})
	runAll(finishRun(t, m, time.Now().Add(-time.Second)))
	s := screen(m)
	for _, want := range []string{"SINCE THE LAST RUN  12 Oct 09:30 (fedcba9)", "Fail anew  1", "FooTest › when inner › three",
		"Fixed  1", "FooTest › one"} {
		if !strings.Contains(s, want) {
			t.Errorf("the overview lacks %q:\n%s", want, s)
		}
	}
}

func TestTheRunsBefore(t *testing.T) {
	m := testModel(t)
	m.opts.Command = []string{"true"}
	at := time.Date(2026, 10, 12, 9, 30, 0, 0, time.Local)
	m.setRuns([]history.Run{
		{Started: at.Add(time.Hour), Task: ":test", Took: 23000, Tests: []history.Result{{ID: "t1", Status: "passed"},
			{ID: "t3", Status: "failed", Failure: "AssertionError: boom"}, {ID: "gone", Status: "failed"}}},
		{Started: at, Task: ":test", Took: 21000, Tests: []history.Result{{ID: "t1", Status: "passed"}}}})
	press(m, "H")
	s := screen(m)
	for _, want := range []string{"12 Oct 10:30", "12 Oct 09:30", "23s", "FooTest › when inner › three", "AssertionError: boom"} {
		if !strings.Contains(s, want) {
			t.Errorf("the runs lack %q:\n%s", want, s)
		}
	}
	press(m, "enter")
	if m.popup != nil || len(m.lastSelection) != 1 || !m.lastSelection["t3"] {
		t.Errorf("enter runs the failures the build still has: %v", m.lastSelection)
	}
}

func TestEventsByTrace(t *testing.T) {
	m := testModel(t)
	m.runStarted = time.Now().Add(-10 * time.Second)
	m.progress = progress.New(m.opts.RunDir)
	m.inRun = map[string]bool{"t1": true}
	at := time.Now().Add(-5 * time.Second)
	events := fmt.Sprintf(`{"event":"plan","at":1,"entries":[`+
		`{"id":"t1","parent":"c1","kind":"test","className":"com.a.FooTest","methodName":"one","name":"one"}]}
{"event":"started","at":%d,"id":"t1"}
{"event":"finished","at":%d,"id":"t1","status":"SUCCESSFUL"}
`, at.UnixMilli(), at.Add(time.Second).UnixMilli())
	if err := os.WriteFile(filepath.Join(m.opts.RunDir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(tickMsg{})
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	mock := services.NewService("wiremock", "wiremock", "wiremock/wiremock:3")
	m.Update(services.Added{Service: mock})
	raw, _ := jsonx.ParseObject([]byte(`{"request":{"method":"POST","url":"/payments","headers":` +
		`{"traceparent":"00-` + traceID + `-00f067aa0ba902b7-01"}},"wasMatched":true}`))
	m.Update(wiremockMsg{view: m.serviceEntry(mock).view.(*wiremockView), poll: wiremockPoll{hasEvents: true,
		events: []wiremock.ServeEvent{{ID: "e1", At: at.Add(200 * time.Millisecond), Method: "POST", URL: "/payments",
			Status: 201, Matched: true, Raw: raw}}}})
	broker := services.NewService("kafka", "kafka", "apache/kafka:3.8.0")
	m.Update(services.Added{Service: broker})
	value, parent := `{"orderId":1}`, "00-"+traceID+"-a3ce929d0e0e4736-01"
	m.Update(kafkaMessagesMsg{view: m.serviceEntry(broker).view.(*kafkaView), batch: []kafka.Message{{
		At: at.Add(3 * time.Second), Topic: "orders.events", Value: &value,
		Headers: []kafka.Header{{Name: "traceparent", Value: &parent}}}}})
	app := services.NewService("shop", "app", "shop:latest")
	m.Update(services.Added{Service: app})
	m.Update(services.Lines{Service: app, Gen: m.gen, Lines: []*logformat.Line{logformat.NewLine(at.Add(300*time.Millisecond),
		"2026-10-12 15:45:51.006  INFO 1 --- [shop] [exec-1] ["+traceID+"-00f067aa0ba902b7] c.e.Shop : Order 7 placed")}})
	cache := services.NewService("redis", "redis", "redis:7")
	m.Update(services.Added{Service: cache})
	m.Update(queriesMsg{view: m.serviceEntry(cache).view.(*dbView), batch: []databases.Query{{
		At: at.Add(400 * time.Millisecond), Connection: "c", Kind: "GET", Args: []string{"GET", "cart:7"}, Text: "GET cart:7"}}})

	m.tree.cursor = "t1"
	screen(m)
	press(m, "3")
	if !strings.Contains(screen(m), "t  By trace") {
		t.Fatalf("the events offer to group by trace:\n%s", screen(m))
	}
	press(m, "t")
	var rows []string
	for _, ev := range m.inspector.timeline {
		rows = append(rows, ev.source+" "+ansi.Strip(ev.summary))
	}
	got := strings.Join(rows, "\n")
	for _, want := range []string{"TRACE trace 4bf92f35  3 events over 2.8s, 1 after the test", "HTTP   POST /payments",
		"LOG   INFO Order 7 placed", "KAFKA   orders.events", "after the test", "TRACE Not traced  1 event",
		"REDIS   GET cart:7"} {
		if !strings.Contains(got, want) {
			t.Errorf("the events by trace lack %q:\n%s", want, got)
		}
	}
	if os.Getenv("SHOW") != "" {
		fmt.Println(screen(m))
	}
	press(m, "enter")
	if m.detailFrom == panelServices {
		t.Error("a trace's heading opens no service")
	}
}

func TestNoiseRulesLeaveItemsOut(t *testing.T) {
	m := testModel(t)
	m.opts.Noise = []noise.Rule{{Service: "wiremock", Match: `^GET /health`}, {Match: `(?i)housekeeping`}}
	m.setNoise()
	mock := services.NewService("wiremock", "wiremock", "wiremock/wiremock:3")
	m.Update(services.Added{Service: mock})
	view := m.serviceEntry(mock).view.(*wiremockView)
	request := func(id, method, url string) wiremock.ServeEvent {
		raw, _ := jsonx.ParseObject([]byte(`{"request":{"method":"` + method + `","url":"` + url + `"},"wasMatched":true}`))
		return wiremock.ServeEvent{ID: id, At: time.Now(), Method: method, URL: url, Status: 200, Matched: true, Raw: raw}
	}
	m.Update(wiremockMsg{view: view, poll: wiremockPoll{hasEvents: true, events: []wiremock.ServeEvent{
		request("e1", "GET", "/health"), request("e2", "POST", "/payments"), request("e3", "GET", "/health")}}})
	if len(view.requests.rows) != 1 || !strings.Contains(view.summary(), "1 request") ||
		!strings.Contains(view.summary(), "2 noise") {
		t.Errorf("health checks are noise: %d rows, %s", len(view.requests.rows), view.summary())
	}
	if got := ansi.Strip(serviceStats(m.serviceEntry(mock))); got != "1 req" {
		t.Errorf("the services list counts what is not noise: %q", got)
	}
	m.serviceList.selectEntry(m, m.serviceEntry(mock))
	m.setFocus(m.serviceList)
	m.setFocus(view.requests)
	press(m, "n")
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	if len(view.requests.rows) != 3 || !strings.Contains(screen(m), "with noise") {
		t.Errorf("n shows the noise: %d rows", len(view.requests.rows))
	}
	app := services.NewService("shop", "app", "shop:latest")
	m.Update(services.Added{Service: app})
	m.Update(services.Lines{Service: app, Gen: m.gen, Lines: []*logformat.Line{
		logformat.NewLine(time.Now(), "2026-10-12 15:45:51,006 INFO [main] c.z.h.HikariPool - HikariPool-1 - Housekeeping"),
		logformat.NewLine(time.Now(), "2026-10-12 15:45:51,106 INFO [main] c.e.Shop - Order 7 placed"),
	}})
	log := m.serviceEntry(app).view.(*logView)
	log.rerender(m)
	if rows := strings.Join(log.rows, "\n"); strings.Contains(rows, "Housekeeping") || !strings.Contains(rows, "Order 7") {
		t.Errorf("a rule for every service applies to logs too:\n%s", rows)
	}
	m.opts.Noise = nil
	m.setNoise()
	if len(view.requests.rows) != 3 {
		t.Errorf("without the rules nothing is noise: %d rows", len(view.requests.rows))
	}
}

func TestHideAsNoise(t *testing.T) {
	m := testModel(t)
	m.opts.ConfigPath = filepath.Join(t.TempDir(), ".testdash.json")
	mock := services.NewService("wiremock", "wiremock", "wiremock/wiremock:3")
	m.Update(services.Added{Service: mock})
	view := m.serviceEntry(mock).view.(*wiremockView)
	request := func(id, url string, ms int) wiremock.ServeEvent {
		raw, _ := jsonx.ParseObject([]byte(`{"request":{"method":"GET","url":"` + url + `"},"wasMatched":true}`))
		return wiremock.ServeEvent{ID: id, At: time.Now().Add(time.Duration(ms) * time.Millisecond), Method: "GET",
			URL: url, Status: 200, Matched: true, Raw: raw}
	}
	m.Update(wiremockMsg{view: view, poll: wiremockPoll{hasEvents: true, events: []wiremock.ServeEvent{
		request("e1", "/health/42", 0), request("e2", "/payments", 1), request("e3", "/health/7", 2)}}})
	m.serviceList.selectEntry(m, m.serviceEntry(mock))
	m.setFocus(m.serviceList)
	m.setFocus(view.requests)
	press(m, "g")
	view.requests.moveCursor(0)
	press(m, "N")
	editor, ok := m.popup.(*noiseEditor)
	if !ok || editor.pattern.Value() != `^GET /health/\d+$` {
		t.Fatalf("N suggests a rule from the request: %T %+v", m.popup, editor)
	}
	if s := screen(m); !strings.Contains(s, "Matches 2 of 3 now") || !strings.Contains(s, "GET /health/7") {
		t.Errorf("the editor shows what the pattern matches:\n%s", s)
	}
	press(m, "enter")
	if m.popup != nil || len(view.requests.rows) != 1 {
		t.Fatalf("enter saves, and the requests it matches go: %d rows", len(view.requests.rows))
	}
	if rules, err := noise.LoadConfig(m.opts.ConfigPath); err != nil || len(rules) != 1 ||
		rules[0] != (noise.Rule{Service: "wiremock", Match: `^GET /health/\d+$`}) {
		t.Errorf("the rule is in the build's config: %+v %v", rules, err)
	}
	m.hideAsNoise("wiremock", "^GET /payments$")
	press(m, "tab", "right", "enter")
	if len(m.prefs.Noise) != 1 || m.prefs.Noise[0].Match != "^GET /payments$" || len(view.requests.rows) != 0 {
		t.Errorf("a rule for every build goes in the settings: %+v, %d rows", m.prefs.Noise, len(view.requests.rows))
	}
}

// rightClickOn right-clicks the first place on screen showing text.
func rightClickOn(t *testing.T, m *Model, text string) {
	t.Helper()
	for y, line := range strings.Split(screen(m), "\n") {
		if x := strings.Index(line, text); x >= 0 {
			m.Update(tea.MouseClickMsg{X: ansi.StringWidth(line[:x]), Y: y, Button: tea.MouseRight})
			return
		}
	}
	t.Fatalf("%q is not on screen:\n%s", text, screen(m))
}

func TestRightClickMenus(t *testing.T) {
	m := testModel(t)
	mock := services.NewService("wiremock", "wiremock", "wiremock/wiremock:3")
	m.Update(services.Added{Service: mock})
	view := m.serviceEntry(mock).view.(*wiremockView)
	var events []wiremock.ServeEvent
	for i, url := range []string{"/health", "/payments"} {
		raw, _ := jsonx.ParseObject([]byte(`{"request":{"method":"GET","url":"` + url + `"},"wasMatched":true}`))
		events = append(events, wiremock.ServeEvent{ID: fmt.Sprint(i), At: time.Now().Add(time.Duration(i) * time.Millisecond),
			Method: "GET", URL: url, Status: 200, Matched: true, Raw: raw})
	}
	m.Update(wiremockMsg{view: view, poll: wiremockPoll{hasEvents: true, events: events}})
	m.serviceList.selectEntry(m, m.serviceEntry(mock))
	m.setFocus(m.serviceList)
	rightClickOn(t, m, "/health")
	menu, ok := m.popup.(*contextMenu)
	if !ok || view.requests.cursorKey() != "r:0" {
		t.Fatalf("a right click picks the row and opens a menu: %T %s", m.popup, view.requests.cursorKey())
	}
	var labels []string
	for _, action := range menu.actions {
		labels = append(labels, action.key+" "+action.label)
	}
	if got := strings.Join(labels, ", "); !strings.Contains(got, "N Hide as noise") || !strings.Contains(got, "c Copy as curl") ||
		strings.Contains(got, "Quit") {
		t.Errorf("the menu offers what the row can do, not what testdash does anywhere: %s", got)
	}
	press(m, "N")
	if editor, ok := m.popup.(*noiseEditor); !ok || editor.pattern.Value() != "^GET /health$" {
		t.Fatalf("a menu item's key chooses it: %T", m.popup)
	}
	press(m, "esc")

	app := services.NewService("shop", "app", "shop:latest")
	m.Update(services.Added{Service: app})
	m.Update(services.Lines{Service: app, Gen: m.gen, Lines: []*logformat.Line{
		logformat.NewLine(time.Now(), "2026-10-12 15:45:51,006 INFO [main] c.z.h.HikariPool - pool stats 7"),
		logformat.NewLine(time.Now(), "2026-10-12 15:45:51,106 INFO [main] c.e.Shop - Order 7 placed"),
	}})
	m.serviceList.selectEntry(m, m.serviceEntry(app))
	m.setFocus(m.serviceList)
	rightClickOn(t, m, "pool stats")
	menu, ok = m.popup.(*contextMenu)
	if !ok || menu.actions[0].label != "Hide lines like this as noise" {
		t.Fatalf("a log's menu hides lines like the one clicked: %+v", m.popup)
	}
	press(m, "enter")
	if editor, ok := m.popup.(*noiseEditor); !ok || !strings.Contains(editor.pattern.Value(), `pool stats \d+$`) {
		t.Errorf("the rule starts from that line: %+v", m.popup)
	}
}

func TestNoiseFiltersDialog(t *testing.T) {
	m := testModel(t)
	m.opts.ConfigPath = filepath.Join(t.TempDir(), ".testdash.json")
	m.opts.Noise = []noise.Rule{{Service: "wiremock", Match: "^GET /health"}}
	m.prefs.Noise = []noise.Rule{{Match: "(?i)hikari"}}
	m.setNoise()
	mock := services.NewService("wiremock", "wiremock", "wiremock/wiremock:3")
	m.Update(services.Added{Service: mock})
	raw, _ := jsonx.ParseObject([]byte(`{"request":{"method":"GET","url":"/health"},"wasMatched":true}`))
	m.Update(wiremockMsg{view: m.serviceEntry(mock).view.(*wiremockView), poll: wiremockPoll{hasEvents: true,
		events: []wiremock.ServeEvent{{ID: "e1", At: time.Now(), Method: "GET", URL: "/health", Matched: true, Raw: raw}}}})
	m.openNoiseRules()
	s := screen(m)
	for _, want := range []string{"this build", "wiremock", "^GET /health", "1 now", "every build", "every service",
		"(?i)hikari", "BUILT IN"} {
		if !strings.Contains(s, want) {
			t.Errorf("the dialog lacks %q:\n%s", want, s)
		}
	}
	press(m, "enter")
	editor, ok := m.popup.(*noiseEditor)
	if !ok || editor.pattern.Value() != "^GET /health" || editor.editing == nil {
		t.Fatalf("enter changes the rule: %T", m.popup)
	}
	editor.pattern.SetValue("^GET /(health|ready)")
	press(m, "enter")
	if len(m.opts.Noise) != 1 || m.opts.Noise[0].Match != "^GET /(health|ready)" {
		t.Errorf("the change replaces the rule: %+v", m.opts.Noise)
	}
	m.openNoiseRules()
	press(m, "down", "d")
	if len(m.prefs.Noise) != 0 || len(m.opts.Noise) != 1 {
		t.Errorf("d removes the rule: %+v %+v", m.opts.Noise, m.prefs.Noise)
	}
	press(m, "a")
	if editor, ok := m.popup.(*noiseEditor); !ok || strings.Join(editor.services, ",") != ",gradle,wiremock" {
		t.Errorf("a adds a rule, for every service or any one: %+v", m.popup)
	}
}

func TestNumbers(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1456: "1,456", 107000: "107,000",
		1234567: "1,234,567", -4200: "-4,200"} {
		if got := number(n); got != want {
			t.Errorf("number(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{999: "999", 1000: "1k", 1456: "1.5k", 10500: "10.5k", 99960: "100k",
		107000: "107k", 999499: "999k", 999500: "1M", 2400000: "2.4M"} {
		if got := compact(n); got != want {
			t.Errorf("compact(%d) = %q, want %q", n, got, want)
		}
	}
	if got := count(10234, "query", "queries"); got != "10,234 queries" {
		t.Errorf("count %q", got)
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
	v := newLogView(func() []*logformat.Line { return lines }, func() string { return "" })
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
