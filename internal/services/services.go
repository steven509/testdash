// Package services finds the containers a test run starts through the Docker CLI, with no configuration.
//
// A container is named after its Compose service (Testcontainers' compose projects get random names, the
// services do not), and what it is comes from its image: WireMock, MongoDB, MariaDB and the like by name,
// Testcontainers' own helpers hidden, anything else is the application. A container belongs to the run when it
// was created after the run started and either Testcontainers labelled it or its Compose file lives in this repo,
// so containers from other runs and other projects on the same machine stay out. Logs are kept after a container
// is removed, and a container restarted mid-run is followed again from where it stopped.
package services

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steven509/testdash/internal/logformat"
)

var kinds = [][2]string{ // image name keyword -> kind
	{"wiremock", "wiremock"}, {"mockserver", "mockserver"}, {"mongo", "mongodb"}, {"mariadb", "mariadb"},
	{"mysql", "mysql"}, {"postgres", "postgres"}, {"kafka", "kafka"}, {"zookeeper", "zookeeper"},
	{"redis", "redis"}, {"rabbitmq", "rabbitmq"}, {"localstack", "localstack"}, {"elasticsearch", "elasticsearch"},
	{"opensearch", "opensearch"}, {"minio", "minio"}, {"keycloak", "keycloak"}, {"nginx", "nginx"},
}

var infra = []string{"ryuk", "socat"} // Testcontainers' resource reaper and port ambassador

var identityLabels = []string{"org.testcontainers.sessionId", "com.docker.compose.project"}

const (
	pollInterval = time.Second
	clockSlack   = 15 * time.Second // Docker's clock (a VM on macOS) can drift from the host's
	claimWait    = 15 * time.Second // how long a container waits for the run's tests to claim it
)

// Service is a container the run started (or a log from .testdash.json). Its key and kind never change; the
// rest changes as the watcher finds the container again, so it is read through methods.
type Service struct {
	Key  string
	Kind string

	mu           sync.RWMutex
	image        string
	containerID  string
	state        string // running | stopped | previous (from an earlier run)
	exposedPorts []string
	hostPorts    map[string]string
	command      []string
}

func NewService(key, kind, image string) *Service {
	return &Service{Key: key, Kind: kind, image: image, state: "running"}
}

func (s *Service) IsApp() bool {
	return s.Kind == "app"
}

func (s *Service) State() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Service) SetState(state string) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}

func (s *Service) ContainerID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.containerID
}

func (s *Service) Image() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.image
}

func (s *Service) ExposedPorts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.exposedPorts
}

func (s *Service) HostPort(port string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostPorts[port]
}

func (s *Service) Command() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.command
}

func (s *Service) found(image, containerID string, ports []string, hostPorts map[string]string, command []string) {
	s.mu.Lock()
	s.image, s.containerID, s.exposedPorts, s.hostPorts, s.command = image, containerID, ports, hostPorts, command
	s.state = "running"
	s.mu.Unlock()
}

// The watcher's messages to the UI.
type (
	Added   struct{ Service *Service }
	Changed struct{ Service *Service }
	// Lines are new log lines; Gen is the run they belong to, so lines from before a new run are dropped.
	Lines struct {
		Service *Service
		Gen     int
		Lines   []*logformat.Line
	}
)

// ClaimState is what the run's tests said about their containers.
type ClaimState struct {
	Listening      bool  // the run's test JVMs report progress, and so their containers, to testdash
	Testcontainers *bool // whether the tests use Testcontainers, nil until they say
	Labels         map[string]bool
	RunActive      bool
}

// Claims shares the run's ClaimState between the UI, which reads the progress files, and the watcher.
type Claims struct {
	mu    sync.Mutex
	state ClaimState
}

func (c *Claims) Set(state ClaimState) {
	labels := make(map[string]bool, len(state.Labels))
	for label := range state.Labels {
		labels[label] = true
	}
	state.Labels = labels
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
}

func (c *Claims) get() ClaimState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

type follower struct {
	finished atomic.Bool
}

