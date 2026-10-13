// Package databases reads the queries a database is asked, as the database itself records them, with its own
// client in its container.
//
// MariaDB and MySQL keep a general query log in the mysql.general_log table once log_output=TABLE and general_log
// are set; testdash reads new rows each second with sql_log_off set, so its own reads are not logged. Postgres
// logs its statements to the container's output (see pgLog). MongoDB's
// profiler keeps every operation in each database's system.profile collection at level 2; one mongosh follows
// them all, turning the profiler on for each database as it appears. Both are switched on over a fresh connection
// to the server that serves the tests: the images' entrypoints first run a temporary server for their init
// scripts, which MariaDB keeps off TCP, and a restarted mongod drops the profiler, which mongosh then switches on
// again.
package databases

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/runner"
	"github.com/steven509/testdash/internal/services"
)

var QueryKinds = map[string]bool{"mariadb": true, "mysql": true, "postgres": true, "mongodb": true}

const mongoProfileBytes = 64 * 1024 * 1024 // the default 1MB profile collection rolls over within a test or two

// The session chatter a JDBC pool sends: hidden until asked for.
// sqlNoise is what pools, drivers and the official images' healthcheck.sh send to keep or check a session.
var sqlNoise = regexp.MustCompile(`(?i)^\s*(SET\s|SHOW\s|SELECT\s+@@|SELECT\s+1\s*$|/\*\s*ping|SELECT\s+DATABASE\(\)|` +
	`SELECT\s+VERSION\(\)|select\s+1\s+from\s+information_schema\.ENGINES\s+WHERE\s+engine\s*=\s*'innodb')`)

var sqlCommands = map[string]bool{"Query": true, "Execute": true} // Connect, Quit, Init DB, Prepare and the like are noise too

type Query struct {
	At         time.Time // by the database's clock
	Connection string
	Kind       string // Query, Execute, Connect... for SQL; find, insert, update, aggregate... for MongoDB
	Target     string // MongoDB's database.collection; empty for SQL
	Text       string // the SQL, or MongoDB's command as JSON
	Millis     string // how long it took, as the database wrote it; empty when unknown
	Params     string // a Postgres prepared statement's parameters: $1 = '42', $2 = 'x'
	Detail     jsonx.Object
	seconds    float64 // At as the database wrote it, to ask for what came after it
	chatter    bool
}

// Noise is whether the query is a connection pool's session chatter, worked out once when it was read.
func (q Query) Noise() bool {
	return q.chatter
}

// Chatter is whether a query is session chatter: SQL other than queries and statements, or a pool's SET, SHOW
// and pings. MongoDB's commands never are.
func Chatter(kind, target, text string) bool {
	if target != "" {
		return false
	}
	return !sqlCommands[kind] || sqlNoise.MatchString(text)
}

// QueryLog follows a database's queries and feeds them to onQueries; Error says why it cannot.
type QueryLog interface {
	Follow(ctx context.Context, onQueries func([]Query))
	Error() string
}

func ForService(service *services.Service) QueryLog {
	switch service.Kind {
	case "mariadb", "mysql":
		return &sqlLog{service: service}
	case "postgres":
		return &pgLog{service: service}
	case "mongodb":
		return &mongoLog{service: service}
	}
	return nil
}

type errorHolder struct {
	mu  sync.Mutex
	err string
}

func (e *errorHolder) Error() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *errorHolder) setError(err string) {
	e.mu.Lock()
	e.err = err
	e.mu.Unlock()
}

type sqlLog struct {
	errorHolder
	service    *services.Service
	ownThreads map[string]bool // testdash's earlier reads: their connect and sql_log_off lines
	env        map[string]string
}

const (
	sqlSwitchOn = "SET GLOBAL log_output = 'TABLE'; SET GLOBAL general_log = 1;"
	sqlRead     = "SET SESSION sql_log_off = 1; " +
		"SELECT UNIX_TIMESTAMP(event_time), thread_id, command_type, CONVERT(argument USING utf8mb4) " +
		"FROM mysql.general_log WHERE event_time > FROM_UNIXTIME(%.6f) AND thread_id <> CONNECTION_ID() " +
		"ORDER BY event_time"
)

