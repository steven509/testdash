package databases

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/steven509/testdash/internal/services"
)

// pgLog follows a Postgres server's statements in its log. testdash has the server log every statement with how
// long it took (log_min_duration_statement = 0), after the time and the backend's process id, and reads them from
// the container's output. ALTER SYSTEM saves settings in the data directory, so once the server has them, they
// are taken out of it again: a database kept in a volume does not go on logging after the run.
type pgLog struct {
	errorHolder
	service *services.Service
}

// Each statement goes on its own, as ALTER SYSTEM cannot run in the transaction several in one would share.
var (
	pgSwitchOn = []string{"ALTER SYSTEM SET log_min_duration_statement = 0",
		"ALTER SYSTEM SET log_line_prefix = '%n [%p] '", "SELECT pg_reload_conf()"}
	pgUnsave = []string{"ALTER SYSTEM RESET log_min_duration_statement", "ALTER SYSTEM RESET log_line_prefix"}
)

var (
	pgEntry  = regexp.MustCompile(`^(\d+\.\d+) \[(\d+)\] (\w+):  (.*)$`)
	pgLogged = regexp.MustCompile(`^duration: ([\d.]+) ms  (statement|execute [^:]*): ?(.*)$`)
)

func (p *pgLog) Follow(ctx context.Context, onQueries func([]Query)) {
	seen := map[string]bool{} // the log is read again from a little before each switch-on
	for ctx.Err() == nil {
		container := p.service.ContainerID()
		if p.service.State() != "running" || container == "" {
			sleep(ctx, time.Second)
			continue
		}
		since := time.Now().Add(-clockSlack)
		if !p.switchOn(ctx, container) {
			sleep(ctx, time.Second)
			continue
		}
		p.follow(ctx, container, since, seen, onQueries)
		sleep(ctx, time.Second)
	}
}

const clockSlack = 15 * time.Second // Docker's clock (a VM on macOS) can drift from the host's

// switchOn has the server log its statements, waits until it does, and takes the settings out of its data
// directory again.
func (p *pgLog) switchOn(ctx context.Context, container string) bool {
	env := services.ContainerEnv(ctx, container)
	if _, ok := p.psql(ctx, container, env, pgSwitchOn...); !ok {
		return false
	}
	for range 20 {
		value, ok := p.psql(ctx, container, env, // the server's value: this session sets its own
			"SELECT reset_val FROM pg_settings WHERE name = 'log_min_duration_statement'")
		if ok && value == "0" {
			_, ok = p.psql(ctx, container, env, pgUnsave...)
			p.setError("")
			return ok
		}
		sleep(ctx, 100*time.Millisecond)
	}
	return false
}

// psql runs statements in the container with its own client, as the image's user, in a session that does not log.
func (p *pgLog) psql(ctx context.Context, container string, env map[string]string, statements ...string) (string, bool) {
	user := env["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	database := env["POSTGRES_DB"]
	if database == "" {
		database = user
	}
	args := []string{"exec", "-e", "PGPASSWORD=" + env["POSTGRES_PASSWORD"], container, "psql", "-X", "-q", "-t", "-A",
		"-v", "ON_ERROR_STOP=1", "-U", user, "-d", database, "-c", "SET log_min_duration_statement = -1"}
	for _, statement := range statements {
		args = append(args, "-c", statement)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		last := lines[len(lines)-1]
		switch {
		case strings.Contains(last, "executable file not found"):
			p.setError("the container has no psql")
		case last != "" && !strings.Contains(last, "could not connect") && !strings.Contains(last, "starting up") &&
			!strings.Contains(last, "shutting down"): // not up yet is not an error
			p.setError(last[:min(200, len(last))])
		}
		return "", false
	}
	return strings.TrimSpace(string(output)), true
}

// follow reads the statements the container logs from since until its log ends, as it does when it stops.
func (p *pgLog) follow(ctx context.Context, container string, since time.Time, seen map[string]bool,
	onQueries func([]Query)) {
	reader, writer := io.Pipe()
	cmd := exec.CommandContext(ctx, "docker", "logs", "--follow", "--since",
		strconv.FormatInt(since.Unix(), 10), container)
	cmd.Stdout, cmd.Stderr = writer, writer // Postgres logs to stderr
	if cmd.Start() != nil {
		return
	}
	go func() {
		_ = cmd.Wait()
		writer.Close()
	}()
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	var parser pgParser
	emit := func() {
		var fresh []Query
		for _, q := range parser.take() {
			key := fmt.Sprintf("%d\x00%s\x00%s", q.At.UnixNano(), q.Connection, q.Text)
			if !seen[key] {
				seen[key] = true
				fresh = append(fresh, q)
			}
		}
		if len(fresh) > 0 {
			onQueries(fresh)
		}
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	quiet := false
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				parser.end()
				emit()
				return
			}
			parser.line(line)
			quiet = false
		case <-ticker.C:
			if quiet {
				parser.end() // a statement's lines come together, so a quiet moment ends the last one
			}
			quiet = true
			emit()
		case <-ctx.Done():
			return
		}
	}
}

// pgParser puts a statement back together from its log lines: the statement, the lines a multi-line statement
// goes on over (each begun with a tab), and its parameters on a DETAIL line.
type pgParser struct {
	done    []Query
	current *Query
	inText  bool // continuation lines go on the statement, rather than its parameters
}

func (p *pgParser) line(line string) {
	if strings.HasPrefix(line, "\t") {
		if p.current != nil && p.inText {
			p.current.Text += "\n" + line[1:]
		} else if p.current != nil {
			p.current.Params += "\n" + line[1:]
		}
		return
	}
	match := pgEntry.FindStringSubmatch(line)
	if match == nil {
		p.end()
		return
	}
	seconds, pid, severity, message := match[1], match[2], match[3], match[4]
	if severity == "DETAIL" && p.current != nil && p.current.Connection == pid &&
		strings.HasPrefix(message, "parameters: ") {
		p.current.Params, p.inText = strings.TrimPrefix(message, "parameters: "), false
		return
	}
	p.end()
	logged := pgLogged.FindStringSubmatch(message)
	if severity != "LOG" || logged == nil {
		return
	}
	whole, fraction, _ := strings.Cut(seconds, ".")
	unix, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return
	}
	nanos, _ := strconv.ParseInt((fraction + "000000000")[:9], 10, 64)
	kind := "Query"
	if strings.HasPrefix(logged[2], "execute") {
		kind = "Execute"
	}
	p.current = &Query{At: time.Unix(unix, nanos), Connection: pid, Kind: kind, Text: logged[3], Millis: logged[1]}
	p.inText = true
}

// end closes the statement being read.
func (p *pgParser) end() {
	if p.current != nil {
		p.current.chatter = Chatter(p.current.Kind, "", p.current.Text)
		p.done = append(p.done, *p.current)
		p.current = nil
	}
}

func (p *pgParser) take() []Query {
	done := p.done
	p.done = nil
	return done
}
