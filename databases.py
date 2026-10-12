"""The queries a database is asked, as the database itself records them, read with its own client in its container.

MariaDB and MySQL keep a general query log in the mysql.general_log table once log_output=TABLE and general_log
are set; testdash reads new rows each second with sql_log_off set, so its own reads are not logged. MongoDB's
profiler keeps every operation in each database's system.profile collection at level 2; one mongosh follows them
all, turning the profiler on for each database as it appears. Both are switched on over a fresh connection to
the server that serves the tests: the images' entrypoints first run a temporary server for their init scripts,
which MariaDB keeps off TCP, and a restarted mongod drops the profiler, which mongosh then switches on again.
"""

from __future__ import annotations

import asyncio
import json
import re
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import datetime

from services import Service

QUERY_KINDS = {"mariadb", "mysql", "mongodb"}
MONGO_PROFILE_BYTES = 64 * 1024 * 1024  # the default 1MB profile collection rolls over within a test or two
# The session chatter a JDBC pool sends: hidden until asked for
SQL_NOISE = re.compile(r"^\s*(SET\s|SHOW\s|SELECT\s+@@|SELECT\s+1\s*$|/\*\s*ping|SELECT\s+DATABASE\(\)|"
                       r"SELECT\s+VERSION\(\))", re.IGNORECASE)
SQL_COMMANDS = {"Query", "Execute"}  # Connect, Quit, Init DB, Prepare and the like are noise too


@dataclass
class Query:
    at: float  # epoch seconds, by the database's clock
    connection: str
    kind: str  # Query, Execute, Connect... for SQL; find, insert, update, aggregate... for MongoDB
    target: str  # MongoDB's database.collection; empty for SQL
    text: str  # the SQL, or MongoDB's command as JSON
    millis: float | None = None
    detail: dict = field(default_factory=dict)  # MongoDB's profile entry

    @property
    def noise(self) -> bool:
        if self.target:
            return False
        return self.kind not in SQL_COMMANDS or bool(SQL_NOISE.match(self.text))


class QueryLog:
    """Follows a database's queries and feeds them to on_queries; error says why it cannot."""

    def __init__(self, service: Service):
        self.service = service
        self.error: str | None = None

    async def follow(self, on_queries: Callable[[list[Query]], None]) -> None:
        raise NotImplementedError


def query_log(service: Service) -> QueryLog | None:
    if service.kind in ("mariadb", "mysql"):
        return SqlQueryLog(service)
    if service.kind == "mongodb":
        return MongoQueryLog(service)
    return None


