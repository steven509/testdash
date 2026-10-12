"""The containers a test run starts, found through the Docker CLI with no configuration.

A container is named after its Compose service (Testcontainers' compose projects get random names, the services
do not), and what it is comes from its image: WireMock, MongoDB, MariaDB and the like by name, Testcontainers'
own helpers hidden, anything else is the application. A container belongs to the run when it was created after
the run started and either Testcontainers labelled it or its Compose file lives in this repo, so containers from
other runs and other projects on the same machine stay out. Logs are kept after a container is removed, and a
container restarted mid-run is followed again from where it stopped.
"""

from __future__ import annotations

import asyncio
import json
import shutil
import time
from collections import deque
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Protocol

KINDS = (  # image name keyword -> kind
    ("wiremock", "wiremock"), ("mockserver", "mockserver"), ("mongo", "mongodb"), ("mariadb", "mariadb"),
    ("mysql", "mysql"), ("postgres", "postgres"), ("kafka", "kafka"), ("zookeeper", "zookeeper"),
    ("redis", "redis"), ("rabbitmq", "rabbitmq"), ("localstack", "localstack"), ("elasticsearch", "elasticsearch"),
    ("opensearch", "opensearch"), ("minio", "minio"), ("keycloak", "keycloak"), ("nginx", "nginx"),
)
INFRA = ("ryuk", "socat")  # Testcontainers' resource reaper and port ambassador
POLL_SECONDS = 1.0
CLOCK_SLACK_SECONDS = 15  # Docker's clock (a VM on macOS) can drift from the host's
MAX_LINES = 50_000


@dataclass
class LogLine:
    at: float  # epoch seconds from Docker's timestamp, 0 if it had none
    text: str


@dataclass
class Service:
    key: str
    kind: str
    image: str
    container_id: str | None = None
    state: str = "running"  # running | stopped | previous (from an earlier run)
    exposed_ports: list[str] = field(default_factory=list)
    host_ports: dict[str, str] = field(default_factory=dict)
    command: list[str] = field(default_factory=list)
    lines: deque = field(default_factory=lambda: deque(maxlen=MAX_LINES))
    problems: int = 0  # warnings and errors logged this run, counted as lines arrive

    @property
    def is_app(self) -> bool:
        return self.kind == "app"


class Listener(Protocol):
    def service_added(self, service: Service) -> None: ...
    def service_changed(self, service: Service) -> None: ...
    def service_lines(self, service: Service, lines: list[LogLine]) -> None: ...


IDENTITY_LABELS = ("org.testcontainers.sessionId", "com.docker.compose.project")
CLAIM_WAIT_SECONDS = 15  # how long a container waits for the run's tests to claim it


