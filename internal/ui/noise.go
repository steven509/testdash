package ui

import (
	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/kafka"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/noise"
	"github.com/steven509/testdash/internal/wiremock"
)

const maxNoiseMarks = 50_000 // past this many, a view's marks start again rather than grow

// noiseMarks remember which of a view's items are noise, so the rules run once for each; forget starts again, as
// when the rules change.
type noiseMarks[K comparable] struct {
	rules func(text string) bool
	gen   func() int // the rules' generation, which changes as they do
	known map[K]bool
}

func (n *noiseMarks[K]) generation() int {
	if n.gen == nil {
		return 0
	}
	return n.gen()
}

func (n *noiseMarks[K]) is(key K, text func() string) bool {
	if n.rules == nil {
		return false
	}
	if noisy, ok := n.known[key]; ok {
		return noisy
	}
	if n.known == nil || len(n.known) > maxNoiseMarks {
		n.known = map[K]bool{}
	}
	noisy := n.rules(text())
	n.known[key] = noisy
	return noisy
}

func (n *noiseMarks[K]) forget() {
	n.known = nil
}

// noiseCount is a view's count of its noise, kept until its items or the rules change.
type noiseCount struct {
	items, trims, generation, n int
}

func (c *noiseCount) get(items, trims, generation int, count func() int) int {
	if c.items != items || c.trims != trims || c.generation != generation {
		*c = noiseCount{items: items, trims: trims, generation: generation, n: count()}
	}
	return c.n
}

type (
	messageID struct {
		topic     string
		partition int
		offset    int64
	}
	queryID struct {
		at               int64
		connection, text string
	}
)

// What the rules match, for each kind of item.
func requestNoiseText(e wiremock.ServeEvent) string {
	return e.Method + " " + e.URL
}

func messageNoiseText(message kafka.Message) string {
	text := message.Topic
	if message.Key != nil {
		text += " " + *message.Key
	}
	if message.Value != nil {
		text += " " + *message.Value
	}
	return text
}

func queryNoiseText(q databases.Query) string {
	if q.Target != "" {
		return q.Kind + " " + q.Target + " " + q.Text
	}
	return q.Text
}

func lineNoiseText(line *logformat.Line) string {
	return line.Text
}

// noiseFor is whether one of a service's items is noise, by the rules in force when asked.
func (m *Model) noiseFor(service string) func(text string) bool {
	return func(text string) bool { return m.noise.Noise(service, text) }
}

// followNoise has a service's views follow the noise rules.
func (m *Model) followNoise(entry *svcEntry) {
	rules, gen := m.noiseFor(entry.svc.Key), func() int { return m.noiseGeneration }
	key, only := entry.svc.Key, func() string { return m.traceFilter }
	switch v := entry.view.(type) {
	case *logView:
		v.noise.rules, v.noise.gen, v.service, v.onlyTrace = rules, gen, key, only
	case *wiremockView:
		v.noise.rules, v.noise.gen, v.onlyTrace = rules, gen, only
		v.log.noise.rules, v.log.noise.gen, v.log.service, v.log.onlyTrace = rules, gen, key, only
	case *kafkaView:
		v.noise.rules, v.noise.gen, v.onlyTrace = rules, gen, only
		v.log.noise.rules, v.log.noise.gen, v.log.service, v.log.onlyTrace = rules, gen, key, only
	case *dbView:
		v.noise.rules, v.noise.gen, v.onlyTrace = rules, gen, only
		v.log.noise.rules, v.log.noise.gen, v.log.service, v.log.onlyTrace = rules, gen, key, only
	}
}

// setNoise puts the build's rules and the settings' in force, and lays every view out again with them.
func (m *Model) setNoise() {
	filter, err := noise.New(m.opts.Noise, m.prefs.Noise)
	m.noise = filter
	m.noiseGeneration++
	if err != nil {
		m.notify(err.Error(), "A noise rule does not compile", "warning")
	}
	m.outputView.noise.forget()
	for _, entry := range m.svcs {
		switch v := entry.view.(type) {
		case *logView:
			v.noise.forget()
		case *wiremockView:
			v.noise.forget()
			v.log.noise.forget()
		case *kafkaView:
			v.noise.forget()
			v.log.noise.forget()
		case *dbView:
			v.noise.forget()
			v.log.noise.forget()
		}
	}
	m.refill()
}

// refill lays every view out again, as when what they leave out changed.
func (m *Model) refill() {
	m.outputView.rerender(m)
	for _, entry := range m.svcs {
		switch v := entry.view.(type) {
		case *logView:
			v.rerender(m)
		case *wiremockView:
			v.fillRequests()
			v.fillNearMisses()
			v.log.rerender(m)
		case *kafkaView:
			v.refillMessages()
			v.log.rerender(m)
		case *dbView:
			v.fillQueries()
			v.log.rerender(m)
		}
	}
	m.inspector.builtAt = -1
	m.version++
}
