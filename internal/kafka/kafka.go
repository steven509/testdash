// Package kafka reads Kafka through its own command-line tools, run in its container: every message, the topics,
// and the consumer groups' lag.
//
// kafka-console-consumer follows every topic at once (--include), but its formatter cannot print a message's
// topic, so testdash gives it one (formatter/TestdashFormatter.java, embedded in the binary) that prints each
// message as a JSON line. It is compiled once against a kafka-clients jar (from the Gradle cache, or copied out of
// the container), kept in ~/.cache/testdash, and copied into the container, so nothing is installed and no image
// is pulled.
package kafka

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steven509/testdash/internal/catalog"
	"github.com/steven509/testdash/internal/gradle"
	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/runner"
	"github.com/steven509/testdash/internal/services"
)

//go:embed formatter/TestdashFormatter.java
var formatterSource []byte

const (
	formatterClass = "testdash.TestdashFormatter"
	containerJar   = "/tmp/testdash-kafka.jar"
	groupPrefix    = "testdash-"
)

var toolDirs = []string{"", "/opt/kafka/bin/", "/opt/bitnami/kafka/bin/"}

// The tools run beside the broker: a small heap, and no JMX port to clash with the broker's.
var toolEnv = []string{"-e", "KAFKA_HEAP_OPTS=-Xmx128m", "-e", "JMX_PORT=", "-e", "CLASSPATH=" + containerJar}

type Message struct {
	At        time.Time // the record's timestamp
	Topic     string
	Partition int
	Offset    int64
	Key       *string
	Value     *string
	Headers   []Header
}

type Header struct {
	Name  string
	Value *string
}

type GroupLag struct {
	Group     string
	Topic     string
	Partition int
	Offset    *int64 // the group's committed offset
	End       *int64
	Lag       *int64
	Consumer  string // empty when the group has no active member on the partition
}

type Client struct {
	service *services.Service

	mu        sync.Mutex
	err       string // why messages cannot be read, for the view to say
	container string
	tools     *[2]string // directory and suffix of the kafka-* scripts
	bootstrap string
}

func New(service *services.Service) *Client {
	return &Client{service: service, bootstrap: "localhost:9092"}
}

func (c *Client) Error() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) setError(err string) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

// Follow feeds every message on every topic to onMessages, from each topic's start, while the container runs.
func (c *Client) Follow(ctx context.Context, onMessages func([]Message)) {
	group := groupPrefix + randomHex(4)
	for ctx.Err() == nil {
		if c.service.State() == "running" && c.service.ContainerID() != "" && c.setup(ctx) {
			c.consume(ctx, group, onMessages)
		}
		sleep(ctx, 2*time.Second)
	}
}

func (c *Client) Topics(ctx context.Context) []string {
	if !c.setup(ctx) {
		return nil
	}
	output := c.tool(ctx, "kafka-topics", "--bootstrap-server", c.bootstrapServer(), "--list")
	var topics []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "__") {
			topics = append(topics, line)
		}
	}
	return topics
}

// Groups returns each consumer group's position and lag on each partition, testdash's own group left out.
func (c *Client) Groups(ctx context.Context) []GroupLag {
	if !c.setup(ctx) {
		return nil
	}
	output := c.tool(ctx, "kafka-consumer-groups", "--bootstrap-server", c.bootstrapServer(), "--describe",
		"--all-groups")
	var lags []GroupLag
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !isDigits(fields[2]) || strings.HasPrefix(fields[0], groupPrefix) {
			continue
		}
		partition, _ := strconv.Atoi(fields[2])
		consumer := ""
		if len(fields) > 6 && fields[6] != "-" {
			consumer = fields[6]
		}
		lags = append(lags, GroupLag{Group: fields[0], Topic: fields[1], Partition: partition,
			Offset: number(fields[3]), End: number(fields[4]), Lag: number(fields[5]), Consumer: consumer})
	}
	return lags
}

func (c *Client) bootstrapServer() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bootstrap
}

