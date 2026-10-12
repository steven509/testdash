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
	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/kafka"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/progress"
	"github.com/steven509/testdash/internal/services"
	"github.com/steven509/testdash/internal/settings"
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

func TestFooterShowsZoom(t *testing.T) {
	m := testModel(t)
	lines := strings.Split(screen(m), "\n")
	if footer := lines[len(lines)-1]; !strings.Contains(footer, "z  Zoom") {
		t.Errorf("the footer shows z with the other keys: %q", footer)
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
