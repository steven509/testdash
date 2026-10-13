// Package noise decides what of what the services saw is noise: what health checks, connection pools and the
// like send, which the views leave out until asked. A rule is a regular expression over an item's text (a
// query, a request line, a message, a log line), for one service or every service. A build's rules live in its
// .testdash.json, and rules for every build in the settings.
package noise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Rule says what is noise.
type Rule struct {
	Service string `json:"service,omitempty"` // a service's name as the services list shows it; empty for every one
	Match   string `json:"match"`             // a regular expression over an item's text
}

// Filter is the rules in force.
type Filter struct {
	rules []compiled
}

type compiled struct {
	service string
	re      *regexp.Regexp
}

// New compiles rules, leaving out those that do not compile, which errs says why.
func New(rules ...[]Rule) (*Filter, error) {
	f := &Filter{}
	var errs []error
	for _, set := range rules {
		for _, rule := range set {
			re, err := regexp.Compile(rule.Match)
			if err != nil {
				errs = append(errs, fmt.Errorf("noise rule %q: %w", rule.Match, err))
				continue
			}
			f.rules = append(f.rules, compiled{service: rule.Service, re: re})
		}
	}
	return f, errors.Join(errs...)
}

// Noise is whether a service's item is noise.
func (f *Filter) Noise(service, text string) bool {
	if f == nil {
		return false
	}
	for _, rule := range f.rules {
		if (rule.service == "" || rule.service == service) && rule.re.MatchString(text) {
			return true
		}
	}
	return false
}

var (
	hexID  = regexp.MustCompile(`\b[0-9a-f]*[0-9][0-9a-f]*[a-f][0-9a-f]*\b|\b[0-9a-f]*[a-f][0-9a-f]*[0-9][0-9a-f]*\b`)
	digits = regexp.MustCompile(`[0-9]+`)
)

// Suggest is a pattern for an item's text to start a rule from: its first line, with ids and numbers generalised.
func Suggest(text string) string {
	line, _, more := strings.Cut(strings.TrimSpace(text), "\n")
	if len(line) > 160 {
		line, more = line[:160], true
	}
	var b strings.Builder
	last := 0
	for _, span := range hexID.FindAllStringIndex(line, -1) {
		if span[1]-span[0] < 8 {
			continue
		}
		b.WriteString(generalise(line[last:span[0]]) + "[0-9a-f]+")
		last = span[1]
	}
	b.WriteString(generalise(line[last:]))
	if more {
		return "^" + b.String()
	}
	return "^" + b.String() + "$"
}

func generalise(text string) string {
	return digits.ReplaceAllString(regexp.QuoteMeta(text), `\d+`)
}

// LoadConfig reads the rules in a build's config file; a missing file has none.
func LoadConfig(path string) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || path == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config struct {
		Noise []Rule `json:"noise"`
	}
	err = json.Unmarshal(data, &config)
	return config.Noise, err
}

// SaveConfig writes a build's rules into its config file, keeping the rest of it as it was, in its order.
func SaveConfig(path string, rules []Rule) error {
	var keys []string
	values := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if keys, err = orderedKeys(data, values); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if _, ok := values["noise"]; !ok {
		keys = append(keys, "noise")
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	values["noise"] = encoded
	var object bytes.Buffer
	object.WriteString("{")
	for i, key := range keys {
		if i > 0 {
			object.WriteString(",")
		}
		name, _ := json.Marshal(key)
		object.Write(name)
		object.WriteString(":")
		object.Write(values[key])
	}
	object.WriteString("}")
	var out bytes.Buffer
	if err := json.Indent(&out, object.Bytes(), "", "  "); err != nil {
		return err
	}
	out.WriteString("\n")
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// orderedKeys reads a JSON object's keys in their order, and their values.
func orderedKeys(data []byte, values map[string]json.RawMessage) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if _, seen := values[key]; !seen {
			keys = append(keys, key)
		}
		values[key] = value
	}
	return keys, nil
}
