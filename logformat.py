"""Reading log lines: parsers for common formats, and the simplified view that keeps only the level and message.

Formats: Spring Boot's console pattern (2.x, 3.x, and 3.2+ with the application name and correlation id), Logback
and Log4j 2's usual patterns, JSON lines (Spring Boot structured logging as Logstash, ECS or GELF, and MongoDB's),
Kafka, MariaDB/MySQL, Postgres and Redis, and any line that starts with a timestamp. A line no parser recognises,
such as a stack trace frame, is a continuation of the record above it.
"""

from __future__ import annotations

import json
import re
import time
from dataclasses import dataclass
from functools import lru_cache

from rich.text import Text

ANSI = re.compile(r"\x1b\[[0-9;]*m")
LEVEL_NAMES = r"TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL"
TIMESTAMP = r"\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?"
MDC = r"(?:\[(?P<context>[^\]]*)\]\s?)?"  # Spring Boot apps often put MDC values before the message

PATTERNS = [
    # 2026-10-12 15:45:51.006  INFO 1 --- [app] [           main] [trace-span] c.l.p.Foo   : [mdc] message
    re.compile(rf"^{TIMESTAMP}\s+(?P<level>{LEVEL_NAMES})\s+(?:\d+\s+)?---\s+(?:\[[^\]]*\]\s*)+[\w.$/\[\]-]+\s*:\s"
               rf"{MDC}(?P<message>.*)$"),
    # 15:45:51.006 [main] INFO  c.l.p.Foo -- message, or 2026-10-12 15:45:51,006 INFO  [main] c.l.p.Foo - message
    re.compile(rf"^(?:\d{{4}}-\d{{2}}-\d{{2}}[ T])?\d{{2}}:\d{{2}}:\d{{2}}[.,]\d{{3}}\S*\s+(?:\[[^\]]+\]\s+)?"
               rf"(?P<level>{LEVEL_NAMES})\s+(?:\[[^\]]+\]\s+)?[\w.$]+\s+--?\s{MDC}(?P<message>.*)$"),
    # [2026-10-12 15:45:58,569] INFO [QuorumController id=1] message (org.apache.kafka.Foo)
    re.compile(rf"^\[\d{{4}}-[^\]]+\]\s+(?P<level>{LEVEL_NAMES})\s+(?P<message>.*?)(?:\s+\([\w.$]+\))?$"),
    # 2026-10-12 15:45:25 0 [Note] InnoDB: message, or 2026-10-12T15:45:25.1Z 0 [System] [MY-010116] [Server] message
    re.compile(rf"^{TIMESTAMP}\s+(?:\d+\s+)?\[(?P<level>Note|Warning|Warn|Error|ERROR|System)\]\s+(?:\[MY-\d+\]\s+)?"
               r"(?P<message>.*)$"),
    # 2026-10-12 15:45:25.123 UTC [1] LOG:  message
    re.compile(rf"^{TIMESTAMP}(?: [A-Z]{{2,5}})?\s+\[\d+\]\s+(?P<level>[A-Z]+\d?):\s+(?P<message>.*)$"),
    # 1:M 12 Oct 2026 15:45:25.123 * message
    re.compile(r"^\d+:[XCSM] \d{1,2} \w{3} \d{4} \d{2}:\d{2}:\d{2}\.\d{3} (?P<level>[.\-*#]) (?P<message>.*)$"),
    # 2026-10-12 15:45:51.006 [WARN] message, or a timestamp and a message (WireMock's own log)
    re.compile(rf"^\[?{TIMESTAMP}\]?\s+(?:\[?(?P<level>{LEVEL_NAMES})\]?:?\s+)?(?P<message>.*)$"),
]
LEVELS = {"TRACE": "TRACE", "DEBUG": "DEBUG", "INFO": "INFO", "WARN": "WARN", "WARNING": "WARN", "ERROR": "ERROR",
          "FATAL": "FATAL", "SEVERE": "ERROR", "PANIC": "FATAL", "NOTE": "INFO", "SYSTEM": "INFO", "LOG": "INFO",
          "NOTICE": "INFO", "F": "FATAL", "E": "ERROR", "W": "WARN", "I": "INFO", "D": "DEBUG",
          ".": "DEBUG", "-": "DEBUG", "*": "INFO", "#": "WARN"}
SYSLOG_LEVELS = {0: "FATAL", 1: "FATAL", 2: "FATAL", 3: "ERROR", 4: "WARN", 5: "INFO", 6: "INFO", 7: "DEBUG"}
PROBLEM_LEVELS = ("WARN", "ERROR", "FATAL")
LEVEL_STYLE = {"FATAL": "bold white on red", "ERROR": "bold red", "WARN": "bold yellow", "INFO": "green",
               "DEBUG": "dim", "TRACE": "dim"}