func (c *Client) setup(ctx context.Context) bool {
	container := c.service.ContainerID()
	if container == "" {
		return false
	}
	c.mu.Lock()
	if container == c.container {
		ready := c.tools != nil
		c.mu.Unlock()
		return ready
	}
	c.container, c.tools, c.err = container, nil, ""
	c.mu.Unlock()

	var probe strings.Builder
	for _, dir := range toolDirs {
		for _, suffix := range []string{"", ".sh"} {
			fmt.Fprintf(&probe, `command -v %skafka-console-consumer%s >/dev/null 2>&1 && { echo "%s|%s"; exit 0; };`,
				dir, suffix, dir, suffix)
		}
	}
	found, _ := services.Command(ctx, "docker", "exec", container, "sh", "-c", probe.String()+" exit 1")
	dir, suffix, ok := strings.Cut(strings.TrimSpace(found), "|")
	if !ok {
		c.setError("the container has no kafka-console-consumer")
		return false
	}
	bootstrap := Bootstrap(services.ContainerEnv(ctx, container))
	jar, err := formatterJar(ctx, container)
	if err != nil {
		c.setError(err.Error())
		return false
	}
	if _, ok := services.Command(ctx, "docker", "cp", jar, container+":"+containerJar); !ok {
		c.setError("could not copy testdash's message formatter into the container")
		return false
	}
	c.mu.Lock()
	c.tools, c.bootstrap = &[2]string{dir, suffix}, bootstrap
	c.mu.Unlock()
	return true
}

// consume reads messages until the container stops, or a new run's container takes over from it.
func (c *Client) consume(ctx context.Context, group string, onMessages func([]Message)) {
	container := c.service.ContainerID()
	for _, include := range []string{"--include", "--whitelist"} { // --whitelist before Kafka 2.8
		args := append(append([]string{"exec"}, toolEnv...), container, c.script("kafka-console-consumer"),
			"--bootstrap-server", c.bootstrapServer(), include, ".*", "--from-beginning", "--group", group,
			"--formatter", formatterClass,
			"--consumer-property", "metadata.max.age.ms=2000") // topics created later are read within 2s
		gotAny, stderr, code := runner.Batched(ctx, exec.CommandContext(ctx, "docker", args...), parseMessage,
			func() bool { return c.service.ContainerID() == container }, onMessages)
		if ctx.Err() != nil {
			return
		}
		if gotAny || !strings.Contains(stderr, include) {
			if code != 0 && !gotAny {
				lines := strings.Split(strings.TrimSpace(stderr), "\n")
				last := lines[len(lines)-1]
				if last == "" {
					last = "kafka-console-consumer stopped"
				}
				c.setError(last[:min(200, len(last))])
			}
			return
		}
	}
}

func (c *Client) script(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tools == nil {
		return name
	}
	return c.tools[0] + name + c.tools[1]
}

func (c *Client) tool(ctx context.Context, name string, args ...string) string {
	all := append(append(append([]string{"exec"}, toolEnv...), c.service.ContainerID(), c.script(name)), args...)
	output, _ := services.Command(ctx, "docker", all...)
	return output
}

// formatterJar is the formatter, compiled against a kafka-clients jar from the Gradle cache or else from the
// container.
func formatterJar(ctx context.Context, container string) (string, error) {
	sum := sha1.Sum(formatterSource)
	jar := filepath.Join(catalog.CacheDir(), "kafka-formatter-"+hex.EncodeToString(sum[:])[:12]+".jar")
	if _, err := os.Stat(jar); err == nil {
		return jar, os.Chmod(jar, 0o644) // as earlier versions left it readable by its owner alone
	}
	if err := os.MkdirAll(filepath.Dir(jar), 0o755); err != nil {
		return "", err
	}
	javac, err := gradle.Javac() // the JUnit listener's build finds javac the same way
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "testdash-kafka")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	clients, err := kafkaClientsJar(ctx, container, tmp)
	if err != nil {
		return "", err
	}
	source := filepath.Join(tmp, "src", "TestdashFormatter.java")
	_ = os.MkdirAll(filepath.Dir(source), 0o755)
	if err := os.WriteFile(source, formatterSource, 0o644); err != nil {
		return "", err
	}
	classes := filepath.Join(tmp, "classes")
	output, err := exec.CommandContext(ctx, javac, "--release", "8", "-nowarn", "-implicit:none", "-d", classes,
		"-cp", clients, source).CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		return "", fmt.Errorf("javac failed: %s", text[:min(300, len(text))])
	}
	return jar, gradle.WriteJar(jar, classes, nil)
}

