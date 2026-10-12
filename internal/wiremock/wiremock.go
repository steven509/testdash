// Package wiremock reads WireMock's admin API: the request journal, near misses for unmatched requests, and the
// stub mappings.
//
// Testcontainers rarely publishes WireMock's port on the host (compose services sit behind its socat ambassador),
// so the API is called through `docker exec` with the container's own curl, which the official image ships. A
// published port is used directly when there is one.
package wiremock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/services"
)

type ServeEvent struct {
	ID        string
	At        time.Time
	Method    string
	URL       string
	Status    int // 0 when the response has none
	Matched   bool
	Stub      string // the matched stub's name or id, empty when unmatched
	Raw       jsonx.Object
	loggedRaw json.RawMessage // request.loggedDate, to match near misses on
}

// NearMiss is WireMock's closest stub for a request no stub matched.
type NearMiss struct {
	Method, URL, LoggedDate string
	Distance                float64
	StubMapping             json.RawMessage
}

type Client struct {
	service *services.Service
}

func New(service *services.Service) *Client {
	return &Client{service: service}
}

func (c *Client) port() string {
	command := c.service.Command()
	for i, arg := range command {
		if arg == "--port" && i+1 < len(command) {
			return command[i+1]
		}
	}
	var exposed []string
	for _, p := range c.service.ExposedPorts() {
		port, _, _ := strings.Cut(p, "/")
		exposed = append(exposed, port)
	}
	for _, p := range exposed {
		if p == "8080" {
			return "8080"
		}
	}
	if len(exposed) == 0 {
		return "8080"
	}
	return exposed[0]
}

// BaseURL is where the host reaches WireMock, or "" when its port is not published.
func (c *Client) BaseURL() string {
	if hostPort := c.service.HostPort(c.port() + "/tcp"); hostPort != "" {
		return "http://127.0.0.1:" + hostPort
	}
	return ""
}

// Requests returns the journal's latest requests, newest first; ok is false when WireMock could not be asked.
func (c *Client) Requests(ctx context.Context, limit int) ([]ServeEvent, bool) {
	body, ok := c.get(ctx, "/__admin/requests?limit="+strconv.Itoa(limit))
	if !ok {
		return nil, false
	}
	return ParseJournal(body)
}

