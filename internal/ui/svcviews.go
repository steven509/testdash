package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/kafka"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/noise"
	"github.com/steven509/testdash/internal/runner"
	"github.com/steven509/testdash/internal/sqlformat"
	"github.com/steven509/testdash/internal/style"
	"github.com/steven509/testdash/internal/trace"
	"github.com/steven509/testdash/internal/wiremock"
)

const (
	maxMessages = 20_000
	maxQueries  = 20_000
)

// tabbedView is the frame the service views share: an info line, a hint, tabs, and the active tab's pane.
type tabbedView struct {
	tabIDs    []string
	tabLabels []string
	active    int
}

func (t *tabbedView) activeID() string {
	return t.tabIDs[t.active]
}

func (t *tabbedView) setActive(id string) {
	for i, tab := range t.tabIDs {
		if tab == id {
			t.active = i
		}
	}
}

func (t *tabbedView) step(by int) {
	t.active = (t.active + by + len(t.tabIDs)) % len(t.tabIDs)
}

// tabLine draws the view's tabs, with what the view holds on the right, and returns the area left for the
// active pane.
func (t *tabbedView) tabLine(m *Model, r rect, meta string, onTab func()) ([]string, rect) {
	tabLine, spans := tabs(t.tabLabels, t.active, r.w)
	for i, span := range spans {
		index := i
		m.addZone(zone{r: rect{r.x + span[0], r.y, span[1] - span[0], 1}, click: func(int, int) tea.Cmd {
			t.active = index
			onTab()
			return nil
		}})
	}
	used := spans[len(spans)-1][1] + 1
	line := tabLine
	if room := r.w - used; room > 8 {
		meta = ansi.Truncate(meta, room, "…")
		line = fit(ansi.Truncate(tabLine, used, ""), r.w-ansi.StringWidth(meta), false) + meta
	}
	out := []string{line, ""}
	return out, rect{r.x, r.y + len(out), r.w, max(0, r.h-len(out))}
}

// tablePane draws a table with the details of its cursor row on the right, or the table alone.
func tablePane(m *Model, r rect, t *table, detail *textbox) []string {
	if detail == nil {
		lines := t.view(r.w, r.h, m.focus == t)
		m.addZone(tableZone(m, r, t))
		return lines
	}
	widths := splitWidth(r.w, 3, 2)
	left := t.view(widths[0], r.h, m.focus == t)
	m.addZone(tableZone(m, rect{r.x, r.y, widths[0], r.h}, t))
	right := leftBorder(padded(detail.view(widths[1]-3, r.h), widths[1]-1, r.h, 1, 1))
	m.addZone(zone{r: rect{r.x + widths[0], r.y, widths[1], r.h}, click: func(int, int) tea.Cmd {
		m.setFocus(detail)
		return nil
	}, wheel: detail.scroll})
	return hjoin(left, right)
}

// tabbedDetailPane draws a table with the details of its cursor row on the right, under tabs that a click
// chooses between.
func tabbedDetailPane(m *Model, r rect, t *table, detail *textbox, labels []string, active int,
	choose func(tab int)) []string {
	widths := splitWidth(r.w, 3, 2)
	left := t.view(widths[0], r.h, m.focus == t)
	m.addZone(tableZone(m, rect{r.x, r.y, widths[0], r.h}, t))
	inner := detailWidth(r.w)
	tabLine, spans := tabs(labels, active, inner)
	for i, span := range spans {
		tab := i
		m.addZone(zone{r: rect{r.x + widths[0] + 2 + span[0], r.y, span[1] - span[0], 1}, click: func(int, int) tea.Cmd {
			choose(tab)
			return nil
		}})
	}
	body := append([]string{tabLine, ""}, detail.view(inner, max(0, r.h-2))...)
	right := leftBorder(padded(body, widths[1]-1, r.h, 1, 1))
	m.addZone(zone{r: rect{r.x + widths[0], r.y + 2, widths[1], max(0, r.h-2)}, click: func(int, int) tea.Cmd {
		m.setFocus(detail)
		return nil
	}, wheel: detail.scroll})
	return hjoin(left, right)
}

// detailWidth is the width of the details beside a table in a pane so wide.
func detailWidth(paneWidth int) int {
	return splitWidth(paneWidth, 3, 2)[1] - 3
}

func tableZone(m *Model, r rect, t *table) zone {
	return zone{r: r, click: func(_, y int) tea.Cmd {
		m.setFocus(t)
		if row, ok := t.rowAt(y); ok {
			t.moveCursor(row)
		}
		return nil
	}, wheel: t.scroll}
}

// tableLevel moves through a table. With onSelect, enter acts on the row, as label says.
func tableLevel(t *table, label string, onSelect func() tea.Cmd) level {
	l := level{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
		handled, selected := t.key(k)
		if selected && onSelect != nil {
			return true, onSelect()
		}
		return handled, nil
	}}
	if onSelect != nil {
		l.bindings = []binding{bind("enter", label, onSelect)}
	}
	return l
}

func detailLevel(b *textbox) level {
	return level{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) { return b.key(k), nil }}
}

func tabBindings(t *tabbedView, onTab func()) []binding {
	return []binding{
		bind("[ left", "Previous tab", func() tea.Cmd { t.step(-1); onTab(); return nil }).hidden().first(),
		bind("] right", "Next tab", func() tea.Cmd { t.step(1); onTab(); return nil }).shown("[ ]").first(),
	}
}

// modeChips show what a view is narrowed to, a chip for each.
func modeChips(modes ...string) string {
	var b strings.Builder
	for _, mode := range modes {
		if mode != "" {
			b.WriteString("  " + chip(mode))
		}
	}
	return b.String()
}

// WireMock

// wiremockView is WireMock through its admin API: every request and the stub that answered it, the closest
// stubs for the requests no stub matched, the stubs themselves, and the container's log.
type wiremockView struct {
	tabbedView
	entry         *svcEntry
	client        *wiremock.Client
	info          func() string
	events        []wiremock.ServeEvent          // every request this run, oldest first, kept when tests reset WireMock
	byID          map[string]wiremock.ServeEvent // the same, by id
	closest       map[string]wiremock.NearMiss   // nearMissKey -> the closest stub WireMock reported for it
	stubs         []jsonx.Object
	unmatchedOnly bool
	window        *window
	requests      *table
	misses        *table
	stubTable     *table
	details       [3]*textbox
	stubDetails   map[string]string // row key -> the stub as JSON
	log           *logView
	reset         chan struct{}
	unmatched     chan struct{} // a request no stub matched arrived, so ask for near misses before a reset loses them
	noise         noiseMarks[string]
	showNoise     bool
	noiseCount    noiseCount
	trims         int
	onlyTrace     func() string
	runDir        string // where the JUnit listener finds this WireMock and copies its journal
}

const (
	journalEvery = 250 * time.Millisecond
	maxRequests  = 20_000
)

type wiremockPoll struct {
	events     []wiremock.ServeEvent
	nearMisses []wiremock.NearMiss
	stubs      []jsonx.Object
	hasEvents  bool
	hasMisses  bool
	hasStubs   bool
}

