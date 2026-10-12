// Package logformat reads log lines: parsers for common formats, and the simplified view that keeps only the
// level and message.
//
// Formats: Spring Boot's console pattern (2.x, 3.x, and 3.2+ with the application name and correlation id),
// Logback and Log4j 2's usual patterns, JSON lines (Spring Boot structured logging as Logstash, ECS or GELF, and
// MongoDB's), Kafka, MariaDB/MySQL, Postgres and Redis, and any line that starts with a timestamp. A line no
// parser recognises, such as a stack trace frame, is a continuation of the record above it.
package logformat

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/style"
)

const (
	levelNames = `TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL`
	timestamp  = `\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`
	mdc        = `(?:\[(?P<context>[^\]]*)\]\s?)?` // Spring Boot apps often put MDC values before the message
	gutter     = "      "                          // the level column, so continuation lines line up with messages
)

var patterns = []*regexp.Regexp{
	// 2026-10-12 15:45:51.006  INFO 1 --- [app] [           main] [trace-span] c.l.p.Foo   : [mdc] message
	regexp.MustCompile(`^` + timestamp + `\s+(?P<level>` + levelNames + `)\s+(?:\d+\s+)?---\s+(?:\[[^\]]*\]\s*)+` +
		`[\w.$/\[\]-]+\s*:\s` + mdc + `(?P<message>.*)$`),
	// 15:45:51.006 [main] INFO  c.l.p.Foo -- message, or 2026-10-12 15:45:51,006 INFO  [main] c.l.p.Foo - message
	regexp.MustCompile(`^(?:\d{4}-\d{2}-\d{2}[ T])?\d{2}:\d{2}:\d{2}[.,]\d{3}\S*\s+(?:\[[^\]]+\]\s+)?` +
		`(?P<level>` + levelNames + `)\s+(?:\[[^\]]+\]\s+)?[\w.$]+\s+--?\s` + mdc + `(?P<message>.*)$`),
	// [2026-10-12 15:45:58,569] INFO [QuorumController id=1] message (org.apache.kafka.Foo)
	regexp.MustCompile(`^\[\d{4}-[^\]]+\]\s+(?P<level>` + levelNames + `)\s+(?P<message>.*?)(?:\s+\([\w.$]+\))?$`),
	// 2026-10-12 15:45:25 0 [Note] InnoDB: message, or 2026-10-12T15:45:25.1Z 0 [System] [MY-010116] [Server] message
	regexp.MustCompile(`^` + timestamp + `\s+(?:\d+\s+)?\[(?P<level>Note|Warning|Warn|Error|ERROR|System)\]\s+` +
		`(?:\[MY-\d+\]\s+)?(?P<message>.*)$`),
	// 2026-10-12 15:45:25.123 UTC [1] LOG:  message
	regexp.MustCompile(`^` + timestamp + `(?: [A-Z]{2,5})?\s+\[\d+\]\s+(?P<level>[A-Z]+\d?):\s+(?P<message>.*)$`),
	// 1:M 12 Oct 2026 15:45:25.123 * message
	regexp.MustCompile(`^\d+:[XCSM] \d{1,2} \w{3} \d{4} \d{2}:\d{2}:\d{2}\.\d{3} (?P<level>[.\-*#]) (?P<message>.*)$`),
	// 2026-10-12 15:45:51.006 [WARN] message, or a timestamp and a message (WireMock's own log)
	regexp.MustCompile(`^\[?` + timestamp + `\]?\s+(?:\[?(?P<level>` + levelNames + `)\]?:?\s+)?(?P<message>.*)$`),
}