class Watcher:
    """Finds the containers a run starts and follows their logs. When the run's tests use Testcontainers, they
    name the labels of their containers (see claims), so only those are taken, even while another run starts
    containers from the same repo. Otherwise a container is the run's when it was created after the run
    started and Testcontainers labelled it, or its Compose file is in the repo."""

    def __init__(self, listener: Listener, since: float, scope_dirs: list[Path], include_all: bool = False,
                 claims: Callable[[], tuple[bool | None, set[str]]] = lambda: (False, set()),
                 run_active: Callable[[], bool] = lambda: True):
        self.listener = listener
        self.claims = claims  # whether the tests use Testcontainers (None: not said yet), and their labels
        self.run_active = run_active
        self.since = since
        self.scope_dirs = {str(d) for d in scope_dirs} | {str(d.resolve()) for d in scope_dirs}
        self.include_all = include_all
        self.services: dict[str, Service] = {}
        self._by_container: dict[str, Service] = {}
        self._followers: dict[str, asyncio.Task] = {}
        self._last_seen: dict[str, float] = {}  # container -> last log timestamp, to resume after a restart
        self._ignored: set[str] = set()
        self._retired: set[str] = set()  # earlier runs' containers, never a later run's however recent
        self._first_seen: dict[str, float] = {}  # containers waiting to be claimed
        self._pending: dict[str, list[LogLine]] = {}

    @staticmethod
    def available() -> bool:
        return shutil.which("docker") is not None

    def new_run(self, started_at: float) -> None:
        """Starts a run's scope: earlier containers' logs are cleared and their services wait for new ones."""
        self.since = started_at
        for task in self._followers.values():
            task.cancel()
        self._followers.clear()
        self._retired.update(self._by_container)
        self._by_container.clear()
        self._ignored.clear()
        self._first_seen.clear()
        self._pending.clear()
        for service in self.services.values():
            service.lines.clear()
            service.problems = 0
            service.container_id = None
            service.state = "previous"
            self.listener.service_changed(service)

    async def run(self) -> None:
        while True:
            try:
                await self._scan()
            except (OSError, ValueError):
                pass
            self._flush()
            await asyncio.sleep(POLL_SECONDS / 4)
            self._flush()
            await asyncio.sleep(POLL_SECONDS * 3 / 4)

    async def _scan(self) -> None:
        running = await _running_container_ids()
        for container_id in running:
            if container_id in self._ignored:
                continue
            service = self._by_container.get(container_id)
            if service is None:
                details = await _inspect(container_id)
                belongs = self._belongs(container_id, details) if details is not None else False
                if belongs is None:
                    continue  # asked again on the next scan
                if not belongs:
                    self._ignored.add(container_id)
                    continue
                service = self._service_for(container_id, details)
                if service is None:
                    self._ignored.add(container_id)
                    continue
            if container_id not in self._followers or self._followers[container_id].done():
                if service.state != "running":
                    service.state = "running"
                    self.listener.service_changed(service)
                self._followers[container_id] = asyncio.create_task(self._follow(service, container_id))
        for container_id, service in list(self._by_container.items()):
            if container_id not in running and service.state == "running" and service.container_id == container_id:
                service.state = "stopped"
                self.listener.service_changed(service)

    def _belongs(self, container_id: str, details: dict) -> bool | None:
        """True for the run's container, False for anyone else's, None while the run's tests may yet claim it."""
        if self.include_all:
            return True
        if container_id in self._retired or _parse_time(details.get("Created", "")) < self.since - CLOCK_SLACK_SECONDS:
            return False
        labels = (details.get("Config") or {}).get("Labels") or {}
        identity = {f"{key}={labels[key]}" for key in IDENTITY_LABELS if key in labels}
        uses_testcontainers, claimed = self.claims()
        if identity & claimed:
            return True
        if identity and uses_testcontainers is not False:  # unclaimed so far
            waited = time.time() - self._first_seen.setdefault(container_id, time.time())
            still_running = self.run_active() and (uses_testcontainers is None or waited < CLAIM_WAIT_SECONDS)
            return None if still_running else False
        if any(label.startswith("org.testcontainers") for label in labels):
            return True
        compose = " ".join(labels.get(k, "") for k in ("com.docker.compose.project.config_files",
                                                        "com.docker.compose.project.working_dir"))
        return any(directory in compose for directory in self.scope_dirs)

    def _service_for(self, container_id: str, details: dict) -> Service | None:
        config = details.get("Config") or {}
        image = config.get("Image") or ""
        labels = config.get("Labels") or {}
        kind = classify(image)
        if kind is None:
            return None
        base = labels.get("com.docker.compose.service") or _short_image(image)
        key, number = base, 1
        service = self.services.get(key)
        while service is not None and service.state == "running" and service.container_id not in (None, container_id):
            number += 1  # two at once, e.g. the same image started twice
            key = f"{base} {number}"
            service = self.services.get(key)
        ports = sorted((config.get("ExposedPorts") or {}).keys())
        host_ports = {port: bindings[0].get("HostPort") for port, bindings in
                      ((details.get("NetworkSettings") or {}).get("Ports") or {}).items() if bindings}
        command = list(config.get("Cmd") or [])
        if service is None:
            service = Service(key, kind, image)
            self.services[key] = service
            service.container_id, service.exposed_ports, service.host_ports, service.command = (
                container_id, ports, host_ports, command)
            self.listener.service_added(service)
        else:
            service.image, service.container_id, service.exposed_ports, service.host_ports, service.command = (
                image, container_id, ports, host_ports, command)
            service.state = "running"
            self.listener.service_changed(service)
        self._by_container[container_id] = service
        return service

    async def _follow(self, service: Service, container_id: str) -> None:
        command = ["docker", "logs", "--follow", "--timestamps"]
        if container_id in self._last_seen:
            command += ["--since", str(self._last_seen[container_id])]
        process = await asyncio.create_subprocess_exec(
            *command, container_id, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.STDOUT)
        try:
            while line := await process.stdout.readline():
                if line.startswith(b"Error response from daemon:"):
                    continue  # docker's own, e.g. following a container that is being removed
                entry = _log_line(line.decode("utf-8", errors="replace").rstrip("\n"))
                if entry.at:
                    self._last_seen[container_id] = entry.at
                self._pending.setdefault(service.key, []).append(entry)
            await process.wait()
        except asyncio.CancelledError:  # a new run, or quitting: nothing left to show the lines in
            process.kill()
            raise
        self._flush()  # the container stopped, so its last lines go out now rather than at the next scan

    def _flush(self) -> None:
        pending, self._pending = self._pending, {}
        for key, lines in pending.items():
            service = self.services.get(key)
            if service is not None:
                service.lines.extend(lines)
                self.listener.service_lines(service, lines)