func newWiremockView(m *Model, entry *svcEntry, info func() string) *wiremockView {
	v := &wiremockView{entry: entry, client: wiremock.New(entry.svc), info: info, stubDetails: map[string]string{},
		byID: map[string]wiremock.ServeEvent{}, closest: map[string]wiremock.NearMiss{},
		unmatched: make(chan struct{}, 1),
		runDir:    m.opts.RunDir,
		requests:  newTable("Time", "Status", "Stub", "Method", "URL"),
		misses:    newTable("Time", "Method", "URL", "Closest stub"),
		stubTable: newTable("Stub", "Method", "URL", "Status", "Priority"),
		details:   [3]*textbox{{}, {}, {}},
		log:       newLogView(func() []*logformat.Line { return entry.lines.all() }, info),
		reset:     make(chan struct{}, 1)}
	v.tabbedView = tabbedView{tabIDs: []string{"requests", "near-misses", "stubs", "log"},
		tabLabels: []string{"Requests", "Unmatched", "Stubs", "Log"}}
	go v.poll(m.ctx, m.send)
	return v
}

// poll follows the request journal, and asks WireMock for near misses as soon as a request goes unmatched (and
// every 4.5s, as working them out costs WireMock), and for its stubs every 4.5s.
func (v *wiremockView) poll(ctx context.Context, send func(tea.Msg)) {
	go v.followJournal(ctx, send)
	go v.followCopies(ctx, send)
	var lastMisses, lastStubs string
	newUnmatched := false
	for cycle := 0; ctx.Err() == nil; cycle++ {
		select {
		case <-v.reset:
			lastMisses, lastStubs = "", ""
		default:
		}
		if v.entry.svc.State() == "running" {
			var result wiremockPoll
			if newUnmatched || cycle%3 == 0 {
				if misses, ok := v.client.NearMisses(ctx); ok {
					if encoded, _ := json.Marshal(misses); string(encoded) != lastMisses {
						lastMisses = string(encoded)
						result.nearMisses, result.hasMisses = misses, true
					}
				}
			}
			if cycle%3 == 0 {
				if stubs, ok := v.client.Mappings(ctx); ok && len(stubs) > 0 {
					var encoded strings.Builder
					for _, s := range stubs {
						encoded.Write(s.Marshal())
					}
					if encoded.String() != lastStubs {
						lastStubs = encoded.String()
						result.stubs, result.hasStubs = stubs, true
					}
				}
			}
			if result.hasMisses || result.hasStubs {
				send(wiremockMsg{view: v, poll: result})
			}
		}
		newUnmatched = false
		select {
		case <-ctx.Done():
		case <-v.unmatched:
			newUnmatched = true
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// followJournal sends the requests WireMock's journal has that it has not sent before, oldest first. What it
// sent is remembered for the view's life, so a container kept from the last run does not bring its requests back.
func (v *wiremockView) followJournal(ctx context.Context, send func(tea.Msg)) {
	sent := map[string]bool{}
	for ctx.Err() == nil {
		if v.entry.svc.State() == "running" {
			v.client.FollowJournal(ctx, journalEvery, 200, func(events []wiremock.ServeEvent) {
				var fresh []wiremock.ServeEvent
				unmatched := false
				for i := len(events) - 1; i >= 0; i-- { // the journal lists the newest first
					if e := events[i]; !sent[e.ID] {
						sent[e.ID] = true
						fresh = append(fresh, e)
						unmatched = unmatched || !e.Matched
					}
				}
				if len(fresh) > 0 {
					send(wiremockMsg{view: v, poll: wiremockPoll{events: fresh, hasEvents: true}})
				}
				if unmatched {
					select {
					case v.unmatched <- struct{}{}:
					default:
					}
				}
			})
		}
		sleepContext(ctx, time.Second)
	}
}

var journalCopy = regexp.MustCompile(`^\d+-\d+-(.+)\.(requests|near-misses)\.json$`)

// followCopies tells the JUnit listener where this WireMock is, and reads the copies of its journal the listener
// makes as each test starts: a test that resets WireMock can take a request out of the journal a moment after it
// arrived, before followJournal sees it, but not before the next test starts.
func (v *wiremockView) followCopies(ctx context.Context, send func(tea.Msg)) {
	dir := filepath.Join(v.runDir, "wiremock-journal")
	for tick := 0; ctx.Err() == nil; tick++ {
		if tick%4 == 0 {
			v.publishTarget()
		}
		files, _ := os.ReadDir(dir)
		var poll wiremockPoll
		for _, file := range files {
			match := journalCopy.FindStringSubmatch(file.Name())
			if match == nil || match[1] != targetName(v.entry.svc.Key) {
				continue
			}
			path := filepath.Join(dir, file.Name())
			body, err := os.ReadFile(path)
			_ = os.Remove(path)
			if err != nil {
				continue
			}
			if match[2] == "requests" {
				if events, ok := wiremock.ParseJournal(body); ok {
					poll.events, poll.hasEvents = append(poll.events, events...), true
				}
			} else if misses, ok := wiremock.ParseNearMisses(body); ok {
				poll.nearMisses, poll.hasMisses = append(poll.nearMisses, misses...), true
			}
		}
		if poll.hasEvents || poll.hasMisses {
			send(wiremockMsg{view: v, poll: poll})
		}
		sleepContext(ctx, journalEvery)
	}
}

// publishTarget writes where the host reaches this WireMock to [run dir]/wiremock/[service], for the listener,
// while it runs with a published port, and removes it otherwise.
func (v *wiremockView) publishTarget() {
	path := filepath.Join(v.runDir, "wiremock", targetName(v.entry.svc.Key))
	url := ""
	if v.entry.svc.State() == "running" {
		url = v.client.BaseURL()
	}
	current, err := os.ReadFile(path)
	switch {
	case url == "" && err == nil:
		_ = os.Remove(path)
	case url != "" && string(current) != url:
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil && os.WriteFile(path+".partial", []byte(url), 0o644) == nil {
			_ = os.Rename(path+".partial", path)
		}
	}
}

// targetName is a service's key made safe for a file name.
func targetName(key string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_') {
			return r
		}
		return '_'
	}, key)
}

// polled takes in new requests, near misses and stubs. Requests are added, never replaced, as tests that reset
// WireMock empty its journal; near misses are kept for the same reason.
func (v *wiremockView) polled(m *Model, p wiremockPoll) {
	if p.hasEvents {
		v.add(p.events)
	}
	if p.hasMisses {
		for _, miss := range p.nearMisses {
			key := nearMissKey(miss.Method, miss.URL, miss.LoggedDate)
			if known, ok := v.closest[key]; !ok || miss.Distance < known.Distance {
				v.closest[key] = miss
			}
		}
		v.fillNearMisses()
	}
	if p.hasStubs {
		v.stubs = p.stubs
		v.fillStubs()
	}
}

func (v *wiremockView) add(events []wiremock.ServeEvent) {
	var fresh []wiremock.ServeEvent
	for _, e := range events {
		if _, seen := v.byID[e.ID]; !seen {
			v.byID[e.ID] = e
			fresh = append(fresh, e)
		}
	}
	if len(fresh) == 0 {
		return
	}
	earlier := func(a, b wiremock.ServeEvent) bool { return a.At.Before(b.At) }
	inOrder := sort.SliceIsSorted(fresh, func(i, j int) bool { return earlier(fresh[i], fresh[j]) }) &&
		(len(v.events) == 0 || !earlier(fresh[0], v.events[len(v.events)-1]))
	v.events = append(v.events, fresh...)
	if !inOrder {
		sort.SliceStable(v.events, func(i, j int) bool { return earlier(v.events[i], v.events[j]) })
	}
	trimmed := false
	if len(v.events) > maxRequests+maxRequests/10 { // trimmed in steps, as the tables are built again after
		for _, e := range v.events[:len(v.events)-maxRequests] {
			delete(v.byID, e.ID)
		}
		v.events = append([]wiremock.ServeEvent(nil), v.events[len(v.events)-maxRequests:]...)
		trimmed = true
		v.trims++
	}
	if inOrder && !trimmed {
		following := len(v.requests.rows) == 0 || v.requests.cursor >= len(v.requests.rows)-1
		for _, e := range fresh {
			v.addRequestRow(e)
		}
		if following {
			v.requests.moveCursor(len(v.requests.rows) - 1)
		}
	} else {
		v.fillRequests()
	}
	for _, e := range fresh {
		if !e.Matched {
			v.fillNearMisses()
			break
		}
	}
}