// Watcher finds the containers a run starts and follows their logs. When the run's tests use Testcontainers,
// they name the labels of their containers (see Claims), so only those are taken, even while another run starts
// containers from the same repo. Otherwise a container is the run's when it was created after the run started
// and Testcontainers labelled it, or its Compose file is in the repo.
type Watcher struct {
	ctx        context.Context
	send       func(any)
	claims     *Claims
	scopeDirs  []string
	includeAll bool

	mu          sync.Mutex
	since       time.Time
	gen         int
	runCtx      context.Context
	runCancel   context.CancelFunc
	services    map[string]*Service
	byContainer map[string]*Service
	followers   map[string]*follower
	lastSeen    map[string]time.Time // container -> last log timestamp, to resume after a restart
	ignored     map[string]bool
	retired     map[string]bool // earlier runs' containers, never a later run's however recent
	firstSeen   map[string]time.Time
	pending     map[string][]*logformat.Line
	pendingKeys []string
}

func NewWatcher(ctx context.Context, send func(any), since time.Time, scopeDirs []string, includeAll bool,
	claims *Claims) *Watcher {
	w := &Watcher{ctx: ctx, send: send, claims: claims, since: since, includeAll: includeAll,
		services: map[string]*Service{}, byContainer: map[string]*Service{}, followers: map[string]*follower{},
		lastSeen: map[string]time.Time{}, ignored: map[string]bool{}, retired: map[string]bool{},
		firstSeen: map[string]time.Time{}, pending: map[string][]*logformat.Line{}}
	w.runCtx, w.runCancel = context.WithCancel(ctx)
	seen := map[string]bool{}
	for _, dir := range scopeDirs {
		for _, d := range []string{dir, resolve(dir)} {
			if d != "" && !seen[d] {
				seen[d] = true
				w.scopeDirs = append(w.scopeDirs, d)
			}
		}
	}
	return w
}

func Available() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// NewRun starts a run's scope: earlier containers stop being followed, their services wait for new ones, and
// lines already on their way are from an earlier generation. It returns the new generation.
func (w *Watcher) NewRun(startedAt time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.since = startedAt
	w.runCancel()
	w.runCtx, w.runCancel = context.WithCancel(w.ctx)
	w.followers = map[string]*follower{}
	for id := range w.byContainer {
		w.retired[id] = true
	}
	w.byContainer = map[string]*Service{}
	w.ignored = map[string]bool{}
	w.firstSeen = map[string]time.Time{}
	w.pending, w.pendingKeys = map[string][]*logformat.Line{}, nil
	for _, s := range w.services {
		s.mu.Lock()
		s.containerID, s.state = "", "previous"
		s.mu.Unlock()
	}
	w.gen++
	return w.gen
}

