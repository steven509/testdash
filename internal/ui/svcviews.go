package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"testdash/internal/databases"
	"testdash/internal/jsonx"
	"testdash/internal/kafka"
	"testdash/internal/logformat"
	"testdash/internal/runner"
	"testdash/internal/style"
	"testdash/internal/wiremock"
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

// frame draws the info line, the hint and the tabs, and returns the area left for the active pane.
func (t *tabbedView) frame(m *Model, r rect, info, hint string, onTab func()) ([]string, rect) {
	out := []string{bar(info, r.w, style.FgBg(style.MutedText, style.Surface))}
	tabLine, spans := tabs(t.tabLabels, t.active, r.w-1)
	for i, span := range spans {
		index := i
		m.addZone(zone{r: rect{r.x + 1 + span[0], r.y + len(out), span[1] - span[0], 1}, click: func(int, int) tea.Cmd {
			t.active = index
			onTab()
			return nil
		}})
	}
	out = append(out, " "+tabLine)
	for _, line := range wrapLines(hint, r.w-2) {
		out = append(out, " "+style.Dim.Render(line))
	}
	return out, rect{r.x, r.y + len(out), r.w, max(0, r.h-len(out))}
}

// tablePane draws a table with the details of its cursor row on the right, or the table alone.
func tablePane(m *Model, r rect, t *table, detail *textbox) []string {
	if detail == nil {
		lines := t.view(r.w, r.h, m.focused() == t)
		m.addZone(tableZone(m, r, t))
		return lines
	}
	widths := splitWidth(r.w, 3, 2)
	left := t.view(widths[0], r.h, m.focused() == t)
	m.addZone(tableZone(m, rect{r.x, r.y, widths[0], r.h}, t))
	right := leftBorder(padded(detail.view(widths[1]-3, r.h), widths[1]-1, r.h, 1, 1))
	m.addZone(zone{r: rect{r.x + widths[0], r.y, widths[1], r.h}, click: func(int, int) tea.Cmd {
		m.setFocus(detail)
		return nil
	}, wheel: detail.scroll})
	return hjoin(left, right)
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

func tableLevel(t *table, onSelect func() tea.Cmd) level {
	return level{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) {
		handled, selected := t.key(k)
		if selected && onSelect != nil {
			return true, onSelect()
		}
		return handled, nil
	}}
}

func detailLevel(b *textbox) level {
	return level{raw: func(k tea.KeyPressMsg) (bool, tea.Cmd) { return b.key(k), nil }}
}

func tabBindings(t *tabbedView, onTab func()) []binding {
	return []binding{
		bind("left", "Tab", func() tea.Cmd { t.step(-1); onTab(); return nil }).hidden().first(),
		bind("right", "Switch tab", func() tea.Cmd { t.step(1); onTab(); return nil }).shown("← →").first(),
	}
}