func nearMissKey(method, url, loggedDate string) string {
	return method + "\x00" + url + "\x00" + loggedDate
}

// requestDetails describes the request a row stands for, with the closest stub when no stub matched it.
func (v *wiremockView) requestDetails(key string) string {
	if details, ok := v.stubDetails[key]; ok {
		return details
	}
	_, id, _ := strings.Cut(key, ":")
	e, ok := v.byID[id]
	if !ok {
		return ""
	}
	var near *wiremock.NearMiss
	if miss, ok := v.closest[nearMissKey(e.Method, e.URL, e.LoggedDate())]; ok && !e.Matched {
		near = &miss
	}
	return describeRequest(e, near)
}

func (v *wiremockView) main() any {
	switch v.activeID() {
	case "log":
		return v.log
	case "requests":
		return v.requests
	case "near-misses":
		return v.misses
	}
	return v.stubTable
}

// selectedRequest is the request under the cursor of the requests or near misses.
func (v *wiremockView) selectedRequest() (wiremock.ServeEvent, bool) {
	if v.activeID() != "requests" && v.activeID() != "near-misses" {
		return wiremock.ServeEvent{}, false
	}
	t, _ := v.paneOf(v.activeID())
	_, id, _ := strings.Cut(t.cursorKey(), ":")
	e, ok := v.byID[id]
	return e, ok
}

func (v *wiremockView) paneOf(id string) (*table, *textbox) {
	switch id {
	case "requests":
		return v.requests, v.details[0]
	case "near-misses":
		return v.misses, v.details[1]
	}
	return v.stubTable, v.details[2]
}

func (v *wiremockView) focusables(m *Model) []any {
	if v.activeID() == "log" {
		return v.log.focusables(m)
	}
	t, d := v.paneOf(v.activeID())
	return []any{t, d}
}

func (v *wiremockView) summary() string {
	unmatched := 0
	for _, e := range v.events {
		if !e.Matched && !v.isNoise(e) {
			unmatched++
		}
	}
	text := count(len(v.events)-v.noisy(), "request") + ", " + number(unmatched) + " unmatched, " +
		count(len(v.stubs), "stub")
	if n := v.noisy(); n > 0 {
		text += ", " + number(n) + " noise"
	}
	return text
}

func (v *wiremockView) isNoise(e wiremock.ServeEvent) bool {
	return v.noise.is(e.ID, func() string { return requestNoiseText(e) })
}

// noisy is how many of the requests are noise.
func (v *wiremockView) noisy() int {
	return v.noiseCount.get(len(v.events), v.trims, v.noise.generation(), func() int {
		n := 0
		for _, e := range v.events {
			if v.isNoise(e) {
				n++
			}
		}
		return n
	})
}

func (v *wiremockView) render(m *Model, r rect) []string {
	windowLabel, unmatched := "", ""
	if v.window != nil {
		windowLabel = v.window.label
	}
	if v.unmatchedOnly {
		unmatched = "unmatched only"
	}
	meta := style.Muted.Render(v.summary()) +
		modeChips(windowLabel, unmatched, map[bool]string{true: "with noise"}[v.showNoise], traceChip(m.traceFilter))
	onTab := func() { m.setFocus(v.main()) }
	out, pane := v.tabLine(m, r, meta, onTab)
	if v.activeID() == "log" {
		return append(out, v.log.render(m, pane)...)
	}
	t, detail := v.paneOf(v.activeID())
	detail.set(t.cursorKey(), v.requestDetails(t.cursorKey()))
	return append(out, tablePane(m, pane, t, detail)...)
}

func (v *wiremockView) levels(m *Model, focus any) []level {
	onTab := func() { m.setFocus(v.main()) }
	own := level{bindings: append(tabBindings(&v.tabbedView, onTab),
		bind("u", "Unmatched only", func() tea.Cmd {
			v.unmatchedOnly = !v.unmatchedOnly
			v.fillRequests()
			return nil
		}),
		noiseToggle(&v.showNoise, v.noisy, func() {
			v.fillRequests()
			v.fillNearMisses()
		}),
		bind("esc", "All requests", func() tea.Cmd { v.setWindow(m, nil); return nil }).
			when(func() bool { return v.window != nil }),
		m.allTracesOnEsc(),
		m.traceBinding(func() (trace.Context, bool) {
			e, ok := v.selectedRequest()
			c := requestTrace(e)
			return c, ok && c.TraceID != ""
		}),
		bind("c", "Copy as curl", func() tea.Cmd {
			e, _ := v.selectedRequest()
			return m.copyText("the request as curl", curlCommand(e, v.client.BaseURL()))
		}).when(func() bool { _, ok := v.selectedRequest(); return ok }),
		bind("N", "Hide as noise", func() tea.Cmd {
			e, _ := v.selectedRequest()
			m.hideAsNoise(v.entry.svc.Key, noise.Suggest(requestNoiseText(e)))
			return nil
		}).when(func() bool { _, ok := v.selectedRequest(); return ok }),
	)}
	if v.activeID() == "log" {
		return append(v.log.levels(m, focus), own)
	}
	t, detail := v.paneOf(v.activeID())
	if focus == detail {
		return []level{detailLevel(detail), own}
	}
	return []level{tableLevel(t, "", nil), own}
}

func (v *wiremockView) setWindow(m *Model, w *window) {
	v.window = w
	v.fillRequests()
	v.fillNearMisses()
	if w != nil {
		v.setActive("requests")
		m.setFocus(v.main())
	}
}

func (v *wiremockView) clear() {
	v.events, v.byID, v.closest = nil, map[string]wiremock.ServeEvent{}, map[string]wiremock.NearMiss{}
	v.fillRequests()
	v.fillNearMisses()
	for _, d := range v.details {
		d.set("", "")
	}
	v.log.clear()
	select {
	case v.reset <- struct{}{}:
	default:
	}
}

func (v *wiremockView) append(m *Model, lines []*logformat.Line) {
	v.log.append(m, lines)
}

func (v *wiremockView) fillRequests() {
	key := v.requests.cursorKey()
	following := len(v.requests.rows) == 0 || v.requests.cursor >= len(v.requests.rows)-1
	v.requests.clear()
	for _, e := range v.events {
		v.addRequestRow(e)
	}
	if following {
		v.requests.moveCursor(len(v.requests.rows) - 1)
	} else {
		v.requests.selectKey(key)
	}
}

func (v *wiremockView) addRequestRow(e wiremock.ServeEvent) {
	if (v.unmatchedOnly && e.Matched) || !v.window.holds(e.At) || (!v.showNoise && v.isNoise(e)) ||
		!inTrace(v.onlyTrace, func() trace.Context { return requestTrace(e) }) {
		return
	}
	statusStyle := style.Green
	if e.Status == 0 || e.Status >= 400 {
		statusStyle = style.Red
	}
	status := ""
	if e.Status != 0 {
		status = fmt.Sprint(e.Status)
	}
	stub := style.Green.Render("✔")
	if !e.Matched {
		stub = style.BoldRed.Render("✘ none")
	}
	v.requests.addRow("r:"+e.ID, clock(e.At), statusStyle.Render(status), stub, cleanText(e.Method), cleanText(e.URL))
}

