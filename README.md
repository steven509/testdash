# testdash

Pick tests, run them, and follow everything the run touches, in one terminal app. It works in any Gradle
repo with no configuration: it lists the build's tests itself, and finds the application, WireMock, databases
and brokers the tests start through Docker.

## The screens

**Picker.** The tests of a Gradle test task, by class, including `@Nested` classes and parameterised tests.
Type to filter them, select some (or none, to run them all), press Enter to reach the Run button and Enter
again to run them. Results from the last run show next to each test, and `ctrl+f` selects the ones that failed.

**Run screen.** A sidebar of views down the left: the overview, the tests, the application (★) and every other
service the run started, and the Gradle output. On the right of each: a green ● for a running container, a dim
■ for a stopped one, ⚠ and the number of warnings and errors it logged, ▶ while Gradle runs, and the tests'
counts. Hovering over one says the same in words. The overview shows the tests (progress, the test running,
failures with their message), WireMock (requests, the unmatched ones grouped by endpoint, the most requested
endpoints), Kafka (the busiest topics and the latest messages) and a large log with a tab per service, the
application's first.

**Full views.** The tests with each one's duration and failure, plus the WireMock requests, Kafka messages and
application warnings from while it ran; `l`, `w`, `k` and `d` open the application's log, WireMock's requests,
Kafka's messages or the databases' queries from just that time, and Enter runs the test (or class) again. A log with filter, follow and
errors-only. WireMock with every request and whether a stub matched, the unmatched requests with their closest
stub, the stubs, and its log. Kafka with every message on every topic (Enter on a topic shows only its), the
topics, each consumer group's lag, and its log. MariaDB, MySQL and MongoDB with every query they were asked (a
connection pool's `SET`s and `SHOW`s hidden until `n`), and their log. Every view but
the overview has a status bar of two lines: the test running (its method, its display name when it has one, and
the classes it is nested in, `Outer › Inner`), then how many passed, failed, were skipped and are left.

**Logs** are simplified: each line shows its level and message, without the timestamp, process id, thread and
logger, and stack traces sit indented under the line that logged them. MDC values a pattern puts before the
message (`[reference=…]`) stay, dimmed. `v` shows lines as they were logged and `t` adds the time Docker
received each one; both apply to every log. testdash reads Spring Boot's console format (2.x, 3.x, and 3.2+
with the application name and correlation id), Logback and Log4j 2's usual patterns, JSON logs (Spring Boot's
structured Logstash, ECS and GELF output, and MongoDB's), Kafka, MariaDB/MySQL, Postgres and Redis, and any
line that starts with a timestamp. Lines in other formats are shown as they are.

When the run cannot start its containers because a port is taken, as when another run of the same Compose
file is up, the overview and status bar say which container (or process) holds the port.

Esc goes back: from a full view to the overview, from the overview to the picker. It first clears a filter or a
test's time. A run carries on while you look around.

| Where | Keys |
|---|---|
| Picker | type to filter (case, spaces and punctuation ignored; Esc clears) · ↑/↓ move · ←/→ collapse/expand · space select (under a filter, only matching tests) · `ctrl+a` all/none · `ctrl+f` last run's failures · `ctrl+t` task (choosing one, or Esc, goes back to the tests) · Enter → Run button → run · while a run goes on, Esc watches it and `ctrl+x` stops it |
| Run screen | ↑/↓ in the sidebar or `1`–`9` switch views · Tab moves between sidebar and panels · Enter opens a panel · `[` `]` switch the overview's log · `r` run again · `F` run the failures · `x` stop (twice kills) · `p` or Esc (on the overview) picker · `:` jump · `q` quit |
| Tests view | `/` find · `s` show all, failed or not passed · Enter run this test or class again · `l` its log · `w` its WireMock requests · `k` its Kafka messages · `d` its database queries |
| Log views | `/` filter (regex, on the whole line) · `e` errors only · `f` follow · `w` wrap · `v` simplified or full lines · `t` time · Esc back |
| WireMock | ←/→ Requests, Unmatched, Stubs, Log · ↑/↓ a row, details on the right · `u` unmatched only |
| Kafka | ←/→ Messages, Topics, Groups, Log · ↑/↓ a row, details on the right · Enter on a topic shows its messages |
| Databases | ←/→ Queries, Log · ↑/↓ a query, all of it on the right · `n` session chatter |
| Anywhere | `ctrl+p` jump to anything · `ctrl+q` quit |

