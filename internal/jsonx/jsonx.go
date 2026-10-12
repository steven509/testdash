// Package jsonx reads and prints JSON keeping its keys in the order they came in, as the views show what
// WireMock, Kafka and MongoDB sent.
package jsonx

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// ObjectKeys returns a JSON object's keys in the order they appear.
func ObjectKeys(data []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		keys = append(keys, key)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// Object is a JSON object's members in order.
type Object struct {
	Keys   []string
	Values map[string]json.RawMessage
}

// ParseObject reads a JSON object, or returns ok false for anything else.
func ParseObject(data []byte) (Object, bool) {
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil || values == nil {
		return Object{}, false
	}
	keys, err := ObjectKeys(data)
	if err != nil {
		return Object{}, false
	}
	return Object{Keys: keys, Values: values}, true
}

// Get returns a member's raw value, nil when missing.
func (o Object) Get(key string) json.RawMessage {
	return o.Values[key]
}

// Without returns the object without some of its keys, still in order.
func (o Object) Without(keys ...string) Object {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	kept := Object{Values: map[string]json.RawMessage{}}
	for _, k := range o.Keys {
		if !drop[k] {
			kept.Keys = append(kept.Keys, k)
			kept.Values[k] = o.Values[k]
		}
	}
	return kept
}

// Marshal writes the object back out in its order.
func (o Object) Marshal() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.Keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		b.Write(o.Values[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// String is a member's value as text: a string unquoted, anything else as its JSON, "" when missing or null.
func (o Object) String(key string) string {
	return Text(o.Values[key])
}

// Text is a raw value as text: a string unquoted, anything else as its JSON, "" when missing or null.
func Text(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// Truthy is Python's truth of a JSON value: false for null, false, 0, "", [] and {}.
func Truthy(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false", "0", `""`, "[]", "{}":
		return false
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64); err == nil {
		return f != 0
	}
	return true
}

// Dumps is JSON on one line the way Python's json.dumps writes it, with a space after each comma and colon.
func Dumps(raw []byte) string {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return string(raw)
	}
	var b strings.Builder
	inString, escaped := false, false
	for _, c := range compact.Bytes() {
		b.WriteByte(c)
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && (c == ',' || c == ':'):
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// Indent is JSON indented by two spaces, as Python's json.dumps(..., indent=2) writes it.
func Indent(raw []byte) string {
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") != nil {
		return string(raw)
	}
	return b.String()
}

// Pretty is a body indented when it is JSON, and as it is otherwise.
func Pretty(body string) string {
	trimmed := strings.TrimSpace(body)
	if !json.Valid([]byte(trimmed)) || trimmed == "" {
		return body
	}
	return Indent([]byte(trimmed))
}