func (s *sqlLog) Follow(ctx context.Context, onQueries func([]Query)) {
	container, since := "", 0.0
	for ctx.Err() == nil {
		sleep(ctx, time.Second)
		current := s.service.ContainerID()
		if s.service.State() != "running" || current == "" {
			continue
		}
		if container != current {
			container, since, s.ownThreads = current, 1.0, map[string]bool{}
			s.env = services.ContainerEnv(ctx, container)
			if _, ok := s.mysql(ctx, container, sqlSwitchOn); !ok {
				container = "" // not up yet, or the temporary init server
				continue
			}
			s.setError("")
		}
		output, ok := s.mysql(ctx, container, fmt.Sprintf(sqlRead, since))
		if !ok {
			container = "" // a restart turns the log off again
			continue
		}
		queries := s.parse(output)
		if len(queries) == 0 {
			continue
		}
		var theirs []Query
		for _, q := range queries {
			since = max(since, q.seconds)
			if !s.ownThreads[q.Connection] {
				theirs = append(theirs, q)
			}
		}
		onQueries(theirs)
	}
}

func (s *sqlLog) mysql(ctx context.Context, container, sql string) (string, bool) {
	env := s.env
	password, hasPassword := env["MARIADB_ROOT_PASSWORD"], true
	if password == "" {
		password, hasPassword = env["MYSQL_ROOT_PASSWORD"]
	}
	if !hasPassword && env["MARIADB_ALLOW_EMPTY_ROOT_PASSWORD"] == "" && env["MYSQL_ALLOW_EMPTY_PASSWORD"] == "" {
		s.setError("no root password in the container's environment, which reading the query log needs")
		return "", false
	}
	script := `client=$(command -v mariadb || command -v mysql) && ` +
		`exec "$client" --protocol=TCP -h127.0.0.1 -uroot --batch --skip-column-names -e "$1"`
	cmd := exec.CommandContext(ctx, "docker", "exec", "-e", "MYSQL_PWD="+password, container, "sh", "-c", script,
		"sh", sql)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if last := lines[len(lines)-1]; last != "" && !strings.Contains(last, "Can't connect") { // not up yet is not an error
			s.setError(last[:min(200, len(last))])
		}
		return "", false
	}
	return string(output), true
}

func (s *sqlLog) parse(output string) []Query {
	var queries []Query
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			continue
		}
		at, thread, command := fields[0], fields[1], fields[2]
		text := unescape(strings.Join(fields[3:], "\t"))
		if strings.HasPrefix(text, "SET SESSION sql_log_off") {
			s.ownThreads[thread] = true
		}
		seconds, err := strconv.ParseFloat(at, 64)
		if err != nil {
			continue
		}
		queries = append(queries, Query{At: time.Unix(0, int64(seconds*1e9)), Connection: thread, Kind: command,
			Text: text, seconds: seconds, chatter: Chatter(command, "", text)})
	}
	return queries
}

type mongoLog struct {
	errorHolder
	service *services.Service
}

var mongoScript = fmt.Sprintf(`
const known = {};
while (true) {
  for (const {name} of db.adminCommand({listDatabases: 1, nameOnly: true}).databases) {
    if (['admin', 'local', 'config'].includes(name)) continue;
    const d = db.getSiblingDB(name);
    if (!(name in known)) {
      if (d.getProfilingStatus().was !== 2) {
        d.setProfilingLevel(0);
        d.system.profile.drop();
        d.createCollection('system.profile', {capped: true, size: %d});
        d.setProfilingLevel(2);
      }
      known[name] = new Date(0);
    }
    // the profiler records testdash's own reads too
    d.system.profile.find({ts: {$gt: known[name]}, appName: {$ne: '%s'}, ns: {$ne: name + '.system.profile'}})
     .sort({ts: 1}).forEach(entry => {
      known[name] = entry.ts;
      print(EJSON.stringify(entry, {relaxed: true}));
    });
  }
  sleep(500);
}
`, mongoProfileBytes, "testdash")