func (w *Watcher) Run() {
	for w.ctx.Err() == nil {
		w.scan()
		w.flush()
		sleep(w.ctx, pollInterval/4)
		w.flush()
		sleep(w.ctx, pollInterval*3/4)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

type belonging int

const (
	notOurs belonging = iota
	ours
	undecided // asked again on the next scan
)

type details struct {
	Created string `json:"Created"`
	Config  struct {
		Image        string            `json:"Image"`
		Labels       map[string]string `json:"Labels"`
		ExposedPorts map[string]any    `json:"ExposedPorts"`
		Cmd          []string          `json:"Cmd"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

func (w *Watcher) scan() {
	output, ok := Command(w.ctx, "docker", "ps", "--no-trunc", "--format", "{{.ID}}")
	if !ok {
		return
	}
	running := strings.Fields(output)
	isRunning := map[string]bool{}
	var events []any
	for _, id := range running {
		isRunning[id] = true
		w.mu.Lock()
		ignored, service := w.ignored[id], w.byContainer[id]
		w.mu.Unlock()
		if ignored {
			continue
		}
		if service == nil {
			found, ok := inspect(w.ctx, id)
			w.mu.Lock()
			verdict := notOurs
			if ok {
				verdict = w.belongs(id, found)
			}
			if verdict == ours {
				var event any
				service, event = w.serviceFor(id, found)
				if event != nil {
					events = append(events, event)
				}
			}
			if verdict == notOurs || (verdict == ours && service == nil) {
				w.ignored[id] = true
			}
			w.mu.Unlock()
			if service == nil {
				continue
			}
		}
		w.mu.Lock()
		if f := w.followers[id]; f == nil || f.finished.Load() {
			if service.State() != "running" {
				service.SetState("running")
				events = append(events, Changed{service})
			}
			w.startFollower(service, id)
		}
		w.mu.Unlock()
	}
	w.mu.Lock()
	for id, service := range w.byContainer {
		if !isRunning[id] && service.State() == "running" && service.ContainerID() == id {
			service.SetState("stopped")
			events = append(events, Changed{service})
		}
	}
	w.mu.Unlock()
	for _, event := range events {
		w.send(event)
	}
}

// belongs says whether a container is the run's; the caller holds the lock.
func (w *Watcher) belongs(id string, found details) belonging {
	if w.includeAll {
		return ours
	}
	created := parseTime(found.Created)
	if w.retired[id] || created.IsZero() || created.Before(w.since.Add(-clockSlack)) {
		return notOurs
	}
	labels := found.Config.Labels
	claims := w.claims.get()
	var identity []string
	for _, key := range identityLabels {
		if value, ok := labels[key]; ok {
			identity = append(identity, key+"="+value)
		}
	}
	for _, label := range identity {
		if claims.Labels[label] {
			return ours
		}
	}
	usesTestcontainers := claims.Testcontainers
	if !claims.Listening {
		no := false
		usesTestcontainers = &no
	}
	if len(identity) > 0 && (usesTestcontainers == nil || *usesTestcontainers) { // unclaimed so far
		first, seen := w.firstSeen[id]
		if !seen {
			first = time.Now()
			w.firstSeen[id] = first
		}
		waited := time.Since(first)
		if claims.RunActive && (usesTestcontainers == nil || waited < claimWait) {
			return undecided
		}
		return notOurs
	}
	for label := range labels {
		if strings.HasPrefix(label, "org.testcontainers") {
			return ours
		}
	}
	compose := labels["com.docker.compose.project.config_files"] + " " + labels["com.docker.compose.project.working_dir"]
	for _, dir := range w.scopeDirs {
		if strings.Contains(compose, dir) {
			return ours
		}
	}
	return notOurs
}

// serviceFor names a container's service and records it; the caller holds the lock.
func (w *Watcher) serviceFor(id string, found details) (*Service, any) {
	image := found.Config.Image
	labels := found.Config.Labels
	kind := Classify(image)
	if kind == "" {
		return nil, nil
	}
	base := labels["com.docker.compose.service"]
	if base == "" {
		base = shortImage(image)
	}
	key, number := base, 1
	service := w.services[key]
	for service != nil && service.State() == "running" && service.ContainerID() != "" && service.ContainerID() != id {
		number++ // two at once, e.g. the same image started twice
		key = fmt.Sprintf("%s %d", base, number)
		service = w.services[key]
	}
	var ports []string
	for port := range found.Config.ExposedPorts {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	hostPorts := map[string]string{}
	for port, bindings := range found.NetworkSettings.Ports {
		if len(bindings) > 0 {
			hostPorts[port] = bindings[0].HostPort
		}
	}
	var event any
	if service == nil {
		service = NewService(key, kind, image)
		w.services[key] = service
		event = Added{service}
	} else {
		event = Changed{service}
	}
	service.found(image, id, ports, hostPorts, found.Config.Cmd)
	w.byContainer[id] = service
	return service, event
}

// startFollower follows a container's log until it stops or a new run starts; the caller holds the lock.
func (w *Watcher) startFollower(service *Service, id string) {
	f := &follower{}
	w.followers[id] = f
	since := w.lastSeen[id]
	go w.follow(w.runCtx, f, service, id, since)
}

func (w *Watcher) follow(ctx context.Context, f *follower, service *Service, id string, since time.Time) {
	defer f.finished.Store(true)
	args := []string{"logs", "--follow", "--timestamps"}
	if !since.IsZero() {
		args = append(args, "--since", strconv.FormatFloat(float64(since.UnixNano())/1e9, 'f', 6, 64))
	}
	cmd := exec.CommandContext(ctx, "docker", append(args, id)...)
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		return
	}
	go func() {
		_ = cmd.Wait()
		writer.Close()
	}()
	scanner := bufio.NewReaderSize(reader, 64*1024)
	for {
		raw, err := scanner.ReadString('\n')
		if raw != "" && !strings.HasPrefix(raw, "Error response from daemon:") { // docker's own, e.g. a container being removed
			line := logLine(strings.TrimRight(raw, "\r\n"))
			w.mu.Lock()
			if ctx.Err() == nil {
				if !line.At.IsZero() {
					w.lastSeen[id] = line.At
				}
				if _, ok := w.pending[service.Key]; !ok {
					w.pendingKeys = append(w.pendingKeys, service.Key)
				}
				w.pending[service.Key] = append(w.pending[service.Key], line)
			}
			w.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	if ctx.Err() == nil {
		w.flush() // the container stopped, so its last lines go out now rather than at the next scan
	}
}

func (w *Watcher) flush() {
	w.mu.Lock()
	pending, keys, gen := w.pending, w.pendingKeys, w.gen
	w.pending, w.pendingKeys = map[string][]*logformat.Line{}, nil
	var batches []Lines
	for _, key := range keys {
		if service := w.services[key]; service != nil {
			batches = append(batches, Lines{Service: service, Gen: gen, Lines: pending[key]})
		}
	}
	w.mu.Unlock()
	for _, batch := range batches {
		w.send(batch)
	}
}

// Classify is the kind of service an image runs, "" for Testcontainers' own helpers, "app" for anything unknown.
func Classify(image string) string {
	short := shortImage(image)
	for _, helper := range infra {
		if strings.Contains(short, helper) {
			return ""
		}
	}
	for _, k := range kinds {
		if strings.Contains(short, k[0]) {
			return k[1]
		}
	}
	return "app"
}

// PortHolder says what holds a host port: a container, with where its Compose project lives, or else a process.
func PortHolder(ctx context.Context, port string) string {
	output, _ := Command(ctx, "docker", "ps", "--filter", "publish="+port, "--format",
		`{{.Names}}	{{.Label "com.docker.compose.project.working_dir"}}	{{.RunningFor}}`)
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		fields := append(strings.Split(line, "\t"), "", "")
		name, workdir, runningFor := fields[0], fields[1], fields[2]
		where := ""
		if workdir != "" {
			home, _ := os.UserHomeDir()
			if home != "" {
				workdir = strings.ReplaceAll(workdir, home, "~")
			}
			where = ", Compose project in " + workdir
		}
		return fmt.Sprintf("container %s (up %s%s)", name, strings.TrimSuffix(runningFor, " ago"), where)
	}
	output, _ = Command(ctx, "lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fcp")
	fields := map[byte]string{}
	for _, line := range strings.Split(output, "\n") {
		if line != "" {
			fields[line[0]] = line[1:]
		}
	}
	if fields['c'] != "" && fields['p'] != "" {
		return fmt.Sprintf("process %s (pid %s)", fields['c'], fields['p'])
	}
	return ""
}

// Command runs a command and returns its output, and whether it succeeded; its stderr is dropped.
func Command(ctx context.Context, name string, args ...string) (string, bool) {
	output, err := exec.CommandContext(ctx, name, args...).Output()
	return string(output), err == nil
}

func inspect(ctx context.Context, id string) (details, bool) {
	output, ok := Command(ctx, "docker", "inspect", id)
	var found []details
	if !ok || json.Unmarshal([]byte(output), &found) != nil || len(found) == 0 {
		return details{}, false
	}
	return found[0], true
}

// ContainerEnv is a container's environment variables.
func ContainerEnv(ctx context.Context, container string) map[string]string {
	output, _ := Command(ctx, "docker", "inspect", "--format", "{{json .Config.Env}}", container)
	var items []string
	env := map[string]string{}
	if json.Unmarshal([]byte(output), &items) != nil {
		return env
	}
	for _, item := range items {
		if key, value, ok := strings.Cut(item, "="); ok {
			env[key] = value
		}
	}
	return env
}

func shortImage(image string) string {
	image, _, _ = strings.Cut(image, "@")
	image = image[strings.LastIndex(image, "/")+1:]
	image, _, _ = strings.Cut(image, ":")
	return strings.ToLower(image)
}

func logLine(raw string) *logformat.Line {
	stamp, text, _ := strings.Cut(raw, " ")
	if at := parseTime(stamp); !at.IsZero() {
		return logformat.NewLine(at, text)
	}
	return logformat.NewLine(time.Time{}, raw)
}

// parseTime reads Docker's RFC 3339 timestamps, zero when stamp is not one.
func parseTime(stamp string) time.Time {
	if len(stamp) < 19 || stamp[4] != '-' || stamp[10] != 'T' {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}
	}
	return at
}

func resolve(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