var (
	levels = map[string]string{"TRACE": "TRACE", "DEBUG": "DEBUG", "INFO": "INFO", "WARN": "WARN", "WARNING": "WARN",
		"ERROR": "ERROR", "FATAL": "FATAL", "SEVERE": "ERROR", "PANIC": "FATAL", "NOTE": "INFO", "SYSTEM": "INFO",
		"LOG": "INFO", "NOTICE": "INFO", "F": "FATAL", "E": "ERROR", "W": "WARN", "I": "INFO", "D": "DEBUG",
		".": "DEBUG", "-": "DEBUG", "*": "INFO", "#": "WARN"}
	syslogLevels = map[int]string{0: "FATAL", 1: "FATAL", 2: "FATAL", 3: "ERROR", 4: "WARN", 5: "INFO", 6: "INFO",
		7: "DEBUG"}
	levelStyle = map[string]style.Style{"FATAL": style.Fatal, "ERROR": style.BoldRed, "WARN": style.BoldYellow,
		"INFO": style.Green, "DEBUG": style.Dim, "TRACE": style.Dim}
	lineStyle = map[string]style.Style{"FATAL": style.Red, "ERROR": style.Red, "WARN": style.Yellow,
		"DEBUG": style.Dim, "TRACE": style.Dim}

	stackFrame      = regexp.MustCompile(`^\s+(?:at |\.\.\. \d+ )`)
	exception       = regexp.MustCompile(`^(?:Caused by: |Suppressed: )?(?:[a-z_$][\w$]*\.)+[A-Z][\w$]*(?:Exception|Error|Throwable)\b`)
	unparsedLevel   = regexp.MustCompile(`\b(FATAL|ERROR|SEVERE|WARN(?:ING)?)\b`)
	unparsedProblem = regexp.MustCompile(`\b(FATAL|ERROR|SEVERE|WARN(?:ING)?)\b|Exception\b|^\s+at |^Caused by:`)
	sgr             = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

func isProblemLevel(level string) bool {
	return level == "WARN" || level == "ERROR" || level == "FATAL"
}

// Record is a parsed log line. Level is empty when the format has none.
type Record struct {
	Level   string
	Message string
	Context string // MDC values, which say what the line is about
}

// Line is one line of a log, with when Docker (or testdash) received it. It keeps what it parses and renders to,
// as every view of a log shows the same lines; only the UI goroutine may call its methods.
type Line struct {
	At   time.Time // zero when the line had no timestamp
	Text string

	parsed      bool
	record      *Record
	renderedFor int // 1 + simple + 2*time; 0 when not rendered yet
	rendered    string
}

func NewLine(at time.Time, text string) *Line {
	return &Line{At: at, Text: Sanitize(text)}
}

func (l *Line) Record() *Record {
	if !l.parsed {
		l.record, l.parsed = Parse(l.Text), true
	}
	return l.record
}

// IsProblem is a warning or error, or part of a stack trace: what the errors-only filter keeps.
func (l *Line) IsProblem() bool {
	if record := l.Record(); record != nil {
		return isProblemLevel(record.Level)
	}
	return unparsedProblem.MatchString(Plain(l.Text))
}

// IsLoggedProblem is a warning or error log entry, without the stack trace lines that follow it.
func (l *Line) IsLoggedProblem() bool {
	if record := l.Record(); record != nil {
		return isProblemLevel(record.Level)
	}
	plain := Plain(l.Text)
	if plain == "" || plain[0] == ' ' || plain[0] == '\t' {
		return false
	}
	return unparsedLevel.MatchString(firstRunes(plain, 200))
}

// Render is the line simplified to its level and message, or as it was logged, after an optional time column.
// A record that carries a stack trace renders to several lines.
func (l *Line) Render(simple, showTime bool) string {
	key := 1
	if simple {
		key++
	}
	if showTime {
		key += 2
	}
	if l.renderedFor != key {
		l.rendered, l.renderedFor = render(l, simple, showTime), key
	}
	return l.rendered
}

func render(l *Line, simple, showTime bool) string {
	var b strings.Builder
	if showTime {
		if l.At.IsZero() {
			b.WriteString(strings.Repeat(" ", 9))
		} else {
			b.WriteString(style.Dim.Render(l.At.Local().Format("15:04:05") + " "))
		}
	}
	record := l.Record()
	if !simple {
		if record != nil && !strings.Contains(l.Text, "\x1b[") {
			if s, ok := lineStyle[record.Level]; ok {
				b.WriteString(s.Render(l.Text))
				return b.String()
			}
		}
		b.WriteString(l.Text)
		return b.String()
	}
	if record == nil {
		b.WriteString(continuation(l.Text))
		return b.String()
	}
	message, more, _ := strings.Cut(record.Message, "\n") // JSON logs carry the stack trace in the record
	message = Sanitize(message)
	if record.Level == "" {
		b.WriteString(message)
	} else {
		b.WriteString(levelStyle[record.Level].Render(padRight(record.Level, 5) + " "))
		if record.Context != "" {
			b.WriteString(style.Dim.Render("[" + record.Context + "] "))
		}
		b.WriteString(lineStyle[record.Level].Render(message))
	}
	if more != "" {
		for _, extra := range strings.Split(strings.TrimRight(more, "\n"), "\n") {
			b.WriteString("\n")
			b.WriteString(continuation(Sanitize(extra)))
		}
	}
	return b.String()
}

func continuation(text string) string {
	if strings.Contains(text, "\x1b[") { // styled by whoever wrote it, like testdash's own Gradle output
		return text
	}
	stripped := strings.TrimSpace(text)
	if exception.MatchString(stripped) {
		return style.Red.Render(gutter + stripped)
	}
	if stackFrame.MatchString(text) {
		return style.Dim.Render(gutter + stripped)
	}
	return text
}

// Parse reads a line's level and message, nil when no format matches.
func Parse(text string) *Record {
	plain := Plain(text)
	if strings.HasPrefix(plain, "{") {
		return parseJSON(plain)
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatchIndex(plain)
		if match == nil {
			continue
		}
		group := func(name string) (string, bool) {
			i := pattern.SubexpIndex(name)
			if i < 0 || match[2*i] < 0 {
				return "", false
			}
			return plain[match[2*i]:match[2*i+1]], true
		}
		level, hasLevel := group("level")
		message, _ := group("message")
		context, _ := group("context")
		normal := ""
		if hasLevel {
			normal = levelOf(level)
			if normal == "" { // Postgres' DETAIL:, STATEMENT: and the like
				message = level + ": " + message
			}
		}
		return &Record{Level: normal, Message: message, Context: strings.TrimSpace(context)}
	}
	return nil
}

func levelOf(name string) string {
	if name == "" {
		return ""
	}
	return levels[strings.ToUpper(name)]
}

func parseJSON(text string) *Record {
	record, ok := jsonx.ParseObject([]byte(text))
	if !ok {
		return nil
	}
	if record.Get("t") != nil && record.Get("s") != nil && record.Get("msg") != nil { // MongoDB
		message := jsonx.Text(record.Get("msg"))
		if attr := record.Get("attr"); jsonx.Truthy(attr) {
			message += "  " + jsonx.Dumps(attr)
		}
		return &Record{Level: jsonLevel(record.Get("s")), Message: message}
	}
	var log jsonx.Object
	if raw := record.Get("log"); raw != nil {
		log, _ = jsonx.ParseObject(raw)
	}
	level := firstTruthy(record.Get("level"), record.Get("log.level"), log.Get("level"), record.Get("severity"))
	message := firstTruthy(record.Get("message"), record.Get("short_message"), record.Get("msg"))
	if message == nil || string(message) == "null" {
		return nil
	}
	text = jsonx.Text(message)
	stack := firstTruthy(record.Get("stack_trace"), record.Get("error.stack_trace"), record.Get("full_message"))
	var trace string
	if json.Unmarshal(stack, &trace) == nil && strings.TrimSpace(trace) != "" && trace != text {
		text += "\n" + trace
	}
	return &Record{Level: jsonLevel(level), Message: text}
}

// firstTruthy is Python's `a or b or c`: the first truthy value, else the last one.
func firstTruthy(values ...json.RawMessage) json.RawMessage {
	for _, v := range values {
		if jsonx.Truthy(v) {
			return v
		}
	}
	return values[len(values)-1]
}

func jsonLevel(raw json.RawMessage) string {
	var number float64
	if json.Unmarshal(raw, &number) == nil { // GELF uses syslog severities
		if number != math.Trunc(number) {
			return ""
		}
		return syslogLevels[int(number)]
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return levelOf(name)
	}
	return ""
}

// Plain is text without its SGR styles.
func Plain(text string) string {
	if !strings.Contains(text, "\x1b") {
		return text
	}
	return sgr.ReplaceAllString(text, "")
}

// Sanitize keeps a line's SGR styles and drops every other escape sequence and control character, which would
// move the cursor or change the terminal; tabs become spaces to the next multiple of eight.
func Sanitize(text string) string {
	clean := true
	for i := 0; i < len(text); i++ {
		if c := text[i]; c < 0x20 || c == 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return text
	}
	var b strings.Builder
	column := 0
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == 0x1b:
			i += escapeLength(text[i:], &b)
			continue
		case c == '\t':
			spaces := 8 - column%8
			b.WriteString(strings.Repeat(" ", spaces))
			column += spaces
		case c < 0x20 || c == 0x7f:
		default:
			r, size := utf8.DecodeRuneInString(text[i:])
			b.WriteString(text[i : i+size])
			if r != utf8.RuneError || size > 1 {
				column++
			}
			i += size
			continue
		}
		i++
	}
	return b.String()
}

// escapeLength returns how long the escape sequence at the start of s is, writing it to b when it is SGR.
func escapeLength(s string, b *strings.Builder) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for j := 2; j < len(s); j++ {
			if c := s[j]; c >= 0x40 && c <= 0x7e {
				if c == 'm' {
					b.WriteString(s[:j+1])
				}
				return j + 1
			}
		}
		return len(s)
	case ']', 'P', '_', '^': // OSC, DCS, APC, PM: up to BEL or ST
		for j := 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	}
	return 2
}

func padRight(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

func firstRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}