// kafkaClientsJar finds a 3.x kafka-clients jar in the Gradle cache, else copies the broker's: MessageFormatter
// moved to kafka-clients in Kafka 2.7.
func kafkaClientsJar(ctx context.Context, container, tmp string) (string, error) {
	base := filepath.Join(gradle.GradleCache(), "org.apache.kafka", "kafka-clients")
	dirs, _ := os.ReadDir(base)
	var versions []string
	for _, d := range dirs {
		major, _, _ := strings.Cut(d.Name(), ".")
		if n, err := strconv.Atoi(major); err == nil && n >= 3 {
			versions = append(versions, d.Name())
		}
	}
	sort.Slice(versions, func(i, j int) bool { return gradle.CompareVersions(versions[i], versions[j]) > 0 })
	for _, version := range versions {
		jars, _ := filepath.Glob(filepath.Join(base, version, "*", "kafka-clients-"+version+".jar"))
		if len(jars) > 0 {
			return jars[0], nil
		}
	}
	found, _ := services.Command(ctx, "docker", "exec", container, "sh", "-c",
		"ls /usr/share/java/kafka/kafka-clients-*.jar /opt/kafka/libs/kafka-clients-*.jar "+
			"/opt/bitnami/kafka/libs/kafka-clients-*.jar 2>/dev/null | head -1")
	found = strings.TrimSpace(found)
	if found == "" {
		return "", errors.New("no kafka-clients jar in the Gradle cache or the container")
	}
	target := filepath.Join(tmp, "kafka-clients.jar")
	if _, ok := services.Command(ctx, "docker", "cp", container+":"+found, target); !ok {
		return "", fmt.Errorf("could not copy %s out of the container", found)
	}
	return target, nil
}

// Bootstrap is a plaintext listener the broker advertises with an address it can reach itself: the inter-broker
// listener when it is plaintext (Testcontainers advertises its PLAINTEXT one on the host's port), else the first.
func Bootstrap(env map[string]string) string {
	get := func(name string) string {
		if value := env["KAFKA_"+name]; value != "" {
			return value
		}
		return env["KAFKA_CFG_"+name]
	}
	protocols := map[string]string{}
	for _, item := range strings.Split(get("LISTENER_SECURITY_PROTOCOL_MAP"), ",") {
		if name, protocol, ok := strings.Cut(item, ":"); ok {
			protocols[name] = protocol
		}
	}
	controllers := map[string]bool{}
	for _, name := range strings.Split(get("CONTROLLER_LISTENER_NAMES"), ",") {
		controllers[name] = true
	}
	type listener struct{ name, port string }
	var listeners []listener
	for _, item := range strings.Split(get("LISTENERS"), ",") {
		name, address, _ := strings.Cut(strings.TrimSpace(item), "://")
		port := address[strings.LastIndex(address, ":")+1:]
		protocol, ok := protocols[name]
		if !ok {
			protocol = name
		}
		if isDigits(port) && !controllers[name] && protocol == "PLAINTEXT" {
			listeners = append(listeners, listener{name, port})
		}
	}
	interBroker := get("INTER_BROKER_LISTENER_NAME")
	sort.SliceStable(listeners, func(i, j int) bool {
		return listeners[i].name == interBroker && listeners[j].name != interBroker
	})
	if len(listeners) == 0 {
		return "localhost:9092"
	}
	return "localhost:" + listeners[0].port
}

func parseMessage(line []byte) (Message, bool) {
	var raw struct {
		Timestamp *float64 `json:"timestamp"`
		Topic     *string  `json:"topic"`
		Partition *int     `json:"partition"`
		Offset    *int64   `json:"offset"`
		Key       *string  `json:"key"`
		Value     *string  `json:"value"`
		Headers   json.RawMessage
	}
	if json.Unmarshal(line, &raw) != nil || raw.Timestamp == nil || raw.Topic == nil || raw.Partition == nil ||
		raw.Offset == nil {
		return Message{}, false // the consumer's own warnings
	}
	message := Message{At: time.UnixMilli(int64(*raw.Timestamp)), Topic: *raw.Topic, Partition: *raw.Partition,
		Offset: *raw.Offset, Key: raw.Key, Value: raw.Value}
	if keys, err := headerKeys(raw.Headers); err == nil {
		var values map[string]*string
		_ = json.Unmarshal(raw.Headers, &values)
		for _, name := range keys {
			message.Headers = append(message.Headers, Header{Name: name, Value: values[name]})
		}
	}
	return message, true
}

func headerKeys(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("no headers")
	}
	return jsonx.ObjectKeys(raw)
}

func number(text string) *int64 {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