// modePills shows what a view is narrowed to, a pill for each.
func modePills(modes ...string) string {
	var b strings.Builder
	for _, mode := range modes {
		if mode != "" {
			b.WriteString("  " + style.Pill(mode, style.Warn, style.Tint(style.Warn)))
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
	events        []wiremock.ServeEvent
	nearMisses    []wiremock.NearMiss
	stubs         []jsonx.Object
	unmatchedOnly bool
	window        *window
	requests      *table
	misses        *table
	stubTable     *table
	details       [3]*textbox
	raw           map[string]string // row key -> its details
	log           *logView
	reset         chan struct{}
}

const wiremockHint = "←/→ switch tab   ↑/↓ choose a row, its details show on the right   u unmatched only   Esc back"

type wiremockPoll struct {
	events     []wiremock.ServeEvent
	nearMisses []wiremock.NearMiss
	stubs      []jsonx.Object
	hasEvents  bool
	hasMisses  bool
	hasStubs   bool
}

func newWiremockView(m *Model, entry *svcEntry, info func() string) *wiremockView {
	v := &wiremockView{entry: entry, client: wiremock.New(entry.svc), info: info, raw: map[string]string{},
		requests:  newTable("Time", "Status", "Stub", "Method", "URL"),
		misses:    newTable("Time", "Method", "URL", "Closest stub"),
		stubTable: newTable("Stub", "Method", "URL", "Status", "Priority"),
		details:   [3]*textbox{{}, {}, {}},
		log:       newLogView(func() []*logformat.Line { return entry.lines.all() }, func() string { return "" }, ""),
		reset:     make(chan struct{}, 1)}
	v.tabbedView = tabbedView{tabIDs: []string{"requests", "near-misses", "stubs", "log"},
		tabLabels: []string{"Requests", "Unmatched", "Stubs", "Log"}}
	go v.poll(m.ctx, m.send)
	return v
}

// poll asks WireMock every 1.5s while it runs: requests each time, near misses and stubs every third time, or
// straight away when a new request went unmatched, as a short run's containers soon stop.
func (v *wiremockView) poll(ctx context.Context, send func(tea.Msg)) {
	var known []string
	var lastMisses, lastStubs string
	for cycle := 0; ctx.Err() == nil; cycle++ {
		select {
		case <-v.reset:
			known, lastMisses, lastStubs = nil, "", ""
		default:
		}
		if v.entry.svc.State() == "running" {
			var result wiremockPoll
			newUnmatched := false
			if events, ok := v.client.Requests(ctx, 300); ok && len(events) > 0 {
				ids := make([]string, len(events))
				seen := map[string]bool{}
				for _, id := range known {
					seen[id] = true
				}
				for i, e := range events {
					ids[i] = e.ID
					newUnmatched = newUnmatched || (!e.Matched && !seen[e.ID])
				}
				if strings.Join(ids, ",") != strings.Join(known, ",") {
					known = ids
					result.events, result.hasEvents = events, true
				}
			}
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
			if result.hasEvents || result.hasMisses || result.hasStubs {
				send(wiremockMsg{view: v, poll: result})
			}
		}
		sleepContext(ctx, 1500*time.Millisecond)
	}
}

func (v *wiremockView) polled(m *Model, p wiremockPoll) {
	if p.hasEvents {
		v.events = p.events
	}
	if p.hasMisses {
		v.nearMisses = p.nearMisses
	}
	if p.hasStubs {
		v.stubs = p.stubs
		v.fillStubs()
	}
	if p.hasEvents || p.hasMisses {
		v.fillRequests()
		v.fillNearMisses()
	}
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
		if !e.Matched {
			unmatched++
		}
	}
	return fmt.Sprintf("%d requests, %d unmatched, %d stubs", len(v.events), unmatched, len(v.stubs))
}

func (v *wiremockView) render(m *Model, r rect) []string {
	windowLabel, unmatched := "", ""
	if v.window != nil {
		windowLabel = v.window.label
	}
	if v.unmatchedOnly {
		unmatched = "unmatched only"
	}
	info := v.info() + "  " + style.Muted.Render(v.summary()) + modePills(windowLabel, unmatched)
	onTab := func() { m.setFocus(v.main()) }
	out, pane := v.frame(m, r, info, wiremockHint, onTab)
	if v.activeID() == "log" {
		return append(out, v.log.render(m, pane)...)
	}
	t, detail := v.paneOf(v.activeID())
	detail.set(t.cursorKey(), v.raw[t.cursorKey()])
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
		bind("esc", "All requests", func() tea.Cmd { v.setWindow(m, nil); return nil }).
			when(func() bool { return v.window != nil }),
	)}
	if v.activeID() == "log" {
		return append(v.log.levels(m, focus), own)
	}
	t, detail := v.paneOf(v.activeID())
	if focus == detail {
		return []level{detailLevel(detail), own}
	}
	return []level{tableLevel(t, nil), own}
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
	v.events, v.nearMisses, v.raw = nil, nil, map[string]string{}
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
	cursor := v.requests.cursor
	v.requests.clear()
	for _, e := range v.events {
		if (v.unmatchedOnly && e.Matched) || !v.window.holds(e.At) {
			continue
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
		key := "r:" + e.ID
		v.requests.addRow(key, clock(e.At), statusStyle.Render(status), stub, cleanText(e.Method), cleanText(e.URL))
		v.raw[key] = describeRequest(e, nil)
	}
	v.requests.cursor = cursor
}