func (m *mongoLog) Follow(ctx context.Context, onQueries func([]Query)) {
	seen := map[string]bool{} // a restarted mongosh reads each profile from its start again
	container := ""
	for ctx.Err() == nil {
		if current := m.service.ContainerID(); m.service.State() == "running" && current != "" {
			if container != current {
				container, seen = current, map[string]bool{}
			}
			m.followOnce(ctx, container, seen, onQueries)
		}
		sleep(ctx, 2*time.Second)
	}
}

func (m *mongoLog) followOnce(ctx context.Context, container string, seen map[string]bool, onQueries func([]Query)) {
	env := services.ContainerEnv(ctx, container)
	var auth []string
	if user := env["MONGO_INITDB_ROOT_USERNAME"]; user != "" {
		auth = []string{"-u", user, "-p", env["MONGO_INITDB_ROOT_PASSWORD"], "--authenticationDatabase", "admin"}
	}
	args := append(append([]string{"exec", container, "mongosh", "mongodb://127.0.0.1:27017/?appName=testdash",
		"--quiet"}, auth...), "--eval", mongoScript)
	parse := func(line []byte) (Query, bool) {
		query, ok := mongoQuery(line)
		if !ok {
			return Query{}, false
		}
		key := fmt.Sprintf("%d\x00%s\x00%s\x00%s", query.At.UnixNano(), query.Target, query.Kind, query.Text)
		if seen[key] {
			return Query{}, false
		}
		seen[key] = true
		return query, true
	}
	gotAny, stderr, _ := runner.Batched(ctx, exec.CommandContext(ctx, "docker", args...), parse,
		func() bool { return m.service.ContainerID() == container },
		func(batch []Query) {
			m.setError("")
			onQueries(batch)
		})
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	last := lines[len(lines)-1]
	if !gotAny && last != "" && !strings.Contains(last, "ECONNREFUSED") && !strings.Contains(last, "MongoNetworkError") &&
		ctx.Err() == nil {
		if strings.Contains(last, "executable file not found") {
			m.setError("the container has no mongosh (MongoDB 6 and later have it)")
		} else {
			m.setError(last[:min(200, len(last))])
		}
	}
}

func mongoQuery(line []byte) (Query, bool) {
	entry, ok := jsonx.ParseObject(line)
	if !ok {
		return Query{}, false
	}
	var ts struct {
		Date string `json:"$date"`
	}
	if json.Unmarshal(entry.Get("ts"), &ts) != nil {
		return Query{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, ts.Date)
	if err != nil {
		return Query{}, false
	}
	commandRaw := entry.Get("command")
	if !jsonx.Truthy(commandRaw) {
		commandRaw = entry.Get("query")
	}
	command, isObject := jsonx.ParseObject(commandRaw)
	kind := ""
	if isObject && len(command.Keys) > 0 {
		kind = command.Keys[0]
	}
	switch kind {
	case "", "q", "filter", "$db", "lsid": // a legacy query or a write's own document
		kind = entry.String("op")
	}
	switch kind {
	case "getMore", "killCursors", "endSessions":
		return Query{}, false // a cursor's next batch: the query that opened it is already listed
	}
	text := "{}"
	if isObject { // the driver's bookkeeping, the same on every command
		text = jsonx.Dumps(command.Without("lsid", "$clusterTime", "$db", "$readPreference").Marshal())
	} else if commandRaw != nil {
		text = jsonx.Dumps(commandRaw)
	}
	millis := ""
	if raw := entry.Get("millis"); raw != nil && string(raw) != "null" {
		millis = string(raw)
	}
	return Query{At: at, Connection: entry.String("client"), Kind: kind, Target: entry.String("ns"), Text: text,
		Millis: millis, Detail: entry}, true
}

var escaped = regexp.MustCompile(`\\(.)`)

// unescape reads what mysql --batch writes as \n, \t, \0 and \\.
func unescape(text string) string {
	return escaped.ReplaceAllStringFunc(text, func(match string) string {
		switch match[1] {
		case 'n':
			return "\n"
		case 't':
			return "\t"
		case '0':
			return "\x00"
		case '\\':
			return "\\"
		}
		return match
	})
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