// latest moves to the newest requests, misses and stubs.
func (v *wiremockView) latest() {
	for _, t := range []*table{v.requests, v.misses, v.stubTable} {
		t.moveCursor(len(t.rows) - 1)
	}
	v.log.latest()
}

// fillNearMisses lists every request no stub matched, with the closest stub when WireMock reports one.
func (v *wiremockView) fillNearMisses() {
	cursor := v.misses.cursor
	v.misses.clear()
	for _, e := range v.events {
		if e.Matched || !v.window.holds(e.At) || (!v.showNoise && v.isNoise(e)) ||
			!inTrace(v.onlyTrace, func() trace.Context { return requestTrace(e) }) {
			continue
		}
		stub := style.Dim.Render("no stub is close")
		if miss, ok := v.closest[nearMissKey(e.Method, e.URL, e.LoggedDate())]; ok {
			stub = cleanText(wiremock.StubLabel(miss.StubMapping))
		}
		v.misses.addRow("n:"+e.ID, clock(e.At), cleanText(e.Method), cleanText(e.URL), stub)
	}
	v.misses.cursor = cursor
}

func (v *wiremockView) fillStubs() {
	cursor := v.stubTable.cursor
	v.stubTable.clear()
	for _, stub := range v.stubs {
		request, _ := jsonx.ParseObject(stub.Get("request"))
		response, _ := jsonx.ParseObject(stub.Get("response"))
		name := stub.String("name")
		if name == "" {
			id := stub.String("id")
			name = id[:min(8, len(id))]
		}
		method := request.String("method")
		if method == "" {
			method = "ANY"
		}
		key := "s:" + stub.String("id")
		v.stubTable.addRow(key, cleanText(name), cleanText(method), cleanText(wiremock.StubURL(request)),
			response.String("status"), stub.String("priority"))
		v.stubDetails[key] = highlightJSON(cleanBlock(jsonx.Indent(stub.Marshal())))
	}
	v.stubTable.cursor = cursor
}

func (v *wiremockView) eventsBetween(w *window) []wiremock.ServeEvent {
	var found []wiremock.ServeEvent
	for _, e := range v.events {
		if w.holds(e.At) && !v.isNoise(e) {
			found = append(found, e)
		}
	}
	return found
}

// describeRequest is a request WireMock received, with its response and stub, or its closest stub, readably.
func describeRequest(e wiremock.ServeEvent, near *wiremock.NearMiss) string {
	var b strings.Builder
	raw := e.Raw
	request, _ := jsonx.ParseObject(raw.Get("request"))
	url := request.String("absoluteUrl")
	if url == "" {
		url = request.String("url")
	}
	b.WriteString(style.Bold.Render(cleanText(request.String("method")+" "+url)) + "\n")
	if headers, ok := jsonx.ParseObject(request.Get("headers")); ok {
		for _, name := range headers.Keys {
			b.WriteString(style.Dim.Render(cleanText(name+": "+headerValue(headers.Get(name)))) + "\n")
		}
	}
	if body := request.String("body"); body != "" {
		b.WriteString("\n" + prettyBody(body) + "\n")
	}
	responseRaw := raw.Get("response")
	if !jsonx.Truthy(responseRaw) {
		responseRaw = raw.Get("responseDefinition")
	}
	if response, ok := jsonx.ParseObject(responseRaw); ok && len(response.Keys) > 0 {
		b.WriteString("\n" + style.Bold.Render("→ "+response.String("status")) + "\n")
		if body := response.String("body"); body != "" {
			b.WriteString(prettyBody(body) + "\n")
		}
	}
	stub := raw.Get("stubMapping")
	wasMatched := raw.Get("wasMatched")
	notMatched := wasMatched != nil && !jsonx.Truthy(wasMatched)
	switch {
	case notMatched && near != nil:
		b.WriteString("\n" + style.Red.Render("No stub matched. Closest: "+cleanText(wiremock.StubLabel(near.StubMapping))) + "\n")
		b.WriteString(highlightJSON(cleanBlock(jsonx.Indent(near.StubMapping))) + "\n")
	case notMatched: // WireMock records its built-in 404 as the "stub" here
		b.WriteString("\n" + style.Red.Render("No stub matched, and none is close. The Stubs tab lists them all.") + "\n")
	case jsonx.Truthy(stub):
		b.WriteString("\n" + style.Bold.Render("stub "+cleanText(wiremock.StubLabel(stub))) + "\n")
		b.WriteString(highlightJSON(cleanBlock(jsonx.Indent(stub))) + "\n")
	}
	return styleEachLine(b.String())
}

// curlCommand is a curl command that sends a request WireMock received again: to WireMock's published port when
// it has one, so it runs from this machine, else to the address the request came to.
func curlCommand(e wiremock.ServeEvent, baseURL string) string {
	request, _ := jsonx.ParseObject(e.Raw.Get("request"))
	url := request.String("absoluteUrl")
	if baseURL != "" || url == "" {
		url = baseURL + request.String("url")
	}
	lines := []string{"curl " + shellQuote(url)}
	if method := request.String("method"); method != "" && method != "GET" {
		lines = append(lines, "-X "+method)
	}
	if headers, ok := jsonx.ParseObject(request.Get("headers")); ok {
		for _, name := range headers.Keys {
			if !curlSkipsHeader[strings.ToLower(name)] {
				for _, value := range headerValues(headers.Get(name)) {
					lines = append(lines, "-H "+shellQuote(name+": "+value))
				}
			}
		}
	}
	if body := request.String("body"); body != "" {
		lines = append(lines, "--data-raw "+shellQuote(body))
	}
	return strings.Join(lines, " \\\n  ")
}

// curlSkipsHeader are the headers curl sets itself from the command.
var curlSkipsHeader = map[string]bool{"host": true, "content-length": true, "connection": true,
	"transfer-encoding": true}

func headerValues(raw json.RawMessage) []string {
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		return values
	}
	return []string{jsonx.Text(raw)}
}

func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

func headerValue(raw json.RawMessage) string {
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		quoted := make([]string, len(values))
		for i, value := range values {
			quoted[i] = "'" + value + "'"
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}
	return jsonx.Text(raw)
}

// styleEachLine repeats each line's opening style on the lines a multi-line styled span runs over, so a wrapped
// or scrolled part keeps it.
func styleEachLine(text string) string {
	parts := strings.Split(text, "\n")
	open := ""
	for i, part := range parts {
		if open != "" {
			parts[i] = open + part
		}
		if j := strings.LastIndex(part, "\x1b["); j >= 0 {
			end := strings.IndexByte(part[j:], 'm')
			if end >= 0 && part[j:j+end+1] != style.Reset && part[j:j+end+1] != "\x1b[0m" {
				open = part[j : j+end+1]
				parts[i] += style.Reset
			} else {
				open = ""
			}
		} else if open != "" {
			parts[i] += style.Reset
		}
	}
	return strings.Join(parts, "\n")
}

// Kafka

// kafkaView is Kafka through its own tools: every message on every topic, the topics, each consumer group's lag,
// and the container's log.
type kafkaView struct {
	tabbedView
	entry       *svcEntry
	client      *kafka.Client
	info        func() string
	messages    []kafka.Message
	groups      []kafka.GroupLag
	topics      []string
	topic       string
	window      *window
	seen        map[string]bool
	byKey       map[string]kafka.Message
	table       *table
	detail      *textbox
	topicTab    *table
	groupTab    *table
	log         *logView
	showHeaders bool
	noise       noiseMarks[messageID]
	showNoise   bool
	noiseCount  noiseCount
	trims       int
	onlyTrace   func() string // the selected message's details show its headers rather than its value
	showing     atomic.Bool
	reset       chan struct{}
}