// fillNearMisses lists every request no stub matched, with the closest stub when WireMock reports one.
func (v *wiremockView) fillNearMisses() {
	cursor := v.misses.cursor
	v.misses.clear()
	closest := map[string]wiremock.NearMiss{}
	for _, miss := range v.nearMisses {
		key := miss.Method + "\x00" + miss.URL + "\x00" + miss.LoggedDate
		if known, ok := closest[key]; !ok || miss.Distance < known.Distance {
			closest[key] = miss
		}
	}
	for _, e := range v.events {
		if e.Matched || !v.window.holds(e.At) {
			continue
		}
		miss, ok := closest[e.Method+"\x00"+e.URL+"\x00"+e.LoggedDate()]
		stub := style.Dim.Render("no stub is close")
		var near *wiremock.NearMiss
		if ok {
			stub = cleanText(wiremock.StubLabel(miss.StubMapping))
			near = &miss
		}
		key := "n:" + e.ID
		v.misses.addRow(key, clock(e.At), cleanText(e.Method), cleanText(e.URL), stub)
		v.raw[key] = describeRequest(e, near)
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
		v.raw[key] = style.Cyan.Render(cleanBlock(jsonx.Indent(stub.Marshal())))
	}
	v.stubTable.cursor = cursor
}

func (v *wiremockView) eventsBetween(w *window) []wiremock.ServeEvent {
	var found []wiremock.ServeEvent
	for _, e := range v.events {
		if w.holds(e.At) {
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
		b.WriteString("\n" + cleanBlock(jsonx.Pretty(body)) + "\n")
	}
	responseRaw := raw.Get("response")
	if !jsonx.Truthy(responseRaw) {
		responseRaw = raw.Get("responseDefinition")
	}
	if response, ok := jsonx.ParseObject(responseRaw); ok && len(response.Keys) > 0 {
		b.WriteString("\n" + style.Bold.Render("→ "+response.String("status")) + "\n")
		if body := response.String("body"); body != "" {
			b.WriteString(cleanBlock(jsonx.Pretty(body)) + "\n")
		}
	}
	stub := raw.Get("stubMapping")
	wasMatched := raw.Get("wasMatched")
	notMatched := wasMatched != nil && !jsonx.Truthy(wasMatched)
	switch {
	case notMatched && near != nil:
		b.WriteString("\n" + style.Red.Render("No stub matched. Closest: "+cleanText(wiremock.StubLabel(near.StubMapping))) + "\n")
		b.WriteString(style.Cyan.Render(cleanBlock(jsonx.Indent(near.StubMapping))) + "\n")
	case notMatched: // WireMock records its built-in 404 as the "stub" here
		b.WriteString("\n" + style.Red.Render("No stub matched, and none is close. The Stubs tab lists them all.") + "\n")
	case jsonx.Truthy(stub):
		b.WriteString("\n" + style.Bold.Render("stub "+cleanText(wiremock.StubLabel(stub))) + "\n")
		b.WriteString(style.Cyan.Render(cleanBlock(jsonx.Indent(stub))) + "\n")
	}
	return styleEachLine(b.String())
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
	entry    *svcEntry
	client   *kafka.Client
	info     func() string
	messages []kafka.Message
	groups   []kafka.GroupLag
	topics   []string
	topic    string
	window   *window
	seen     map[string]bool
	byKey    map[string]kafka.Message
	table    *table
	detail   *textbox
	topicTab *table
	groupTab *table
	log      *logView
	showing  atomic.Bool
	reset    chan struct{}
}

const kafkaHint = "←/→ switch tab   ↑/↓ choose a row, its details show on the right   Enter on a topic shows its messages"

func newKafkaView(m *Model, entry *svcEntry, info func() string) *kafkaView {
	v := &kafkaView{entry: entry, client: kafka.New(entry.svc), info: info, seen: map[string]bool{},
		byKey:    map[string]kafka.Message{},
		table:    newTable("Time", "Topic", "Key", "Value"),
		detail:   &textbox{},
		topicTab: newTable("Topic", "Messages", "Last", "Lag"),
		groupTab: newTable("Group", "Topic", "Partition", "Offset", "End", "Lag", "Consumer"),
		log:      newLogView(func() []*logformat.Line { return entry.lines.all() }, func() string { return "" }, ""),
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
	v.messages = append(v.messages, fresh...)
	if len(v.messages) > maxMessages {
		v.messages = append([]kafka.Message(nil), v.messages[len(v.messages)-maxMessages:]...)
		v.fillMessages()
	} else {
		v.addRows(fresh)
	}
	v.fillTopics()
}

func (v *kafkaView) shown(message kafka.Message) bool {
	if v.topic != "" && message.Topic != v.topic {
		return false
	}
	return v.window.holds(message.At)
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
			behind = style.Green.Render(fmt.Sprint(lag[topic]))
			if lag[topic] != 0 {
				behind = style.BoldRed.Render(fmt.Sprint(lag[topic]))
			}
		}
		v.topicTab.addRow(topic, cleanText(topic), fmt.Sprint(counts[topic]), lastAt, behind)
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
			lag = style.Green.Render(fmt.Sprint(*g.Lag))
			if *g.Lag != 0 {
				lag = style.BoldRed.Render(fmt.Sprint(*g.Lag))
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
	return fmt.Sprintf("%d messages on %d topics", len(v.messages), len(topics))
}

func (v *kafkaView) render(m *Model, r rect) []string {
	topic, windowLabel := "", ""
	if v.topic != "" {
		topic = "topic " + cleanText(v.topic)
	}
	if v.window != nil {
		windowLabel = v.window.label
	}
	v.showing.Store(m.screen == runScreen && m.run.current == v.entry.viewID &&
		(v.activeID() == "topics" || v.activeID() == "groups"))
	info := v.info() + "  " + style.Muted.Render(v.summary()) + modePills(topic, windowLabel)
	out, pane := v.frame(m, r, info, kafkaHint, func() { m.setFocus(v.main()) })
	switch v.activeID() {
	case "log":
		return append(out, v.log.render(m, pane)...)
	case "messages":
		key := v.table.cursorKey()
		if message, ok := v.byKey[key]; ok {
			v.detail.set(key, describeMessage(message))
		} else {
			v.detail.set("", "")
		}
		return append(out, tablePane(m, pane, v.table, v.detail)...)
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
	)}
	switch v.activeID() {
	case "log":
		return append(v.log.levels(m, focus), own)
	case "topics":
		return []level{tableLevel(v.topicTab, func() tea.Cmd {
			if key := v.topicTab.cursorKey(); key != "" {
				v.topic = key
				v.filtered(m)
			}
			return nil
		}), own}
	case "groups":
		return []level{tableLevel(v.groupTab, nil), own}
	}
	if focus == v.detail {
		return []level{detailLevel(v.detail), own}
	}
	return []level{tableLevel(v.table, nil), own}
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
		if w.holds(message.At) {
			found = append(found, message)
		}
	}
	return found
}

