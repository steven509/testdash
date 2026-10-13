package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/kafka"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/style"
	"github.com/steven509/testdash/internal/trace"
	"github.com/steven509/testdash/internal/wiremock"
)

// byTrace lays a test's events out by the trace they belong to: each trace seen while the test ran, with every
// event of it in the run, those after the test too, as asynchronous work goes on, and the application's log lines
// that carry it; then the events no trace carried.
func (m *Model) byTrace(w *window, during []timelineEvent) []timelineEvent {
	var order []string
	traced := map[string]bool{}
	for _, ev := range during {
		if id := ev.trace.TraceID; id != "" && !traced[id] {
			traced[id] = true
			order = append(order, id)
		}
	}
	var out []timelineEvent
	if len(order) > 0 {
		end := m.runFinished
		if end.IsZero() {
			end = time.Now()
		}
		run := &window{start: m.runStarted.Add(-correlationSlack), end: end.Add(correlationSlack)}
		all := m.collect(run, func(line *logformat.Line) bool {
			c, ok := trace.FromText(line.Text)
			return ok && traced[c.TraceID]
		}, func(ev *timelineEvent) bool { return traced[ev.trace.TraceID] })
		groups := map[string][]timelineEvent{}
		for _, ev := range all {
			groups[ev.trace.TraceID] = append(groups[ev.trace.TraceID], ev)
		}
		for _, id := range order {
			out = append(out, traceHeading(id, groups[id], w))
			out = append(out, nested(groups[id], w)...)
		}
	}
	var untraced []timelineEvent
	for _, ev := range during {
		if ev.trace.TraceID == "" {
			untraced = append(untraced, ev)
		}
	}
	if len(untraced) > 0 {
		out = append(out, timelineEvent{at: untraced[0].at, source: "TRACE", color: style.SubtleText, header: true,
			summary: style.Muted.Render("Not traced") + style.Dim.Render("  "+count(len(untraced), "event")),
			detail: func() string {
				return style.Bold.Render("Not traced") + "\n\n" + style.Muted.Render("These reached the services while the "+
					"test ran without trace context: no traceparent or B3 header, sqlcommenter comment, or trace id "+
					"in the log line.")
			}})
		out = append(out, nested(untraced, w)...)
	}
	return out
}

// nested indents a trace's events under its heading, and marks those after the test.
func nested(events []timelineEvent, w *window) []timelineEvent {
	out := make([]timelineEvent, len(events))
	for i, ev := range events {
		ev.summary = "  " + ev.summary
		if !w.holds(ev.at) {
			ev.summary += style.Dim.Render("  after the test")
		}
		out[i] = ev
	}
	return out
}

// traceHeading heads a trace's events: its id, how many, over how long, and what each source saw.
func traceHeading(id string, events []timelineEvent, w *window) timelineEvent {
	first, last := events[0].at, events[len(events)-1].at
	after := 0
	sources := map[string]int{}
	spans := map[string]bool{}
	for _, ev := range events {
		if !w.holds(ev.at) {
			after++
		}
		sources[ev.source]++
		if ev.trace.SpanID != "" {
			spans[ev.trace.SpanID] = true
		}
	}
	took := duration(last.Sub(first).Milliseconds())
	summary := style.AccentBold.Render("trace "+id[:8]) + style.Dim.Render("  "+count(len(events), "event")+" over "+took)
	if after > 0 {
		summary += style.Dim.Render(fmt.Sprintf(", %s after the test", number(after)))
	}
	return timelineEvent{at: first, source: "TRACE", color: style.Primary, header: true, summary: summary,
		copy: copyable{"trace id", id}, trace: trace.Context{TraceID: id},
		detail: func() string {
			var names []string
			for source := range sources {
				names = append(names, source)
			}
			sort.Strings(names)
			var seen []string
			for _, source := range names {
				seen = append(seen, fmt.Sprintf("%s %d", source, sources[source]))
			}
			return style.Bold.Render("Trace "+id) + "\n" + style.Dim.Render(clockMillis(first)+" to "+clockMillis(last)+
				", "+took) + "\n\n" + strings.Join(seen, " · ") + "\n" + style.Dim.Render(count(len(spans), "span")+
				" seen") + "\n\n" + style.Muted.Render("Its events from the whole run, those after the test as well: "+
				"what a consumer or a scheduled job did with it later.")
		}}
}

// eventCount is how many events a timeline has, leaving out the traces' headings.
func eventCount(events []timelineEvent) int {
	n := 0
	for _, ev := range events {
		if !ev.header {
			n++
		}
	}
	return n
}

// requestTrace is the trace context a request WireMock received carried.
func requestTrace(e wiremock.ServeEvent) trace.Context {
	request, _ := jsonx.ParseObject(e.Raw.Get("request"))
	headers, _ := jsonx.ParseObject(request.Get("headers"))
	c, _ := trace.FromHeaders(func(name string) string {
		for _, key := range headers.Keys {
			if strings.EqualFold(key, name) {
				if values := headerValues(headers.Get(key)); len(values) > 0 {
					return values[0]
				}
			}
		}
		return ""
	})
	return c
}

// messageTrace is the trace context a Kafka message carries in its headers.
func messageTrace(message kafka.Message) trace.Context {
	c, _ := trace.FromHeaders(func(name string) string {
		for _, header := range message.Headers {
			if strings.EqualFold(header.Name, name) && header.Value != nil {
				return *header.Value
			}
		}
		return ""
	})
	return c
}

// narrowToTrace narrows every service's view to one trace, or with "" shows every trace again.
func (m *Model) narrowToTrace(id string) {
	m.traceFilter = id
	m.refill()
}

// traceBinding is t, to narrow every service to the trace of what is selected, or to show every trace again.
func (m *Model) traceBinding(selected func() (trace.Context, bool)) binding {
	label := "This trace"
	if m.traceFilter != "" {
		label = "All traces"
	}
	return bind("t", label, func() tea.Cmd {
		if m.traceFilter != "" {
			m.narrowToTrace("")
		} else if c, ok := selected(); ok {
			m.narrowToTrace(c.TraceID)
		}
		return nil
	}).when(func() bool {
		_, ok := selected()
		return ok || m.traceFilter != ""
	})
}

// allTracesOnEsc is esc, once a view has nothing nearer to clear, to show every trace again.
func (m *Model) allTracesOnEsc() binding {
	return bind("esc", "All traces", func() tea.Cmd { m.narrowToTrace(""); return nil }).
		when(func() bool { return m.traceFilter != "" })
}

// inTrace is whether something is in the trace a view is narrowed to, or the view is not narrowed.
func inTrace(only func() string, of func() trace.Context) bool {
	if only == nil {
		return true
	}
	id := only()
	return id == "" || of().TraceID == id
}

func traceChip(id string) string {
	if id == "" {
		return ""
	}
	return "trace " + id[:min(8, len(id))]
}
