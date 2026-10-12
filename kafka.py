"""Kafka through its own command-line tools, run in its container: every message, the topics, and the consumer
groups' lag.

kafka-console-consumer follows every topic at once (--include), but its formatter cannot print a message's topic,
so testdash gives it one (kafka/TestdashFormatter.java) that prints each message as a JSON line. It is compiled
once against a kafka-clients jar (from the Gradle cache, or copied out of the container), kept in
~/.cache/testdash, and copied into the container, so nothing is installed and no image is pulled.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import os
import subprocess
import tempfile
import uuid
import zipfile
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from services import Service

HERE = Path(__file__).resolve().parent
FORMATTER_SOURCE = HERE / "kafka" / "TestdashFormatter.java"
FORMATTER_CLASS = "testdash.TestdashFormatter"
CONTAINER_JAR = "/tmp/testdash-kafka.jar"
GRADLE_CACHE = Path.home() / ".gradle" / "caches" / "modules-2" / "files-2.1"
TOOL_DIRS = ("", "/opt/kafka/bin/", "/opt/bitnami/kafka/bin/")
# The tools run beside the broker: a small heap, and no JMX port to clash with the broker's.
TOOL_ENV = ("-e", "KAFKA_HEAP_OPTS=-Xmx128m", "-e", "JMX_PORT=", "-e", f"CLASSPATH={CONTAINER_JAR}")
GROUP_PREFIX = "testdash-"


@dataclass
class KafkaMessage:
    at: float  # the record's timestamp, epoch seconds
    topic: str
    partition: int
    offset: int
    key: str | None
    value: str | None
    headers: dict[str, str | None]


@dataclass
class GroupLag:
    group: str
    topic: str
    partition: int
    offset: int | None  # the group's committed offset
    end: int | None
    lag: int | None
    consumer: str  # empty when the group has no active member on the partition


class Kafka:
    def __init__(self, service: Service):
        self.service = service
        self.error: str | None = None  # why messages cannot be read, for the view to say
        self._container: str | None = None
        self._tools: tuple[str, str] | None = None  # (directory, suffix) of the kafka-* scripts
        self._bootstrap = "localhost:9092"

    async def follow(self, on_messages: Callable[[list[KafkaMessage]], None]) -> None:
        """Feeds every message on every topic to on_messages, from each topic's start, while the container runs."""
        group = GROUP_PREFIX + uuid.uuid4().hex[:8]
        while True:
            if self.service.state == "running" and self.service.container_id and await self._setup():
                await self._consume(group, on_messages)
            await asyncio.sleep(2)

    async def topics(self) -> list[str]:
        if not await self._setup():
            return []
        output = await self._tool("kafka-topics", "--bootstrap-server", self._bootstrap, "--list")
        return [line.strip() for line in output.splitlines() if line.strip() and not line.startswith("__")]

    async def groups(self) -> list[GroupLag]:
        """Each consumer group's position and lag on each partition, testdash's own group left out."""
        if not await self._setup():
            return []
        output = await self._tool("kafka-consumer-groups", "--bootstrap-server", self._bootstrap, "--describe",
                                  "--all-groups")
        lags = []
        for line in output.splitlines():
            fields = line.split()
            if len(fields) < 6 or not fields[2].isdigit() or fields[0].startswith(GROUP_PREFIX):
                continue
            number = lambda text: int(text) if text.lstrip("-").isdigit() else None  # noqa: E731
            lags.append(GroupLag(fields[0], fields[1], int(fields[2]), number(fields[3]), number(fields[4]),
                                 number(fields[5]), fields[6] if len(fields) > 6 and fields[6] != "-" else ""))
        return lags

    async def _setup(self) -> bool:
        container = self.service.container_id
        if container is None:
            return False
        if container == self._container:
            return self._tools is not None
        self._container, self._tools, self.error = container, None, None
        tools = await _output("docker", "exec", container, "sh", "-c", " ".join(
            f'command -v {d}kafka-console-consumer{s} >/dev/null 2>&1 && {{ echo "{d}|{s}"; exit 0; }};'
            for d in TOOL_DIRS for s in ("", ".sh")) + " exit 1")
        if "|" not in tools:
            self.error = "the container has no kafka-console-consumer"
            return False
        directory, suffix = tools.strip().split("|", 1)
        self._bootstrap = _bootstrap(await _container_env(container))
        try:
            jar = await asyncio.to_thread(formatter_jar, container)
        except FormatterUnavailable as e:
            self.error = str(e)
            return False
        if not await _succeeds("docker", "cp", str(jar), f"{container}:{CONTAINER_JAR}"):
            self.error = "could not copy testdash's message formatter into the container"
            return False
        self._tools = (directory, suffix)
        return True

    async def _consume(self, group: str, on_messages: Callable[[list[KafkaMessage]], None]) -> None:
        """Until the container stops, or a new run's container takes over from it."""
        container = self.service.container_id
        for include in ("--include", "--whitelist"):  # --whitelist before Kafka 2.8
            process = await asyncio.create_subprocess_exec(
                "docker", "exec", *TOOL_ENV, container, self._script("kafka-console-consumer"),
                "--bootstrap-server", self._bootstrap, include, ".*", "--from-beginning", "--group", group,
                "--formatter", FORMATTER_CLASS,
                "--consumer-property", "metadata.max.age.ms=2000",  # topics created later are read within 2s
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
            got_any = False
            try:
                batch: list[KafkaMessage] = []
                while True:
                    try:
                        line = await asyncio.wait_for(process.stdout.readline(), timeout=0.3 if batch else 1)
                    except asyncio.TimeoutError:
                        line = None
                    if self.service.container_id != container:
                        process.kill()
                        return
                    if line:
                        message = _message(line)
                        if message is not None:
                            batch.append(message)
                            if len(batch) < 500:
                                continue
                    if batch:
                        got_any = True
                        on_messages(batch)
                        batch = []
                    if line == b"":
                        break
            except asyncio.CancelledError:
                process.kill()
                raise
            stderr = (await process.stderr.read()).decode("utf-8", errors="replace")
            await process.wait()
            if got_any or include not in stderr:
                if process.returncode and not got_any:
                    self.error = (stderr.strip().splitlines() or ["kafka-console-consumer stopped"])[-1][:200]
                return

    def _script(self, name: str) -> str:
        directory, suffix = self._tools or ("", "")
        return f"{directory}{name}{suffix}"

    async def _tool(self, name: str, *args: str) -> str:
        return await _output("docker", "exec", *TOOL_ENV, self.service.container_id, self._script(name), *args)


class FormatterUnavailable(Exception):
    pass


def formatter_jar(container: str) -> Path:
    """The formatter, compiled against a kafka-clients jar from the Gradle cache or else from the container."""
    cache = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "testdash"
    jar = cache / f"kafka-formatter-{hashlib.sha1(FORMATTER_SOURCE.read_bytes()).hexdigest()[:12]}.jar"
    if jar.exists():
        return jar
    cache.mkdir(parents=True, exist_ok=True)
    from gradle import ListenerUnavailable, _javac  # the JUnit listener's build finds javac the same way
    try:
        javac = _javac()
    except ListenerUnavailable as e:
        raise FormatterUnavailable(str(e)) from e
    with tempfile.TemporaryDirectory() as tmp:
        clients = _kafka_clients_jar(container, Path(tmp))
        classes = Path(tmp) / "classes"
        result = subprocess.run([javac, "--release", "8", "-nowarn", "-implicit:none", "-d", str(classes),
                                 "-cp", str(clients), str(FORMATTER_SOURCE)], capture_output=True, text=True)
        if result.returncode != 0:
            raise FormatterUnavailable(f"javac failed: {result.stderr.strip()[:300]}")
        partial = jar.with_suffix(".tmp")
        with zipfile.ZipFile(partial, "w") as zf:
            for path in classes.rglob("*.class"):
                zf.write(path, path.relative_to(classes).as_posix())
        partial.replace(jar)
    return jar


def _kafka_clients_jar(container: str, tmp: Path) -> Path:
    """MessageFormatter moved to kafka-clients in Kafka 2.7, so a 3.x jar from the Gradle cache, else the broker's."""
    from gradle import _version_key
    versions = [d for d in (GRADLE_CACHE / "org.apache.kafka" / "kafka-clients").glob("*") if d.name[:1].isdigit()
                and int(d.name.split(".")[0]) >= 3]
    for version in sorted(versions, key=lambda d: _version_key(d.name), reverse=True):
        jars = list(version.glob(f"*/kafka-clients-{version.name}.jar"))
        if jars:
            return jars[0]
    found = subprocess.run(["docker", "exec", container, "sh", "-c",
                            "ls /usr/share/java/kafka/kafka-clients-*.jar /opt/kafka/libs/kafka-clients-*.jar "
                            "/opt/bitnami/kafka/libs/kafka-clients-*.jar 2>/dev/null | head -1"],
                           capture_output=True, text=True).stdout.strip()
    if not found:
        raise FormatterUnavailable("no kafka-clients jar in the Gradle cache or the container")
    target = tmp / "kafka-clients.jar"
    if subprocess.run(["docker", "cp", f"{container}:{found}", str(target)], capture_output=True).returncode != 0:
        raise FormatterUnavailable(f"could not copy {found} out of the container")
    return target


def _bootstrap(env: dict[str, str]) -> str:
    """A plaintext listener the broker advertises with an address it can reach itself: the inter-broker listener
    when it is plaintext (Testcontainers advertises its PLAINTEXT one on the host's port), else the first."""
    get = lambda name: env.get(f"KAFKA_{name}") or env.get(f"KAFKA_CFG_{name}") or ""  # noqa: E731
    protocols = dict(item.split(":", 1) for item in get("LISTENER_SECURITY_PROTOCOL_MAP").split(",") if ":" in item)
    controllers = set(get("CONTROLLER_LISTENER_NAMES").split(","))
    listeners = []
    for listener in get("LISTENERS").split(","):
        name, _, address = listener.strip().partition("://")
        port = address.rsplit(":", 1)[-1]
        if port.isdigit() and name not in controllers and protocols.get(name, name) == "PLAINTEXT":
            listeners.append((name, port))
    inter_broker = get("INTER_BROKER_LISTENER_NAME")
    listeners.sort(key=lambda listener: listener[0] != inter_broker)
    return f"localhost:{listeners[0][1]}" if listeners else "localhost:9092"


def _message(line: bytes) -> KafkaMessage | None:
    try:
        raw = json.loads(line)
        return KafkaMessage(raw["timestamp"] / 1000, raw["topic"], raw["partition"], raw["offset"], raw.get("key"),
                            raw.get("value"), raw.get("headers") or {})
    except (ValueError, KeyError, TypeError):
        return None  # the consumer's own warnings


async def _container_env(container: str) -> dict[str, str]:
    output = await _output("docker", "inspect", "--format", "{{json .Config.Env}}", container)
    try:
        return dict(item.split("=", 1) for item in json.loads(output) if "=" in item)
    except (ValueError, TypeError):
        return {}


async def _output(*command: str) -> str:
    process = await asyncio.create_subprocess_exec(
        *command, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
    stdout, _ = await process.communicate()
    return stdout.decode("utf-8", errors="replace")


async def _succeeds(*command: str) -> bool:
    process = await asyncio.create_subprocess_exec(
        *command, stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL)
    return await process.wait() == 0