def classify(image: str) -> str | None:
    """The kind of service an image runs, None for Testcontainers' own helpers, "app" for anything unknown."""
    short = _short_image(image)
    if any(helper in short for helper in INFRA):
        return None
    for keyword, kind in KINDS:
        if keyword in short:
            return kind
    return "app"


async def port_holder(port: str) -> str | None:
    """What holds a host port: a container, with where its Compose project lives, or else a process."""
    output = await _docker("ps", "--filter", f"publish={port}", "--format",
                           '{{.Names}}\t{{.Label "com.docker.compose.project.working_dir"}}\t{{.RunningFor}}')
    for line in output.splitlines():
        name, workdir, running_for = (line.split("\t") + ["", ""])[:3]
        where = f", Compose project in {workdir.replace(str(Path.home()), '~')}" if workdir else ""
        return f"container {name} (up {running_for.removesuffix(' ago')}{where})"
    process = await asyncio.create_subprocess_exec(
        "lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN", "-Fcp",
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
    stdout, _ = await process.communicate()
    fields = {line[:1]: line[1:] for line in stdout.decode(errors="replace").splitlines() if line}
    return f"process {fields['c']} (pid {fields['p']})" if "c" in fields and "p" in fields else None


async def _running_container_ids() -> list[str]:
    output = await _docker("ps", "--no-trunc", "--format", "{{.ID}}")
    return output.split() if output else []


async def _inspect(container_id: str) -> dict | None:
    output = await _docker("inspect", container_id)
    try:
        return json.loads(output)[0] if output else None
    except (ValueError, IndexError):
        return None


async def _docker(*args: str) -> str:
    process = await asyncio.create_subprocess_exec(
        "docker", *args, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
    stdout, _ = await process.communicate()
    return stdout.decode("utf-8", errors="replace") if process.returncode == 0 else ""


def _short_image(image: str) -> str:
    return image.split("@")[0].rsplit("/", 1)[-1].split(":")[0].lower()


def _log_line(raw: str) -> LogLine:
    stamp, _, text = raw.partition(" ")
    at = _parse_time(stamp)
    return LogLine(at, text) if at else LogLine(0, raw)


def _parse_time(stamp: str) -> float:
    """Docker's RFC 3339 timestamps (UTC, "Z") carry nanoseconds; Python parses up to microseconds."""
    if len(stamp) < 19 or stamp[4] != "-" or stamp[10] != "T":
        return 0
    try:
        if stamp.endswith("Z"):
            stamp = stamp[:-1] + "+00:00"
        head, dot, tail = stamp.partition(".")
        if dot:
            digits = len(tail) - len(tail.lstrip("0123456789"))
            stamp = f"{head}.{tail[:digits][:6].ljust(6, '0')}{tail[digits:]}"
        return datetime.fromisoformat(stamp).timestamp()
    except ValueError:
        return 0