LINE_STYLE = {"FATAL": "red", "ERROR": "red", "WARN": "yellow", "DEBUG": "dim", "TRACE": "dim"}
STACK_FRAME = re.compile(r"^\s+(?:at |\.\.\. \d+ )")
EXCEPTION = re.compile(r"^(?:Caused by: |Suppressed: )?(?:[a-z_$][\w$]*\.)+[A-Z][\w$]*(?:Exception|Error|Throwable)\b")
UNPARSED_LEVEL = re.compile(r"\b(FATAL|ERROR|SEVERE|WARN(?:ING)?)\b")
UNPARSED_PROBLEM = re.compile(r"\b(FATAL|ERROR|SEVERE|WARN(?:ING)?)\b|Exception\b|^\s+at |^Caused by:")
GUTTER = " " * 6  # the level column, so continuation lines line up with messages


@dataclass(frozen=True)
class Record:
    level: str | None
    message: str
    context: str = ""  # MDC values, which say what the line is about


@lru_cache(maxsize=100_000)
def parse(text: str) -> Record | None:
    plain = ANSI.sub("", text)
    if plain.startswith("{"):
        return _json(plain)
    for pattern in PATTERNS:
        if match := pattern.match(plain):
            level, message = match["level"], match["message"]
            if level is not None and _level(level) is None:  # Postgres' DETAIL:, STATEMENT: and the like
                message = f"{level}: {message}"
            return Record(_level(level), message, (match.groupdict().get("context") or "").strip())
    return None


def is_problem(text: str) -> bool:
    """A warning or error, or part of a stack trace: what the errors-only filter keeps."""
    record = parse(text)
    return record.level in PROBLEM_LEVELS if record else bool(UNPARSED_PROBLEM.search(ANSI.sub("", text)))


def is_logged_problem(text: str) -> bool:
    """A warning or error log entry, without the stack trace lines that follow it."""
    record = parse(text)
    if record:
        return record.level in PROBLEM_LEVELS
    plain = ANSI.sub("", text)
    return not plain[:1].isspace() and bool(UNPARSED_LEVEL.search(plain[:200]))


def render(text: str, at: float, *, simple: bool, show_time: bool) -> Text:
    """One log line, simplified to its level and message or as it was logged, after an optional time column."""
    line = Text()
    if show_time:
        line.append(time.strftime("%H:%M:%S ", time.localtime(at)) if at else " " * 9, style="dim")
    record = parse(text)
    if not simple:
        body = Text.from_ansi(text)
        if record and record.level in LINE_STYLE and "\x1b[" not in text:
            body.stylize(LINE_STYLE[record.level])
        return line.append_text(body)
    if record is None:
        return line.append_text(_continuation(text))
    message, _, more = record.message.partition("\n")  # JSON logs carry the stack trace in the record
    if record.level is None:
        line.append(message)
    else:
        line.append(f"{record.level:<5} ", style=LEVEL_STYLE.get(record.level, ""))
        if record.context:
            line.append(f"[{record.context}] ", style="dim")
        line.append(message, style=LINE_STYLE.get(record.level, ""))
    for extra in more.splitlines():
        line.append("\n").append_text(_continuation(extra))
    return line


def _continuation(text: str) -> Text:
    if "\x1b[" in text:  # styled by whoever wrote it, like testdash's own Gradle output
        return Text.from_ansi(text)
    stripped = text.strip()
    if EXCEPTION.match(stripped):
        return Text(GUTTER + stripped, style="red")
    if STACK_FRAME.match(text):
        return Text(GUTTER + stripped, style="dim")
    return Text(text)


def _level(name: str | int | None) -> str | None:
    if isinstance(name, int):  # GELF uses syslog severities
        return SYSLOG_LEVELS.get(name)
    return LEVELS.get(name.upper()) if name else None


def _json(text: str) -> Record | None:
    try:
        record = json.loads(text)
    except ValueError:
        return None
    if not isinstance(record, dict):
        return None
    if {"t", "s", "msg"} <= record.keys():  # MongoDB
        attr = record.get("attr")
        return Record(_level(record["s"]), record["msg"] + (f"  {json.dumps(attr)}" if attr else ""))
    log = record.get("log") if isinstance(record.get("log"), dict) else {}
    level = record.get("level") or record.get("log.level") or log.get("level") or record.get("severity")
    message = record.get("message") or record.get("short_message") or record.get("msg")
    if message is None:
        return None
    stack = record.get("stack_trace") or record.get("error.stack_trace") or record.get("full_message")
    if isinstance(stack, str) and stack.strip() and stack != message:
        message = f"{message}\n{stack}"
    return Record(_level(level), str(message))