class SqlQueryLog(QueryLog):
    SWITCH_ON = "SET GLOBAL log_output = 'TABLE'; SET GLOBAL general_log = 1;"
    READ = ("SET SESSION sql_log_off = 1; "
            "SELECT UNIX_TIMESTAMP(event_time), thread_id, command_type, CONVERT(argument USING utf8mb4) "
            "FROM mysql.general_log WHERE event_time > FROM_UNIXTIME({since:.6f}) AND thread_id <> CONNECTION_ID() "
            "ORDER BY event_time")

    def __init__(self, service: Service):
        super().__init__(service)
        self._own_threads: set[str] = set()  # testdash's earlier reads: their connect and sql_log_off lines
        self._env: dict[str, str] = {}

    async def follow(self, on_queries: Callable[[list[Query]], None]) -> None:
        container, since = None, 0.0
        while True:
            await asyncio.sleep(1)
            if self.service.state != "running" or not self.service.container_id:
                continue
            if container != self.service.container_id:
                container, since, self._own_threads = self.service.container_id, 1.0, set()
                self._env = await _container_env(container)
                if not await self._switch_on(container):
                    container = None  # not up yet, or the temporary init server
                    continue
            output, ok = await self._mysql(container, self.READ.format(since=since))
            if not ok:
                container = None  # a restart turns the log off again
                continue
            queries = self._parse(output)
            if queries:
                since = max(q.at for q in queries)
                on_queries([q for q in queries if q.connection not in self._own_threads])

    async def _switch_on(self, container: str) -> bool:
        _, ok = await self._mysql(container, self.SWITCH_ON)
        if ok:
            self.error = None
        return ok

    async def _mysql(self, container: str, sql: str) -> tuple[str, bool]:
        env = self._env
        password = env.get("MARIADB_ROOT_PASSWORD") or env.get("MYSQL_ROOT_PASSWORD")
        if password is None and not any(env.get(k) for k in ("MARIADB_ALLOW_EMPTY_ROOT_PASSWORD",
                                                               "MYSQL_ALLOW_EMPTY_PASSWORD")):
            self.error = "no root password in the container's environment, which reading the query log needs"
            return "", False
        script = ('client=$(command -v mariadb || command -v mysql) && '
                  'exec "$client" --protocol=TCP -h127.0.0.1 -uroot --batch --skip-column-names -e "$1"')
        process = await asyncio.create_subprocess_exec(
            "docker", "exec", "-e", f"MYSQL_PWD={password or ''}", container, "sh", "-c", script, "sh", sql,
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
        stdout, stderr = await process.communicate()
        if process.returncode != 0:
            message = stderr.decode("utf-8", errors="replace").strip().splitlines()
            if message and "Can't connect" not in message[-1]:  # not up yet is not an error
                self.error = message[-1][:200]
            return "", False
        return stdout.decode("utf-8", errors="replace"), True

    def _parse(self, output: str) -> list[Query]:
        queries = []
        for line in output.splitlines():
            fields = line.split("\t")
            if len(fields) < 4:
                continue
            at, thread, command, argument = fields[0], fields[1], fields[2], "\t".join(fields[3:])
            text = _unescape(argument)
            if text.startswith("SET SESSION sql_log_off"):
                self._own_threads.add(thread)
            try:
                queries.append(Query(float(at), thread, command, "", text))
            except ValueError:
                continue
        return queries


class MongoQueryLog(QueryLog):
    SCRIPT = """
const known = {};
while (true) {
  for (const {name} of db.adminCommand({listDatabases: 1, nameOnly: true}).databases) {
    if (['admin', 'local', 'config'].includes(name)) continue;
    const d = db.getSiblingDB(name);
    if (!(name in known)) {
      if (d.getProfilingStatus().was !== 2) {
        d.setProfilingLevel(0);
        d.system.profile.drop();
        d.createCollection('system.profile', {capped: true, size: %(size)d});
        d.setProfilingLevel(2);
      }
      known[name] = new Date(0);
    }
    // the profiler records testdash's own reads too
    d.system.profile.find({ts: {$gt: known[name]}, appName: {$ne: '%(app)s'}, ns: {$ne: name + '.system.profile'}})
     .sort({ts: 1}).forEach(entry => {
      known[name] = entry.ts;
      print(EJSON.stringify(entry, {relaxed: true}));
    });
  }
  sleep(500);
}
""" % {"size": MONGO_PROFILE_BYTES, "app": "testdash"}

    async def follow(self, on_queries: Callable[[list[Query]], None]) -> None:
        seen: set[tuple] = set()  # a restarted mongosh reads each profile from its start again
        container = None
        while True:
            if self.service.state == "running" and self.service.container_id:
                if container != self.service.container_id:
                    container, seen = self.service.container_id, set()
                await self._follow_once(container, seen, on_queries)
            await asyncio.sleep(2)

    async def _follow_once(self, container: str, seen: set[tuple], on_queries) -> None:
        env = await _container_env(container)
        auth = []
        if env.get("MONGO_INITDB_ROOT_USERNAME"):
            auth = ["-u", env["MONGO_INITDB_ROOT_USERNAME"], "-p", env.get("MONGO_INITDB_ROOT_PASSWORD", ""),
                    "--authenticationDatabase", "admin"]
        process = await asyncio.create_subprocess_exec(
            "docker", "exec", container, "mongosh", "mongodb://127.0.0.1:27017/?appName=testdash", "--quiet", *auth,
            "--eval", self.SCRIPT,
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
        got_any = False
        try:
            batch: list[Query] = []
            while True:
                try:
                    line = await asyncio.wait_for(process.stdout.readline(), timeout=0.3 if batch else 1)
                except asyncio.TimeoutError:
                    line = None
                if self.service.container_id != container:
                    process.kill()
                    return
                if line:
                    query = _mongo_query(line)
                    key = query and (query.at, query.target, query.kind, query.text)
                    if query is not None and key not in seen:
                        seen.add(key)
                        batch.append(query)
                        if len(batch) < 500:
                            continue
                if batch:
                    got_any = True
                    self.error = None
                    on_queries(batch)
                    batch = []
                if line == b"":
                    break
        except asyncio.CancelledError:
            process.kill()
            raise
        stderr = (await process.stderr.read()).decode("utf-8", errors="replace").strip().splitlines()
        await process.wait()
        if not got_any and stderr and "ECONNREFUSED" not in stderr[-1] and "MongoNetworkError" not in stderr[-1]:
            self.error = ("the container has no mongosh (MongoDB 6 and later have it)" if "executable file not found"
                          in stderr[-1] else stderr[-1][:200])


def _mongo_query(line: bytes) -> Query | None:
    try:
        entry = json.loads(line)
        at = datetime.fromisoformat(entry["ts"]["$date"].replace("Z", "+00:00")).timestamp()
    except (ValueError, KeyError, TypeError):
        return None
    command = entry.get("command") or entry.get("query") or {}
    kind = next(iter(command), None) if isinstance(command, dict) else None
    if kind in (None, "q", "filter", "$db", "lsid"):  # a legacy query or a write's own document
        kind = entry.get("op", "")
    if kind in ("getMore", "killCursors", "endSessions"):
        return None  # a cursor's next batch: the query that opened it is already listed
    if isinstance(command, dict):  # the driver's bookkeeping, the same on every command
        command = {k: v for k, v in command.items() if k not in ("lsid", "$clusterTime", "$db", "$readPreference")}
    return Query(at, str(entry.get("client") or ""), kind, entry.get("ns", ""), json.dumps(command), entry.get("millis"),
                 entry)


def _unescape(text: str) -> str:
    """mysql --batch writes newline, tab, NUL and backslash as \\n, \\t, \\0 and \\\\."""
    return re.sub(r"\\(.)", lambda m: {"n": "\n", "t": "\t", "0": "\0", "\\": "\\"}.get(m[1], m[0]), text)


async def _container_env(container: str) -> dict[str, str]:
    process = await asyncio.create_subprocess_exec(
        "docker", "inspect", "--format", "{{json .Config.Env}}", container,
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
    stdout, _ = await process.communicate()
    try:
        return dict(item.split("=", 1) for item in json.loads(stdout) if "=" in item)
    except (ValueError, TypeError):
        return {}