func describeMessage(message kafka.Message) string {
	var b strings.Builder
	b.WriteString(style.Bold.Render(cleanText(message.Topic)))
	b.WriteString(style.Dim.Render(fmt.Sprintf("  partition %d, offset %d", message.Partition, message.Offset)) + "\n")
	b.WriteString(style.Dim.Render(clockMillis(message.At)) + "\n")
	b.WriteString(style.Dim.Render("key "))
	if message.Key != nil {
		b.WriteString(cleanText(*message.Key) + "\n")
	} else {
		b.WriteString("null\n")
	}
	for _, header := range message.Headers {
		value := "None"
		if header.Value != nil {
			value = *header.Value
		}
		b.WriteString(style.Dim.Render(cleanText(header.Name+": "+value)) + "\n")
	}
	b.WriteString("\n")
	if message.Value != nil {
		b.WriteString(styleLines(cleanBlock(jsonx.Pretty(*message.Value)), style.Cyan))
	} else {
		b.WriteString(style.Cyan.Render("null"))
	}
	return b.String()
}

// databases

// dbView is a database's queries, as it records them itself (see package databases), and its log.
type dbView struct {
	tabbedView
	entry     *svcEntry
	client    databases.QueryLog
	info      func() string
	queries   []databases.Query
	window    *window
	showNoise bool
	byKey     map[string]databases.Query
	table     *table
	detail    *textbox
	log       *logView
	mongo     bool
}

const dbHint = "←/→ switch tab   ↑/↓ choose a query, all of it shows on the right"