func newKafkaView(m *Model, entry *svcEntry, info func() string) *kafkaView {
	v := &kafkaView{entry: entry, client: kafka.New(entry.svc), info: info, seen: map[string]bool{},
		byKey:    map[string]kafka.Message{},
		table:    newTable("Time", "Topic", "Key", "Value"),
		detail:   &textbox{},
		topicTab: newTable("Topic", "Messages", "Last", "Lag"),
		groupTab: newTable("Group", "Topic", "Partition", "Offset", "End", "Lag", "Consumer"),
		log:      newLogView(func() []*logformat.Line { return entry.lines.all() }, info),
		reset:    make(chan struct{}, 1)}
	v.tabbedView = tabbedView{tabIDs: []string{"messages", "topics", "groups", "log"},
		tabLabels: []string{"Messages", "Topics", "Groups", "Log"}}
	ctx, send := m.ctx, m.send
	go v.client.Follow(ctx, func(batch []kafka.Message) { send(kafkaMessagesMsg{view: v, batch: batch}) })
	go v.poll(ctx, send)
	return v
}

// poll lists the topics once, then topics and lag only while their tabs are on screen, as each poll starts JVMs
// in the broker's container.
func (v *kafkaView) poll(ctx context.Context, send func(tea.Msg)) {
	haveTopics := false
	for ctx.Err() == nil {
		select {
		case <-v.reset:
			haveTopics = false
		default:
		}
		onTab := v.showing.Load()
		if v.entry.svc.State() == "running" && (onTab || !haveTopics) {
			topics := v.client.Topics(ctx)
			haveTopics = haveTopics || len(topics) > 0
			msg := kafkaTopicsMsg{view: v, topics: topics}
			if onTab {
				msg.groups, msg.lag = v.client.Groups(ctx), true
			}
			send(msg)
		}
		sleepContext(ctx, 5*time.Second)
	}
}

func (v *kafkaView) polled(m *Model, msg kafkaTopicsMsg) {
	if len(msg.topics) > 0 {
		v.topics = msg.topics
	}
	if msg.lag {
		v.groups = msg.groups
		v.fillGroups()
	}
	v.fillTopics()
}

func (v *kafkaView) received(m *Model, batch []kafka.Message) {
	var fresh []kafka.Message
	for _, message := range batch { // a rebalance can deliver a message again, within a batch too
		key := fmt.Sprintf("%s:%d:%d", message.Topic, message.Partition, message.Offset)
		if !v.seen[key] {
			v.seen[key] = true
			fresh = append(fresh, message)
		}
	}
	if len(fresh) == 0 {
		return
	}
	earlier := func(a, b kafka.Message) bool { return a.At.Before(b.At) }
	inOrder := sort.SliceIsSorted(fresh, func(i, j int) bool { return earlier(fresh[i], fresh[j]) }) &&
		(len(v.messages) == 0 || !earlier(fresh[0], v.messages[len(v.messages)-1]))
	v.messages = append(v.messages, fresh...)
	if !inOrder { // the consumer catches up one partition at a time; the table is in time order all the same
		sort.SliceStable(v.messages, func(i, j int) bool { return earlier(v.messages[i], v.messages[j]) })
	}
	switch {
	case len(v.messages) > maxMessages:
		v.messages = append([]kafka.Message(nil), v.messages[len(v.messages)-maxMessages:]...)
		v.trims++
		v.refillMessages()
	case !inOrder:
		v.refillMessages()
	default:
		v.addRows(fresh)
	}
	v.fillTopics()
}

// refillMessages builds the table again, on the same message unless it was following the newest.
func (v *kafkaView) refillMessages() {
	key, following := v.table.cursorKey(), v.table.cursor >= len(v.table.rows)-1
	v.fillMessages()
	if !following {
		v.table.selectKey(key)
	}
}

func (v *kafkaView) shown(message kafka.Message) bool {
	if v.topic != "" && message.Topic != v.topic {
		return false
	}
	if !v.showNoise && v.isNoise(message) {
		return false
	}
	if !inTrace(v.onlyTrace, func() trace.Context { return messageTrace(message) }) {
		return false
	}
	return v.window.holds(message.At)
}

func (v *kafkaView) isNoise(message kafka.Message) bool {
	return v.noise.is(messageID{message.Topic, message.Partition, message.Offset},
		func() string { return messageNoiseText(message) })
}

// noisy is how many of the messages are noise.
func (v *kafkaView) noisy() int {
	return v.noiseCount.get(len(v.messages), v.trims, v.noise.generation(), func() int {
		n := 0
		for _, message := range v.messages {
			if v.isNoise(message) {
				n++
			}
		}
		return n
	})
}

func (v *kafkaView) fillMessages() {
	v.table.clear()
	v.byKey = map[string]kafka.Message{}
	v.addRows(v.messages)
}

// addRows adds rows in the order messages arrive, the cursor kept on the newest while it is there.
func (v *kafkaView) addRows(messages []kafka.Message) {
	following := len(v.table.rows) == 0 || v.table.cursor >= len(v.table.rows)-1
	for _, message := range messages {
		if !v.shown(message) {
			continue
		}
		key := fmt.Sprintf("%s:%d:%d", message.Topic, message.Partition, message.Offset)
		v.byKey[key] = message
		v.table.addRow(key, clock(message.At), cleanText(message.Topic), preview(message.Key, 24),
			preview(message.Value, 90))
	}
	if len(v.table.rows) > 0 && following {
		v.table.moveCursor(len(v.table.rows) - 1)
	}
}

// latest moves to the newest message.
func (v *kafkaView) latest() {
	v.table.moveCursor(len(v.table.rows) - 1)
	v.log.latest()
}

// messagesPane is the messages, and beside them the selected one's value or headers.
func (v *kafkaView) messagesPane(m *Model, r rect) []string {
	inner := detailWidth(r.w)
	key := v.table.cursorKey()
	message, ok := v.byKey[key]
	headers := "Headers"
	if ok {
		headers = "Headers " + number(len(message.Headers))
	}
	active := 0
	if v.showHeaders {
		active = 1
	}
	switch {
	case !ok:
		v.detail.set("", "")
	case v.showHeaders:
		v.detail.set(key+"/headers", messageHeading(message)+"\n"+messageHeaders(message, inner))
	default:
		v.detail.set(key+"/value", messageHeading(message)+"\n"+messageValue(message))
	}
	return tabbedDetailPane(m, r, v.table, v.detail, []string{"Value", headers}, active,
		func(tab int) { v.showHeaders = tab == 1 })
}

func messageKey(message kafka.Message) string {
	return fmt.Sprintf("%s:%d:%d", message.Topic, message.Partition, message.Offset)
}