// FollowJournal reads the journal's latest requests every interval, handing each read to onEvents newest first,
// until ctx ends or the container stops. Tests that reset WireMock take their requests out of the journal, so it
// is read often: over HTTP when the port is published, else with one docker exec that reads it in a loop, as
// starting a docker exec for every read is slow.
func (c *Client) FollowJournal(ctx context.Context, every time.Duration, limit int, onEvents func([]ServeEvent)) {
	path := "/__admin/requests?limit=" + strconv.Itoa(limit)
	if c.service.HostPort(c.port()+"/tcp") == "" && c.streamJournal(ctx, path, every, onEvents) {
		return
	}
	for ctx.Err() == nil && c.service.State() == "running" {
		if body, ok := c.get(ctx, path); ok {
			if events, ok := ParseJournal(body); ok {
				onEvents(events)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(every):
		}
	}
}

var recordEnd = []byte("\n\x1e\n")

// streamJournal reads the journal in a loop inside the container; it is false when that could not start.
func (c *Client) streamJournal(ctx context.Context, path string, every time.Duration,
	onEvents func([]ServeEvent)) bool {
	if c.service.State() != "running" || c.service.ContainerID() == "" {
		return false
	}
	url := "http://localhost:" + c.port() + path
	script := fmt.Sprintf(`while :; do curl -s -m 5 '%[1]s' || wget -qO- -T 5 '%[1]s'; printf '\n\036\n'; `+
		`sleep %.2f; done`, url, every.Seconds())
	cmd := exec.CommandContext(ctx, "docker", "exec", c.service.ContainerID(), "sh", "-c", script)
	output, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return false
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	records := bufio.NewScanner(output)
	records.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	records.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.Index(data, recordEnd); i >= 0 {
			return i + len(recordEnd), data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	read := false
	for records.Scan() {
		if events, ok := ParseJournal(records.Bytes()); ok {
			read = true
			onEvents(events)
		}
	}
	return read
}

// ParseJournal reads a /__admin/requests reply, newest first as WireMock lists them.
func ParseJournal(body []byte) ([]ServeEvent, bool) {
	var journal struct {
		Requests []json.RawMessage `json:"requests"`
	}
	if json.Unmarshal(body, &journal) != nil {
		return nil, false
	}
	events := make([]ServeEvent, 0, len(journal.Requests))
	for _, raw := range journal.Requests {
		if event, ok := serveEvent(raw); ok {
			events = append(events, event)
		}
	}
	return events, true
}

func (c *Client) NearMisses(ctx context.Context) ([]NearMiss, bool) {
	body, ok := c.get(ctx, "/__admin/requests/unmatched/near-misses")
	if !ok {
		return nil, false
	}
	return ParseNearMisses(body)
}

// ParseNearMisses reads a /__admin/requests/unmatched/near-misses reply.
func ParseNearMisses(body []byte) ([]NearMiss, bool) {
	var found struct {
		NearMisses []struct {
			Request struct {
				Method     string          `json:"method"`
				URL        string          `json:"url"`
				LoggedDate json.RawMessage `json:"loggedDate"`
			} `json:"request"`
			StubMapping json.RawMessage `json:"stubMapping"`
			MatchResult *struct {
				Distance *float64 `json:"distance"`
			} `json:"matchResult"`
		} `json:"nearMisses"`
	}
	if json.Unmarshal(body, &found) != nil {
		return nil, false
	}
	misses := make([]NearMiss, 0, len(found.NearMisses))
	for _, m := range found.NearMisses {
		distance := 1.0
		if m.MatchResult != nil && m.MatchResult.Distance != nil {
			distance = *m.MatchResult.Distance
		}
		misses = append(misses, NearMiss{Method: m.Request.Method, URL: m.Request.URL,
			LoggedDate: string(m.Request.LoggedDate), Distance: distance, StubMapping: m.StubMapping})
	}
	return misses, true
}

// Mappings returns the stubs, each as WireMock describes it.
func (c *Client) Mappings(ctx context.Context) ([]jsonx.Object, bool) {
	body, ok := c.get(ctx, "/__admin/mappings")
	if !ok {
		return nil, false
	}
	var found struct {
		Mappings []json.RawMessage `json:"mappings"`
	}
	if json.Unmarshal(body, &found) != nil {
		return nil, false
	}
	stubs := make([]jsonx.Object, 0, len(found.Mappings))
	for _, raw := range found.Mappings {
		if stub, ok := jsonx.ParseObject(raw); ok {
			stubs = append(stubs, stub)
		}
	}
	return stubs, true
}

func (c *Client) get(ctx context.Context, path string) ([]byte, bool) {
	if c.service.State() != "running" || c.service.ContainerID() == "" {
		return nil, false
	}
	var text string
	if hostPort := c.service.HostPort(c.port() + "/tcp"); hostPort != "" {
		text = httpGet(ctx, "http://127.0.0.1:"+hostPort+path)
	} else {
		text = execGet(ctx, c.service.ContainerID(), "http://localhost:"+c.port()+path)
	}
	if text == "" || !json.Valid([]byte(text)) {
		return nil, false
	}
	return []byte(text), true
}

// StubLabel names a stub: its name, else its method and URL, else the start of its id.
func StubLabel(stub json.RawMessage) string {
	object, ok := jsonx.ParseObject(stub)
	if !ok || len(object.Keys) == 0 {
		return ""
	}
	if name := object.String("name"); name != "" {
		return name
	}
	request, _ := jsonx.ParseObject(object.Get("request"))
	method := request.String("method")
	if method == "" {
		method = "ANY"
	}
	label := strings.TrimSpace(method + " " + StubURL(request))
	if label != "" {
		return label
	}
	id := object.String("id")
	return id[:min(8, len(id))]
}

// StubURL is the URL a stub's request pattern matches, whichever way it says it.
func StubURL(request jsonx.Object) string {
	for _, key := range []string{"url", "urlPath", "urlPattern", "urlPathPattern", "urlPathTemplate"} {
		if value := request.String(key); value != "" {
			return value
		}
	}
	return ""
}

func serveEvent(raw json.RawMessage) (ServeEvent, bool) {
	object, ok := jsonx.ParseObject(raw)
	if !ok {
		return ServeEvent{}, false
	}
	request, _ := jsonx.ParseObject(object.Get("request"))
	responseRaw := object.Get("response")
	if !jsonx.Truthy(responseRaw) {
		responseRaw = object.Get("responseDefinition")
	}
	response, _ := jsonx.ParseObject(responseRaw)
	matched := jsonx.Truthy(object.Get("stubMapping"))
	if was := object.Get("wasMatched"); was != nil {
		matched = jsonx.Truthy(was)
	}
	event := ServeEvent{ID: object.String("id"), Method: request.String("method"), URL: request.String("url"),
		Matched: matched, Raw: object, loggedRaw: request.Get("loggedDate")}
	var logged float64
	if json.Unmarshal(request.Get("loggedDate"), &logged) == nil && logged > 0 {
		event.At = time.UnixMilli(int64(logged))
	}
	var status float64
	if json.Unmarshal(response.Get("status"), &status) == nil {
		event.Status = int(status)
	}
	if matched {
		event.Stub = StubLabel(object.Get("stubMapping"))
	}
	return event, true
}

// LoggedDate is the request's loggedDate as WireMock wrote it, which near misses carry too.
func (e ServeEvent) LoggedDate() string {
	return string(e.loggedRaw)
}

func httpGet(ctx context.Context, url string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return string(body)
}

func execGet(ctx context.Context, container, url string) string {
	for _, fetch := range [][]string{{"curl", "-s", "-m", "5", url}, {"wget", "-qO-", "-T", "5", url}} {
		if output, ok := services.Command(ctx, "docker", append([]string{"exec", container}, fetch...)...); ok {
			return output
		}
	}
	return ""
}
