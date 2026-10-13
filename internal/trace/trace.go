// Package trace finds the distributed trace something belongs to: W3C trace context and B3 headers on requests
// and messages, Spring Boot's log correlation, MDC and JSON log fields, and sqlcommenter comments on SQL.
package trace

import (
	"regexp"
	"strings"
)

// Context is a trace and a span in it: the span that made a request or sent a message, or that logged a line.
type Context struct {
	TraceID string // 32 hex digits
	SpanID  string // 16 hex digits, when known
}

var (
	traceparent = regexp.MustCompile(`\b[0-9a-f]{2}-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}\b`)
	// Spring Boot 3.4's correlation id, [traceId-spanId], and 3.0 to 3.3's [application,traceId,spanId]
	correlation = regexp.MustCompile(`\[([0-9a-f]{32})-([0-9a-f]{16})\]|\[[^,\[\]]*,([0-9a-f]{16}|[0-9a-f]{32}),` +
		`([0-9a-f]{16})\]`)
	// traceId=…, "traceId": "…", trace_id, trace.id, and the same for the span
	traceField = regexp.MustCompile(`(?i)\btrace[._]?id"?\s*[:=]\s*"?([0-9a-f]{32}|[0-9a-f]{16})\b`)
	spanField  = regexp.MustCompile(`(?i)\bspan[._]?id"?\s*[:=]\s*"?([0-9a-f]{16})\b`)
)

// FromTraceparent reads a W3C traceparent value: version-traceid-parentid-flags.
func FromTraceparent(value string) (Context, bool) {
	match := traceparent.FindStringSubmatch(strings.ToLower(strings.TrimSpace(value)))
	if match == nil || match[1] == strings.Repeat("0", 32) {
		return Context{}, false
	}
	return Context{TraceID: match[1], SpanID: match[2]}, true
}

// FromHeaders reads the trace context a request or message carries: traceparent, or B3 as one header or several.
func FromHeaders(get func(name string) string) (Context, bool) {
	if c, ok := FromTraceparent(get("traceparent")); ok {
		return c, true
	}
	if b3 := strings.Split(strings.ToLower(get("b3")), "-"); len(b3) >= 2 && hex(b3[0]) && hex(b3[1]) {
		return Context{TraceID: pad(b3[0]), SpanID: b3[1]}, true
	}
	if id := strings.ToLower(get("X-B3-TraceId")); hex(id) {
		return Context{TraceID: pad(id), SpanID: strings.ToLower(get("X-B3-SpanId"))}, true
	}
	return Context{}, false
}

// FromText finds a trace in a log line or a query: a traceparent anywhere (as sqlcommenter adds to SQL), Spring
// Boot's correlation, or trace and span id fields.
func FromText(text string) (Context, bool) {
	lower := strings.ToLower(text)
	if c, ok := FromTraceparent(lower); ok {
		return c, true
	}
	if match := correlation.FindStringSubmatch(lower); match != nil {
		if match[1] != "" {
			return Context{TraceID: match[1], SpanID: match[2]}, true
		}
		return Context{TraceID: pad(match[3]), SpanID: match[4]}, true
	}
	if match := traceField.FindStringSubmatch(lower); match != nil {
		c := Context{TraceID: pad(match[1])}
		if span := spanField.FindStringSubmatch(lower); span != nil {
			c.SpanID = span[1]
		}
		return c, true
	}
	return Context{}, false
}

// pad makes a 64-bit B3 trace id the 128-bit one W3C trace context gives it.
func pad(id string) string {
	if len(id) == 16 {
		return strings.Repeat("0", 16) + id
	}
	return id
}

func hex(s string) bool {
	if len(s) != 16 && len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