## Use

Needs [uv](https://docs.astral.sh/uv/), which installs testdash's one dependency,
[Textual](https://textual.textualize.io), on first use, and Docker for the service views. Gradle builds also need
a JDK (`JAVA_HOME` or `javac` on PATH) to compile the JUnit listener once. Put `testdash` on your PATH with a
link to the script, which finds its other files through the link:

```sh
ln -s "$PWD/testdash" ~/.local/bin/testdash
```

```sh
testdash                             # in a Gradle repo: pick from its test tasks
testdash --task :app:blackBoxTest    # start on a task
testdash --info                      # arguments testdash does not know are passed to Gradle
testdash -- npm test                 # any other command: no test list, the service views still work
```

`--show-test-output` keeps test stdout in the Output view (by default the service views show it), and
`--all-containers` follows every container on the machine rather than only the run's. With a
[Nerd Font](https://www.nerdfonts.com) in the terminal, `TESTDASH_NERD_FONT=1` draws the tree's carets as
chevrons; other fonts have no centred down caret, so they get ▸ and ▾.

A `.testdash.json` at the repo root (found from the current directory up to the git root) is optional:

```json
{
  "task": ":app:blackBoxTest",
  "logs": [{ "name": "app.log", "file": "build/app.log" }]
}
```

`command` (a list or a string) replaces the Gradle task, `showTestOutput` is the flag above, and `logs` adds
views for a `file` or a `command`'s output.

## How it works

**The test list** comes from JUnit Platform's discovery API, so it is exactly what a run would execute. A
Gradle init script (`junit/testdash.init.gradle`) adds a `testdashDiscover` task that compiles each Test task's
tests and discovers them (`junit/Discover.java`) without running them or the Test task's other dependencies. The
result is cached, so the picker opens at once and refreshes in the background. A selection becomes `--tests`
filters; a whole class also selects its `@Nested` classes.

**Progress** comes from a JUnit listener (`junit/TestdashListener.java`) the same init script puts on every Test
task, without changing the build. Both are compiled once against JUnit jars from your Gradle cache, using only
JUnit Platform 1.0 API, and kept in `~/.cache/testdash`.

**Services** are the containers the run started. When the tests use Testcontainers, the listener reports the
labels Testcontainers will clean up by, its session id and each Compose project's name (registered before the
project starts), and only containers with those are taken, so another run's stay out even when it uses the same
Compose file. Otherwise they are the containers created after the run started that Testcontainers labelled, or
whose Compose file lives in the repo. Each is named after its Compose service; its
image says what it is (WireMock, MongoDB, MariaDB, MySQL, Postgres, Kafka, Redis and more), anything else is the
application, and Testcontainers' own helpers are hidden. Logs are kept after the containers are removed.

**WireMock** is read through its admin API (`/__admin/requests`, `near-misses`, `mappings`) with `docker exec`
and the container's own curl, since Testcontainers rarely publishes its port.

**Queries** are what the databases record themselves. MariaDB and MySQL get `log_output=TABLE` and
`general_log=1`, and testdash reads new rows of `mysql.general_log` each second (with `sql_log_off`, so its reads
are not logged), as root, with the password from the container's environment. MongoDB gets its profiler at level
2 on every database as it appears, with a 64MB `system.profile`, and one `mongosh` prints new entries. Both are
switched on over a connection to the server that serves the tests, not the temporary one the images run their
init scripts on, so queries made before that (migrations at startup, say) are missed. MongoDB needs `mongosh`,
which its images have from 6.0.

**Kafka** is read with its own tools in its container. One `kafka-console-consumer` follows every topic with a
formatter that prints each message as JSON with its topic (`kafka/TestdashFormatter.java`, compiled once against
a kafka-clients jar and copied into the container). Topics and consumer group lag are polled with `kafka-topics`
and `kafka-consumer-groups` only while their tabs are open, since each poll starts a JVM beside the broker.

Other test runners can feed the progress views too: a reporter writes JSON lines to `progress-<id>.jsonl` in the
directory JVM tests get as the `testdash.dir` system property. `progress.py` documents the events.