func (v *kafkaView) fillTopics() {
	cursor := v.topicTab.cursor
	v.topicTab.clear()
	counts := map[string]int{}
	last := map[string]time.Time{}
	for _, message := range v.messages {
		counts[message.Topic]++
		last[message.Topic] = message.At
	}
	lag := map[string]int64{}
	hasLag := map[string]bool{}
	for _, g := range v.groups {
		hasLag[g.Topic] = true
		if g.Lag != nil {
			lag[g.Topic] += *g.Lag
		}
	}
	all := map[string]bool{}
	for _, t := range v.topics {
		all[t] = true
	}
	for t := range counts {
		all[t] = true
	}
	var names []string
	for t := range all {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, topic := range names {
		lastAt := ""
		if at, ok := last[topic]; ok {
			lastAt = clock(at)
		}
		behind := ""
		if hasLag[topic] {
			behind = style.Green.Render(number(int(lag[topic])))
			if lag[topic] != 0 {
				behind = style.BoldRed.Render(number(int(lag[topic])))
			}
		}
		v.topicTab.addRow(topic, cleanText(topic), number(counts[topic]), lastAt, behind)
	}
	v.topicTab.cursor = cursor
}

func (v *kafkaView) fillGroups() {
	v.groupTab.clear()
	groups := append([]kafka.GroupLag{}, v.groups...)
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Topic != b.Topic {
			return a.Topic < b.Topic
		}
		return a.Partition < b.Partition
	})
	optional := func(n *int64) string {
		if n == nil || *n == 0 {
			return "-"
		}
		return fmt.Sprint(*n)
	}
	for _, g := range groups {
		lag := style.Green.Render("-")
		if g.Lag != nil {
			lag = style.Green.Render(number(int(*g.Lag)))
			if *g.Lag != 0 {
				lag = style.BoldRed.Render(number(int(*g.Lag)))
			}
		}
		consumer := cleanText(g.Consumer)
		if consumer == "" {
			consumer = style.Dim.Render("no active member")
		}
		v.groupTab.addRow("", cleanText(g.Group), cleanText(g.Topic), fmt.Sprint(g.Partition), optional(g.Offset),
			optional(g.End), lag, consumer)
	}
}

func (v *kafkaView) main() any {
	switch v.activeID() {
	case "log":
		return v.log
	case "topics":
		return v.topicTab
	case "groups":
		return v.groupTab
	}
	return v.table
}

func (v *kafkaView) focusables(m *Model) []any {
	switch v.activeID() {
	case "log":
		return v.log.focusables(m)
	case "messages":
		return []any{v.table, v.detail}
	}
	return []any{v.main()}
}

func (v *kafkaView) summary() string {
	if err := v.client.Error(); err != "" {
		return "cannot read messages: " + cleanText(err)
	}
	topics := map[string]bool{}
	for _, message := range v.messages {
		topics[message.Topic] = true
	}
	text := count(len(v.messages)-v.noisy(), "message") + " on " + count(len(topics), "topic")
	if n := v.noisy(); n > 0 {
		text += ", " + number(n) + " noise"
	}
	return text
}

func (v *kafkaView) render(m *Model, r rect) []string {
	topic, windowLabel := "", ""
	if v.topic != "" {
		topic = "topic " + cleanText(v.topic)
	}
	if v.window != nil {
		windowLabel = v.window.label
	}
	v.showing.Store(m.detailFrom == panelServices && m.serviceList.selected(m) == v.entry &&
		(v.activeID() == "topics" || v.activeID() == "groups"))
	meta := style.Muted.Render(v.summary()) +
		modeChips(topic, windowLabel, map[bool]string{true: "with noise"}[v.showNoise], traceChip(m.traceFilter))
	out, pane := v.tabLine(m, r, meta, func() { m.setFocus(v.main()) })
	switch v.activeID() {
	case "log":
		return append(out, v.log.render(m, pane)...)
	case "messages":
		return append(out, v.messagesPane(m, pane)...)
	case "topics":
		return append(out, tablePane(m, pane, v.topicTab, nil)...)
	}
	return append(out, tablePane(m, pane, v.groupTab, nil)...)
}

func (v *kafkaView) levels(m *Model, focus any) []level {
	onTab := func() { m.setFocus(v.main()) }
	own := level{bindings: append(tabBindings(&v.tabbedView, onTab),
		bind("esc", "All messages", func() tea.Cmd {
			v.topic, v.window = "", nil
			v.filtered(m)
			return nil
		}).when(func() bool { return v.topic != "" || v.window != nil }),
		m.allTracesOnEsc(),
		m.traceBinding(func() (trace.Context, bool) {
			message, ok := v.byKey[v.table.cursorKey()]
			c := messageTrace(message)
			return c, ok && v.activeID() == "messages" && c.TraceID != ""
		}),
		noiseToggle(&v.showNoise, v.noisy, v.refillMessages).
			when(func() bool { return v.activeID() == "messages" && (v.showNoise || v.noisy() > 0) }),
		bind("h", map[bool]string{false: "Headers", true: "Value"}[v.showHeaders], func() tea.Cmd {
			v.showHeaders = !v.showHeaders
			return nil
		}).when(func() bool { return v.activeID() == "messages" }),
		bind("N", "Hide as noise", func() tea.Cmd {
			m.hideAsNoise(v.entry.svc.Key, noise.Suggest(messageNoiseText(v.byKey[v.table.cursorKey()])))
			return nil
		}).when(func() bool {
			_, ok := v.byKey[v.table.cursorKey()]
			return v.activeID() == "messages" && ok
		}),
		bind("c", "Copy payload", func() tea.Cmd {
			return m.copyText("the payload", *v.byKey[v.table.cursorKey()].Value)
		}).when(func() bool {
			message, ok := v.byKey[v.table.cursorKey()]
			return v.activeID() == "messages" && ok && message.Value != nil
		}),
	)}
	switch v.activeID() {
	case "log":
		return append(v.log.levels(m, focus), own)
	case "topics":
		return []level{tableLevel(v.topicTab, "Messages", func() tea.Cmd {
			if key := v.topicTab.cursorKey(); key != "" {
				v.topic = key
				v.filtered(m)
			}
			return nil
		}), own}
	case "groups":
		return []level{tableLevel(v.groupTab, "", nil), own}
	}
	if focus == v.detail {
		return []level{detailLevel(v.detail), own}
	}
	return []level{tableLevel(v.table, "", nil), own}
}

func (v *kafkaView) setWindow(m *Model, w *window) {
	v.window = w
	v.filtered(m)
}

func (v *kafkaView) filtered(m *Model) {
	v.fillMessages()
	if v.activeID() != "messages" {
		v.setActive("messages")
		m.setFocus(v.main())
	}
}

func (v *kafkaView) clear() {
	v.messages, v.groups, v.topics = nil, nil, nil
	v.seen, v.byKey = map[string]bool{}, map[string]kafka.Message{}
	v.fillMessages()
	v.fillTopics()
	v.groupTab.clear()
	v.detail.set("", "")
	v.log.clear()
	select {
	case v.reset <- struct{}{}:
	default:
	}
}

func (v *kafkaView) append(m *Model, lines []*logformat.Line) {
	v.log.append(m, lines)
}

func (v *kafkaView) messagesBetween(w *window) []kafka.Message {
	var found []kafka.Message
	for _, message := range v.messages {
		if w.holds(message.At) && !v.isNoise(message) {
			found = append(found, message)
		}
	}
	return found
}

// describeMessage is a message's topic, place, time, key and value, then its headers, for a test's timeline.
func describeMessage(message kafka.Message) string {
	text := messageHeading(message) + "\n" + messageValue(message)
	if len(message.Headers) > 0 {
		text += "\n\n" + section("Headers", 72, number(len(message.Headers))) + "\n" + messageHeaders(message, 72)
	}
	return text
}

// messageHeading is where a message is and when it was written, and its key.
func messageHeading(message kafka.Message) string {
	var b strings.Builder
	b.WriteString(style.Bold.Render(cleanText(message.Topic)))
	b.WriteString(style.Dim.Render(fmt.Sprintf("  partition %d, offset %d", message.Partition, message.Offset)) + "\n")
	b.WriteString(style.Dim.Render(clockMillis(message.At)) + "\n")
	b.WriteString(style.Dim.Render("key "))
	if message.Key != nil {
		b.WriteString(cleanText(*message.Key) + "\n")
	} else {
		b.WriteString(style.Dim.Render("null") + "\n")
	}
	return b.String()
}

