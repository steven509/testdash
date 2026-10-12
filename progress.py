"""A run's live test state, from the progress-*.jsonl files testdash's JUnit listener writes.

Each file is one test JVM, a JSON object per line, each with "event" and "at" (epoch millis):
  plan        {"entries": [entry, ...]}  every class, test and test template, in run order
  registered  {"entry": entry}           a test created while running, e.g. a parameterised invocation
  started     {"id"}
  finished    {"id", "status": "SUCCESSFUL" | "FAILED" | "ABORTED", "message"}
  skipped     {"id", "message"}
  done        {}
An entry is {"id", "parent", "kind": "class" | "test" | "template", "className", "methodName", "name"}. Ids are
JUnit unique ids, the same ones discovery reports, so states map onto the catalog's tree.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path

from catalog import Entry

STATUS = {"SUCCESSFUL": "passed", "FAILED": "failed", "ABORTED": "aborted"}


@dataclass
class State:
    status: str = "pending"  # pending | running | passed | failed | aborted | skipped
    started_at: float | None = None  # epoch millis
    finished_at: float | None = None
    message: str | None = None

    def took(self, now: float) -> float | None:
        if self.started_at is None:
            return None
        return (self.finished_at or now) - self.started_at


@dataclass
class Progress:
    """Everything the run reported so far; poll() reads only what was appended since the last call."""
    run_dir: Path
    states: dict[str, State] = field(default_factory=dict)
    planned: list[Entry] = field(default_factory=list)
    registered: list[Entry] = field(default_factory=list)  # tests created while running
    jvms_done: int = 0
    # Whether the tests use Testcontainers (None until a test JVM says), and the labels on the containers
    # they started: Testcontainers' session id and each Compose project's name, as "key=value"
    testcontainers: bool | None = None
    container_labels: set[str] = field(default_factory=set)
    _offsets: dict[Path, int] = field(default_factory=dict)

    def poll(self) -> set[str]:
        """Applies new events and returns the ids whose state changed."""
        changed: set[str] = set()
        for path in sorted(self.run_dir.glob("progress-*.jsonl")):
            offset = self._offsets.get(path, 0)
            try:
                with path.open("rb") as f:
                    f.seek(offset)
                    data = f.read()
            except OSError:
                continue
            complete = data[:data.rfind(b"\n") + 1]  # leave a line still being written for next time
            self._offsets[path] = offset + len(complete)
            for line in complete.decode("utf-8", errors="replace").splitlines():
                try:
                    self._apply(json.loads(line), changed)
                except (ValueError, KeyError, TypeError):
                    continue
        return changed

    def _apply(self, event: dict, changed: set[str]) -> None:
        kind = event.get("event")
        if kind == "plan":
            self.planned += [Entry.of(e) for e in event["entries"]]
        elif kind == "registered":
            entry = Entry.of(event["entry"])
            self.registered.append(entry)
            changed.add(entry.id)
        elif kind == "done":
            self.jvms_done += 1
        elif kind == "containers":
            self.testcontainers = bool(self.testcontainers) or bool(event.get("testcontainers"))
            self.container_labels.update(event.get("labels") or [])
        elif kind in ("started", "finished", "skipped"):
            state = self.states.setdefault(event["id"], State())
            if kind == "started":
                state.status, state.started_at = "running", event["at"]
            elif kind == "finished":
                state.status, state.finished_at = STATUS.get(event.get("status"), "failed"), event["at"]
                state.message = event.get("message")
            else:
                state.status, state.message, state.finished_at = "skipped", event.get("message"), event["at"]
            changed.add(event["id"])

    def state(self, entry_id: str) -> State:
        return self.states.get(entry_id) or State()
