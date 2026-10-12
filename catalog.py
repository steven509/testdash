"""A build's test tasks and their tests, as JUnit Platform discovery reports them (see gradle.py), cached so
the picker opens at once and refreshes in the background.

Entries form the plan's tree: classes (nested classes under their outer class), tests, and test templates such
as parameterised tests. Tests and templates are what a user picks; a selection becomes Gradle --tests filters.
"""

from __future__ import annotations

import hashlib
import json
import os
from dataclasses import dataclass, field
from pathlib import Path

CACHE = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "testdash" / "catalog"


@dataclass
class Entry:
    id: str
    parent: str | None
    kind: str  # class | test | template
    class_name: str  # binary name, so a nested class is Outer$Inner, as --tests matches it
    method: str | None
    name: str  # display name

    @classmethod
    def of(cls, raw: dict) -> "Entry":
        return cls(raw["id"], raw.get("parent"), raw.get("kind", "test"), raw.get("className") or "",
                   raw.get("methodName"), _display(raw.get("name") or raw["id"], raw.get("kind")))

    @property
    def is_leaf(self) -> bool:
        return self.kind in ("test", "template")

    @property
    def short_class(self) -> str:
        return self.class_name.rsplit(".", 1)[-1].replace("$", ".")


@dataclass
class TestTask:
    path: str  # e.g. ":app:blackBoxTest"
    entries: list[Entry] = field(default_factory=list)

    def children(self) -> dict[str | None, list[Entry]]:
        ids = {e.id for e in self.entries}
        tree: dict[str | None, list[Entry]] = {}
        for entry in self.entries:
            tree.setdefault(entry.parent if entry.parent in ids else None, []).append(entry)
        return tree

    def leaves(self) -> list[Entry]:
        return [e for e in self.entries if e.is_leaf]


class Catalog:
    def __init__(self, root: Path):
        self.root = root
        self.tasks: dict[str, TestTask] = {}

    @classmethod
    def load(cls, root: Path) -> "Catalog":
        catalog = cls(root)
        try:
            raw = json.loads(catalog._file().read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return catalog
        for path, entries in raw.get("tasks", {}).items():
            catalog.tasks[path] = TestTask(path, [Entry.of(e) for e in entries])
        return catalog

    def update_from_discovery(self, output_dir: Path) -> None:
        """Replaces every task with what a discovery run wrote: one <task>.json per Test task."""
        discovered = {}
        for path in sorted(output_dir.glob("*.json")):
            try:
                raw = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, ValueError):
                continue
            entries = [Entry.of(e) for e in raw.get("entries", [])]
            if any(e.is_leaf for e in entries):
                discovered[raw["task"]] = TestTask(raw["task"], entries)
        if discovered:
            self.tasks = dict(sorted(discovered.items()))
            self._save()

    def _save(self) -> None:
        CACHE.mkdir(parents=True, exist_ok=True)
        raw = {"tasks": {path: [_raw(e) for e in task.entries] for path, task in self.tasks.items()}}
        self._file().write_text(json.dumps(raw), encoding="utf-8")

    def _file(self) -> Path:
        return CACHE / f"{hashlib.sha1(str(self.root.resolve()).encode()).hexdigest()[:16]}.json"


def gradle_filters(task: TestTask, selected: set[str]) -> list[str]:
    """`--tests` arguments for the selected leaf ids, naming a whole class where all of it is selected."""
    tree = task.children()
    filters: list[str] = []

    def leaves_under(entry: Entry) -> list[Entry]:
        if entry.is_leaf:
            return [entry]
        return [leaf for child in tree.get(entry.id, []) for leaf in leaves_under(child)]

    def visit(entry: Entry) -> None:
        under = leaves_under(entry)
        picked = [leaf for leaf in under if leaf.id in selected]
        if not picked:
            return
        if entry.kind == "class" and len(picked) == len(under):
            filters.extend(["--tests", entry.class_name, "--tests", f"{entry.class_name}$*"])  # and its @Nested classes
        elif entry.is_leaf:
            filters.extend(["--tests", f"{entry.class_name}.{entry.method}"])
        else:
            for child in tree.get(entry.id, []):
                visit(child)

    for top in tree.get(None, []):
        visit(top)
    return filters


def build_root(start: Path) -> Path | None:
    """The nearest directory up from start with a Gradle wrapper."""
    for directory in [start, *start.parents]:
        if (directory / "gradlew").is_file():
            return directory
    return None


def _display(name: str, kind: str | None) -> str:
    """`stores values()` reads `stores values`; a template `each(String)` reads `each`."""
    if name.endswith("()"):
        return name[:-2]
    if kind == "template" and name.endswith(")") and "(" in name:
        return name[:name.rindex("(")]
    return name


def _raw(entry: Entry) -> dict:
    return {"id": entry.id, "parent": entry.parent, "kind": entry.kind, "className": entry.class_name,
            "methodName": entry.method, "name": entry.name}
