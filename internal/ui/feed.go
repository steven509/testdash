package ui

import (
	"image/color"
	"sort"
	"strings"

	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/kafka"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/style"
	"github.com/steven509/testdash/internal/trace"
	"github.com/steven509/testdash/internal/wiremock"
)

func requestEvent(entry *svcEntry, v *wiremockView, e wiremock.ServeEvent) timelineEvent {
	summary := style.Bold.Render(cleanText(e.Method)) + " " + cleanText(e.URL) + " " + httpStatus(e.Status)
	if !e.Matched {
		summary += style.Red.Render("  no stub")
	}
	return timelineEvent{at: e.At, source: "HTTP", color: style.Teal, summary: summary, service: entry,
		reveal: func() { v.requests.selectKey("r:" + e.ID) },
		detail: func() string { return v.describe(e) },
		copy:   copyable{"request as curl", curlCommand(e, v.client.BaseURL())}, trace: requestTrace(e),
		noise: requestNoiseText(e)}
}

func messageEvent(entry *svcEntry, v *kafkaView, message kafka.Message) timelineEvent {
	summary := style.Bold.Render(cleanText(message.Topic)) + "  " + style.Dim.Render(preview(message.Value, 80))
	event := timelineEvent{at: message.At, source: "KAFKA", color: style.Pink, summary: summary, service: entry,
		reveal: func() { v.table.selectKey(messageKey(message)) },
		detail: func() string { return describeMessage(message) }, noise: messageNoiseText(message),
		trace: messageTrace(message)}
	if message.Value != nil {
		event.copy = copyable{"payload", *message.Value}
	}
	return event
}

func queryEvent(entry *svcEntry, v *dbView, q databases.Query) timelineEvent {
	source := map[string]string{"mongodb": "MONGO", "redis": "REDIS"}[v.kind]
	if source == "" {
		source = "SQL"
	}
	one, _ := v.noun()
	text := cleanText(q.Text)
	summary := preview(&text, 100)
	if q.Target != "" {
		summary = style.Bold.Render(q.Kind+" "+q.Target) + "  " + style.Dim.Render(preview(&text, 80))
	}
	context, _ := trace.FromText(q.Text)
	return timelineEvent{at: q.At, source: source, color: style.Orange, summary: summary, service: entry,
		reveal: func() { v.selectQuery(q) },
		detail: func() string { return describeQuery(q, entry.svc.Kind) }, copy: copyable{one, q.Text},
		trace: context, noise: queryNoiseText(q)}
}

// logEvent is a log line, with the stack trace lines after it in its details.
func (m *Model) logEvent(entry *svcEntry, lines []*logformat.Line, i int) timelineEvent {
	line := lines[i]
	level, message := "", cleanText(line.Text)
	if record := line.Record(); record != nil {
		level, message = record.Level, cleanText(strings.SplitN(record.Message, "\n", 2)[0])
	} else if line.IsLoggedProblem() {
		level = "WARN"
	}
	color := style.MutedText
	switch level {
	case "ERROR", "FATAL":
		color = style.Fail
	case "WARN":
		color = style.Warn
	}
	summary := message
	if level != "" {
		summary = style.Fg(color).Render(level) + " " + message
	}
	follow := continuation(lines, i)
	context, _ := trace.FromText(line.Text)
	event := timelineEvent{at: line.At, source: "LOG", color: color, trace: context, noise: lineNoiseText(line),
		summary: summary, service: entry, copy: copyable{"line", line.Text},
		detail: func() string {
			var b strings.Builder
			b.WriteString(line.Render(m.logSimple, true))
			for _, more := range follow {
				b.WriteString("\n" + more.Render(m.logSimple, false))
			}
			return b.String()
		}}
	if log, ok := entry.view.(*logView); ok {
		event.reveal = func() { log.reveal(line) }
	}
	return event
}

func (m *Model) gradleEvent(line *logformat.Line) timelineEvent {
	return timelineEvent{at: line.At, source: "gradle", color: style.Primary, summary: strings.TrimSpace(cleanText(line.Text)),
		detail: func() string { return line.Text }, copy: copyable{"line", line.Text},
		reveal: func() { m.outputView.reveal(line) }}
}

// feed is everything the run has shown, in time order: Gradle's output, the applications' log records, the
// other services' warnings and errors, requests, messages and queries, each labelled with where it came from.
// It holds the newest limit of each source, and of them all, and leaves out noise and other traces than the one
// the services are narrowed to.
func (m *Model) feed(limit int) []timelineEvent {
	since := m.runStarted.Add(-correlationSlack)
	var events []timelineEvent
	from := func(source string, c color.Color) func(timelineEvent) bool {
		return func(ev timelineEvent) bool {
			if ev.at.Before(since) || m.traceFilter != "" && ev.trace.TraceID != m.traceFilter {
				return false
			}
			ev.source, ev.color = source, c
			events = append(events, ev)
			return true
		}
	}
	logs := func(lines []*logformat.Line, keep func(*logformat.Line) bool, add func(int) bool) {
		for i, n := len(lines)-1, 0; i >= 0 && n < limit; i-- {
			switch line := lines[i]; {
			case line.At.IsZero():
			case line.At.Before(since):
				return
			case keep(line) && add(i):
				n++
			}
		}
	}
	output, gradle := m.output.all(), from("gradle", style.Primary)
	logs(output, func(line *logformat.Line) bool {
		return strings.TrimSpace(cleanText(line.Text)) != "" && !m.outputView.isNoise(line)
	}, func(i int) bool { return gradle(m.gradleEvent(output[i])) })
	for _, entry := range m.orderedServices() {
		key := entry.svc.Key
		switch v := entry.view.(type) {
		case *wiremockView:
			add := from(key, style.Teal)
			for i, n := len(v.events)-1, 0; i >= 0 && n < limit; i-- {
				if !v.isNoise(v.events[i]) && add(requestEvent(entry, v, v.events[i])) {
					n++
				}
			}
		case *kafkaView:
			add := from(key, style.Pink)
			for i, n := len(v.messages)-1, 0; i >= 0 && n < limit; i-- {
				if !v.isNoise(v.messages[i]) && add(messageEvent(entry, v, v.messages[i])) {
					n++
				}
			}
		case *dbView:
			add := from(key, style.Orange)
			for i, n := len(v.queries)-1, 0; i >= 0 && n < limit; i-- {
				if !v.isNoise(v.queries[i]) && add(queryEvent(entry, v, v.queries[i])) {
					n++
				}
			}
		default:
			app, c := entry.svc.IsApp(), style.MutedText
			if app {
				c = style.Run
			}
			add, lines := from(key, c), entry.lines.all()
			log, _ := entry.view.(*logView)
			logs(lines, func(line *logformat.Line) bool {
				return feedWorthy(line, app) && (log == nil || !log.isNoise(line))
			}, func(i int) bool { return add(m.logEvent(entry, lines, i)) })
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	return lastN(events, limit)
}