func newDBView(m *Model, entry *svcEntry, info func() string) *dbView {
	v := &dbView{entry: entry, client: databases.ForService(entry.svc), info: info, byKey: map[string]databases.Query{},
		detail: &textbox{}, mongo: entry.svc.Kind == "mongodb",
		log: newLogView(func() []*logformat.Line { return entry.lines.all() }, func() string { return "" }, "")}
	if v.mongo {
		v.table = newTable("Time", "Op", "Collection", "ms", "Command")
	} else {
		v.table = newTable("Time", "Conn", "Kind", "Query")
	}
	v.tabbedView = tabbedView{tabIDs: []string{"queries", "log"}, tabLabels: []string{"Queries", "Log"}}
	if v.client != nil {
		ctx, send := m.ctx, m.send
		go v.client.Follow(ctx, func(batch []databases.Query) { send(queriesMsg{view: v, batch: batch}) })
	}
	return v
}

func (v *dbView) received(m *Model, batch []databases.Query) {
	v.queries = append(v.queries, batch...)
	if len(v.queries) > maxQueries {
		v.queries = append([]databases.Query(nil), v.queries[len(v.queries)-maxQueries:]...)
		v.fillQueries()
	} else {
		v.addRows(batch)
	}
}

func (v *dbView) shown(q databases.Query) bool {
	if q.Noise() && !v.showNoise {
		return false
	}
	return v.window.holds(q.At)
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
		rowStyle := style.Plain
		if q.Noise() {
			rowStyle = style.Dim
		}
		v.table.addRow(key, rowStyle.Render(clock(q.At)), style.Dim.Render(q.Connection), rowStyle.Render(cleanText(q.Kind)),
			rowStyle.Render(ansi.Strip(preview(&text, 110))))
	}
	if len(v.table.rows) > 0 && following {
		v.table.moveCursor(len(v.table.rows) - 1)
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
		return "cannot read queries: " + cleanText(v.client.Error())
	}
	chatter := 0
	for _, q := range v.queries {
		if q.Noise() {
			chatter++
		}
	}
	text := fmt.Sprintf("%d queries", len(v.queries)-chatter)
	if chatter > 0 {
		text += fmt.Sprintf(", %d session chatter", chatter)
	}
	return text
}

func (v *dbView) render(m *Model, r rect) []string {
	windowLabel, noise := "", ""
	if v.window != nil {
		windowLabel = v.window.label
	}
	if v.showNoise {
		noise = "with session chatter"
	}
	info := v.info() + "  " + style.Muted.Render(v.summary()) + modePills(windowLabel, noise)
	hint := dbHint
	if !v.mongo {
		hint += "   n session chatter (SET, SHOW, connects)"
	}
	out, pane := v.frame(m, r, info, hint, func() { m.setFocus(v.main()) })
	if v.activeID() == "log" {
		return append(out, v.log.render(m, pane)...)
	}
	key := v.table.cursorKey()
	if q, ok := v.byKey[key]; ok {
		v.detail.set(key, describeQuery(q))
	} else {
		v.detail.set("", "")
	}
	return append(out, tablePane(m, pane, v.table, v.detail)...)
}

func (v *dbView) levels(m *Model, focus any) []level {
	onTab := func() { m.setFocus(v.main()) }
	own := level{bindings: append(tabBindings(&v.tabbedView, onTab),
		bind("n", "Session chatter", func() tea.Cmd {
			v.showNoise = !v.showNoise
			v.fillQueries()
			return nil
		}).when(func() bool { return !v.mongo }),
		bind("esc", "All queries", func() tea.Cmd { v.setWindow(m, nil); return nil }).
			when(func() bool { return v.window != nil }),
	)}
	if v.activeID() == "log" {
		return append(v.log.levels(m, focus), own)
	}
	if focus == v.detail {
		return []level{detailLevel(v.detail), own}
	}
	return []level{tableLevel(v.table, nil), own}
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
		if w.holds(q.At) && !q.Noise() {
			found = append(found, q)
		}
	}
	return found
}

func describeQuery(q databases.Query) string {
	var b strings.Builder
	stamp := clockMillis(q.At)
	if q.Target == "" {
		b.WriteString(style.Bold.Render(cleanText(q.Kind)))
		b.WriteString(style.Dim.Render(fmt.Sprintf("  connection %s  %s", q.Connection, stamp)) + "\n\n")
		b.WriteString(styleLines(cleanBlock(q.Text), style.Cyan))
		return b.String()
	}
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
	b.WriteString("\n" + styleLines(cleanBlock(jsonx.Indent([]byte(q.Text))), style.Cyan))
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