func messageValue(message kafka.Message) string {
	if message.Value == nil {
		return style.Cyan.Render("null")
	}
	if jsonValue(*message.Value) {
		return highlightJSON(cleanBlock(jsonx.Pretty(*message.Value)))
	}
	return styleLines(cleanBlock(*message.Value), style.Cyan)
}

// prettyBody is a request's or response's body, indented and coloured when it is JSON.
func prettyBody(body string) string {
	if jsonValue(body) {
		return highlightJSON(cleanBlock(jsonx.Pretty(body)))
	}
	return cleanBlock(body)
}

// messageHeaders lists a message's headers, names lined up, a JSON value indented below its name.
func messageHeaders(message kafka.Message, width int) string {
	if len(message.Headers) == 0 {
		return style.Dim.Render("No headers")
	}
	nameWidth := 0
	for _, header := range message.Headers {
		nameWidth = max(nameWidth, min(28, len([]rune(cleanText(header.Name)))))
	}
	var b strings.Builder
	for _, header := range message.Headers {
		name := style.Muted.Render(fit(cleanText(header.Name), nameWidth, true))
		switch {
		case header.Value == nil:
			b.WriteString(name + "  " + style.Dim.Render("null") + "\n")
		case jsonValue(*header.Value):
			b.WriteString(name + "\n" + indent(highlightJSON(cleanBlock(jsonx.Pretty(*header.Value))), "  ") + "\n")
		case nameWidth+2+len([]rune(cleanText(*header.Value))) > width: // too long to sit beside its name
			value := cleanText(*header.Value)
			wrapped := strings.Join(wrapLines(value, width-2), "\n")
			if !strings.Contains(value, " ") { // an id or a trace: cut at the edge, not at its hyphens
				wrapped = ansi.Hardwrap(value, width-2, true)
			}
			b.WriteString(name + "\n" + indent(wrapped, "  ") + "\n")
		default:
			b.WriteString(name + "  " + cleanText(*header.Value) + "\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func jsonValue(text string) bool {
	trimmed := strings.TrimSpace(text)
	return (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed))
}

func indent(text, by string) string {
	return by + strings.ReplaceAll(text, "\n", "\n"+by)
}

// databases

// dbView is a database's queries, as it records them itself (see package databases), and its log.
type dbView struct {
	tabbedView
	entry      *svcEntry
	client     databases.QueryLog
	info       func() string
	queries    []databases.Query
	window     *window
	showNoise  bool
	noise      noiseMarks[queryID]
	noiseCount noiseCount
	trims      int
	onlyTrace  func() string
	byKey      map[string]databases.Query
	table      *table
	detail     *textbox
	log        *logView
	kind       string // mariadb, mysql, postgres, mongodb or redis
	showRaw    bool   // a SQL query as the database logged it, rather than laid out and coloured
}

func newDBView(m *Model, entry *svcEntry, info func() string) *dbView {
	v := &dbView{entry: entry, client: databases.ForService(entry.svc), info: info, byKey: map[string]databases.Query{},
		detail: &textbox{}, kind: entry.svc.Kind,
		log: newLogView(func() []*logformat.Line { return entry.lines.all() }, info)}
	switch v.kind {
	case "mongodb":
		v.table = newTable("Time", "Op", "Collection", "ms", "Command")
	case "redis":
		v.table = newTable("Time", "Client", "Command", "Arguments")
	default:
		v.table = newTable("Time", "Conn", "Kind", "Query")
		sqlformat.Prepare()
	}
	_, many := v.noun()
	v.tabbedView = tabbedView{tabIDs: []string{"queries", "log"},
		tabLabels: []string{strings.ToUpper(many[:1]) + many[1:], "Log"}}
	if v.client != nil {
		ctx, send := m.ctx, m.send
		go v.client.Follow(ctx, func(batch []databases.Query) { send(queriesMsg{view: v, batch: batch}) })
	}
	return v
}

// sql is whether the database speaks SQL, which has the formatted and raw tabs.
func (v *dbView) sql() bool {
	return v.kind != "mongodb" && v.kind != "redis"
}

// noun is what the database is asked, in the singular and plural.
func (v *dbView) noun() (string, string) {
	if v.kind == "redis" {
		return "command", "commands"
	}
	return "query", "queries"
}

func (v *dbView) received(m *Model, batch []databases.Query) {
	v.queries = append(v.queries, batch...)
	if len(v.queries) > maxQueries {
		v.queries = append([]databases.Query(nil), v.queries[len(v.queries)-maxQueries:]...)
		v.trims++
		v.fillQueries()
	} else {
		v.addRows(batch)
	}
}

func (v *dbView) shown(q databases.Query) bool {
	if !v.showNoise && v.isNoise(q) {
		return false
	}
	if !inTrace(v.onlyTrace, func() trace.Context { c, _ := trace.FromText(q.Text); return c }) {
		return false
	}
	return v.window.holds(q.At)
}

// isNoise is whether a query is noise: session chatter, which testdash knows, or by a rule.
func (v *dbView) isNoise(q databases.Query) bool {
	return q.Noise() || v.noise.is(queryID{q.At.UnixNano(), q.Connection, q.Text},
		func() string { return queryNoiseText(q) })
}

// noisy is how many of the queries are noise.
func (v *dbView) noisy() int {
	return v.noiseCount.get(len(v.queries), v.trims, v.noise.generation(), func() int {
		n := 0
		for _, q := range v.queries {
			if v.isNoise(q) {
				n++
			}
		}
		return n
	})
}

func (v *dbView) fillQueries() {
	v.table.clear()
	v.byKey = map[string]databases.Query{}
	v.addRows(v.queries)
}

func (v *dbView) addRows(queries []databases.Query) {
	following := len(v.table.rows) == 0 || v.table.cursor >= len(v.table.rows)-1
	for _, q := range queries {
		if !v.shown(q) {
			continue
		}
		key := fmt.Sprint(len(v.byKey))
		v.byKey[key] = q
		text := cleanText(q.Text)
		if q.Target != "" {
			v.table.addRow(key, clock(q.At), cleanText(q.Kind), cleanText(q.Target), q.Millis, preview(&text, 90))
			continue
		}
		if v.kind == "redis" {
			text = cleanText(strings.TrimPrefix(strings.TrimPrefix(q.Text, q.Args[0]), " "))
		}
		rowStyle := style.Plain
		if v.isNoise(q) {
			rowStyle = style.Dim
		}
		v.table.addRow(key, rowStyle.Render(clock(q.At)), style.Dim.Render(q.Connection), rowStyle.Render(cleanText(q.Kind)),
			rowStyle.Render(ansi.Strip(preview(&text, 110))))
	}
	if len(v.table.rows) > 0 && following {
		v.table.moveCursor(len(v.table.rows) - 1)
	}
}

// latest moves to the newest query.
func (v *dbView) latest() {
	v.table.moveCursor(len(v.table.rows) - 1)
	v.log.latest()
}

// selectQuery moves the cursor to a query's row.
func (v *dbView) selectQuery(q databases.Query) {
	for key, shown := range v.byKey {
		if shown.At.Equal(q.At) && shown.Text == q.Text && shown.Connection == q.Connection {
			v.table.selectKey(key)
			return
		}
	}
}

func (v *dbView) main() any {
	if v.activeID() == "log" {
		return v.log
	}
	return v.table
}

func (v *dbView) focusables(m *Model) []any {
	if v.activeID() == "log" {
		return v.log.focusables(m)
	}
	return []any{v.table, v.detail}
}

func (v *dbView) summary() string {
	if v.client != nil && v.client.Error() != "" {
		_, many := v.noun()
		return "cannot read " + many + ": " + cleanText(v.client.Error())
	}
	one, many := v.noun()
	text := count(len(v.queries)-v.noisy(), one, many)
	if n := v.noisy(); n > 0 {
		text += ", " + number(n) + " noise"
	}
	return text
}

func (v *dbView) render(m *Model, r rect) []string {
	windowLabel, noise := "", ""
	if v.window != nil {
		windowLabel = v.window.label
	}
	if v.showNoise {
		noise = "with noise"
	}
	meta := style.Muted.Render(v.summary()) + modeChips(windowLabel, noise, traceChip(m.traceFilter))
	out, pane := v.tabLine(m, r, meta, func() { m.setFocus(v.main()) })
	if v.activeID() == "log" {
		return append(out, v.log.render(m, pane)...)
	}
	key := v.table.cursorKey()
	q, ok := v.byKey[key]
	switch {
	case !ok:
		v.detail.set("", "")
	case !v.sql():
		v.detail.set(key, describeQuery(q, v.kind))
	default:
		v.detail.set(fmt.Sprintf("%s/%t", key, v.showRaw), describeSQL(q, v.kind, v.showRaw))
	}
	if !v.sql() {
		return append(out, tablePane(m, pane, v.table, v.detail)...)
	}
	active := map[bool]int{false: 0, true: 1}[v.showRaw]
	return append(out, tabbedDetailPane(m, pane, v.table, v.detail, []string{"Formatted", "Raw"}, active,
		func(tab int) { v.showRaw = tab == 1 })...)
}

func (v *dbView) levels(m *Model, focus any) []level {
	onTab := func() { m.setFocus(v.main()) }
	one, many := v.noun()
	own := level{bindings: append(tabBindings(&v.tabbedView, onTab),
		noiseToggle(&v.showNoise, v.noisy, v.fillQueries),
		bind("esc", "All "+many, func() tea.Cmd { v.setWindow(m, nil); return nil }).
			when(func() bool { return v.window != nil }),
		m.allTracesOnEsc(),
		m.traceBinding(func() (trace.Context, bool) {
			q, ok := v.byKey[v.table.cursorKey()]
			c, traced := trace.FromText(q.Text)
			return c, ok && traced && v.activeID() == "queries"
		}),
		bind("v", map[bool]string{false: "Raw", true: "Formatted"}[v.showRaw], func() tea.Cmd {
			v.showRaw = !v.showRaw
			return nil
		}).when(func() bool { return v.sql() && v.activeID() == "queries" }),
		bind("N", "Hide as noise", func() tea.Cmd {
			m.hideAsNoise(v.entry.svc.Key, noise.Suggest(queryNoiseText(v.byKey[v.table.cursorKey()])))
			return nil
		}).when(func() bool {
			_, ok := v.byKey[v.table.cursorKey()]
			return v.activeID() == "queries" && ok
		}),
		bind("c", "Copy "+one, func() tea.Cmd { return m.copyText("the "+one, v.byKey[v.table.cursorKey()].Text) }).
			when(func() bool {
				_, ok := v.byKey[v.table.cursorKey()]
				return v.activeID() == "queries" && ok
			}),
	)}
	if v.activeID() == "log" {
		return append(v.log.levels(m, focus), own)
	}
	if focus == v.detail {
		return []level{detailLevel(v.detail), own}
	}
	return []level{tableLevel(v.table, "", nil), own}
}

func (v *dbView) setWindow(m *Model, w *window) {
	v.window = w
	v.fillQueries()
	if w != nil && v.activeID() != "queries" {
		v.setActive("queries")
	}
}

func (v *dbView) clear() {
	v.queries, v.byKey = nil, map[string]databases.Query{}
	v.fillQueries()
	v.detail.set("", "")
	v.log.clear()
}

func (v *dbView) append(m *Model, lines []*logformat.Line) {
	v.log.append(m, lines)
}

func (v *dbView) queriesBetween(w *window) []databases.Query {
	var found []databases.Query
	for _, q := range v.queries {
		if w.holds(q.At) && !v.isNoise(q) {
			found = append(found, q)
		}
	}
	return found
}

// describeQuery is a query's details: a SQL query laid out and coloured in its database's dialect, a Redis
// command, or a MongoDB command with what it did.
func describeQuery(q databases.Query, kind string) string {
	switch {
	case kind == "redis":
		return describeRedis(q)
	case q.Target == "":
		return describeSQL(q, kind, false)
	}
	var b strings.Builder
	stamp := clockMillis(q.At)
	b.WriteString(style.Bold.Render(cleanText(q.Kind + " " + q.Target)))
	timing := "  " + stamp
	if q.Millis != "" {
		timing += "  " + q.Millis + " ms"
	}
	b.WriteString(style.Dim.Render(timing) + "\n")
	var stats []string
	for _, stat := range [][2]string{{"planSummary", "plan"}, {"keysExamined", "keys examined"},
		{"docsExamined", "docs examined"}, {"nreturned", "returned"}, {"nMatched", "matched"},
		{"nModified", "modified"}, {"ninserted", "inserted"}, {"ndeleted", "deleted"}} {
		if raw := q.Detail.Get(stat[0]); raw != nil && string(raw) != "null" {
			stats = append(stats, stat[1]+" "+jsonx.Text(raw))
		}
	}
	if len(stats) > 0 {
		b.WriteString(style.Dim.Render(cleanText(strings.Join(stats, " · "))) + "\n")
	}
	if errMsg := q.Detail.String("errMsg"); errMsg != "" {
		b.WriteString(style.Red.Render(cleanText(errMsg)) + "\n")
	}
	b.WriteString("\n" + highlightJSON(cleanBlock(jsonx.Indent([]byte(q.Text)))))
	return b.String()
}

// extra logs from .testdash.json

// followCommand feeds a command's output until it ends or testdash quits: a `tail -f` or `kubectl logs -f`
// would outlive testdash otherwise.
func followCommand(ctx context.Context, command, dir string, onLines func([]string), stopped func()) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		onLines([]string{"testdash: cannot run " + command + ": " + err.Error()})
		stopped()
		return
	}
	go func() {
		_ = cmd.Wait()
		writer.Close()
	}()
	runner.StreamLines(reader, onLines)
	if ctx.Err() == nil {
		stopped()
	}
}

// followFile feeds what is added to a file, starting over when it is replaced or truncated.
func followFile(ctx context.Context, path string, onLines func([]string)) {
	var inode uint64
	var offset int64
	for ctx.Err() == nil {
		if info, err := os.Stat(path); err == nil {
			ino := fileInode(info)
			if ino != inode || info.Size() < offset {
				inode, offset = ino, 0
			}
			if info.Size() > offset {
				if f, err := os.Open(path); err == nil {
					if _, err := f.Seek(offset, io.SeekStart); err == nil {
						chunk, _ := io.ReadAll(io.LimitReader(f, info.Size()-offset))
						offset += int64(len(chunk))
						var lines []string
						scanner := bufio.NewScanner(strings.NewReader(string(chunk)))
						scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
						for scanner.Scan() {
							lines = append(lines, strings.TrimRight(scanner.Text(), "\r"))
						}
						if len(lines) > 0 {
							onLines(lines)
						}
					}
					f.Close()
				}
			}
		}
		sleepContext(ctx, 500*time.Millisecond)
	}
}

func sleepContext(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func fileInode(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Ino)
	}
	return 0
}
