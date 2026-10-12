"""The testdash app: the state both screens show (tests, selection, run progress, services, output) and the
work behind them (listing tests, running them, following Docker)."""

from __future__ import annotations

import asyncio
import json
import os
import re
import shutil
import signal
import time
from collections import deque
from dataclasses import dataclass, field
from pathlib import Path

from rich.text import Text
from textual import work
from textual.app import App, SystemCommand
from textual.binding import Binding

import catalog
import gradle
from catalog import Entry
from progress import Progress, State
from runner import Runner
from screens import PickerScreen, RunScreen, StatusBar
from services import LogLine, Service, Watcher, port_holder
from tests_view import TestsView
from logformat import is_logged_problem, render
from views import ICON, DatabaseView, KafkaView, WireMockView, Window, clock, duration

STATE_FILE = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "testdash" / "state.json"
CORRELATION_SLACK = 1.0  # seconds either side of a test, for clocks that differ a little
PORT_TAKEN = re.compile(r"Bind for \S*:(\d+) failed: port is already allocated"
                        r"|listen tcp\d? \S*:(\d+): bind: address already in use")


@dataclass
class Settings:
    workdir: Path  # where the command runs
    build_root: Path | None  # the Gradle build, when there is one
    command: list[str] | None  # an explicit or configured command; None runs the picked Gradle task
    extra_args: list[str]
    task: str | None
    show_test_output: bool
    include_all_containers: bool
    run_dir: Path
    extra_logs: list[dict] = field(default_factory=list)  # {"name", "file" | "command"} from .testdash.json


class Testdash(App):
    TITLE = "testdash"
    CSS = """
    .title { height: 1; padding: 0 1; background: $primary-background; }
    .statusbar { height: 2; padding: 0 1; background: $primary-background-darken-1; text-wrap: nowrap;
                 text-overflow: ellipsis; }
    .hidden { display: none; }
    .view { height: 1fr; }
    .viewinfo { height: 1; padding: 0 1; background: $boost; color: $text-muted; }
    .hint { height: auto; padding: 0 1; color: $text-muted; }
    .findbar { height: 1; padding: 0 1; background: $boost; }
    .detail { width: 2fr; border-left: solid $primary-darken-2; padding: 0 1; }
    #picker-body { height: 1fr; }
    #picker-tests { width: 2fr; }
    #picker-side { width: 1fr; min-width: 36; padding: 1 2; border-left: solid $primary-darken-2; }
    #picker-side .label { color: $text-muted; }
    #task-select { margin-bottom: 1; }
    #run-button { width: 100%; margin: 1 0; }
    #picker-detail { margin-top: 1; }
    TestTree { width: 3fr; }
    DataTable { width: 3fr; }
    #run-main { height: 1fr; }
    #nav { width: 28; border-right: solid $primary-darken-2; }
    #nav ListItem > Horizontal { height: 1; }
    #nav .nav-name { width: 1fr; text-wrap: nowrap; text-overflow: ellipsis; }
    #nav .nav-icons { width: auto; padding-right: 1; }
    #run-body { width: 1fr; }
    #overview { height: 1fr; }
    #overview-top { height: 2fr; }
    #ov-logs { height: 3fr; border: round $primary-darken-2; }
    #ov-logs:focus-within { border: round $accent; }
    #ov-log-tabs { height: 1fr; }
    Panel { width: 1fr; height: 100%; border: round $primary-darken-2; padding: 0 1; overflow: hidden;
            text-wrap: nowrap; text-overflow: ellipsis; }
    Panel:focus { border: heavy $accent; }
    """
    BINDINGS = [Binding("ctrl+q", "quit", "Quit", show=False)]  # the run screen has q, as the picker types it

    def __init__(self, settings: Settings):
        super().__init__()
        self.settings = settings
        self.catalog = catalog.Catalog.load(settings.build_root) if settings.build_root else None
        self.test_task: catalog.TestTask | None = None
        self.entries: dict[str, Entry] = {}
        self.kids: dict[str | None, list[Entry]] = {}
        self.selectable: set[str] = set()
        self.selected: set[str] = set()
        self.last_selection: set[str] = set()
        self.runner = Runner()
        self.discovery = Runner()
        self.discovering = False
        self.progress: Progress | None = None
        self.in_run: set[str] | None = None
        self.output: deque[LogLine] = deque(maxlen=50_000)
        self.run_started: float | None = None
        self.run_finished: float | None = None
        self.exit_code: int | None = None
        scope = [d for d in (settings.workdir, settings.build_root) if d is not None]
        self.listening = False  # the run's test JVMs report progress (and their containers) to testdash
        self.watcher = Watcher(self, since=time.time(), scope_dirs=scope, include_all=settings.include_all_containers,
                               claims=self.container_claims,
                               run_active=lambda: self.run_started is not None and self.run_finished is None)
        self.extra_services: dict[str, Service] = {}
        self.log_simple = True  # every log shows a line's level and message only
        self.port_conflict: Text | None = None  # why the run could not start its containers
        self._ports_explained: set[str] = set()
        self.log_time = False

    def on_mount(self) -> None:
        self.install_screen(PickerScreen(), "picker")
        self.install_screen(RunScreen(), "run")
        self.choose_task(self.settings.task, quiet=True)
        self.push_screen("picker")
        if self.settings.build_root and (self.settings.command is None or self._command_is_gradle()):
            self.discover()
        if Watcher.available():
            self.run_worker(self.watcher.run(), group="docker", exclusive=True)
        else:
            self.notify("docker is not on PATH: no service views", severity="warning")
        for extra in self.settings.extra_logs:
            self._follow_extra(extra)
        self.set_interval(0.5, self._tick)

    @property
    def log_style(self) -> tuple[bool, bool]:
        return self.log_simple, self.log_time

    def _screen(self, name: str):
        try:
            screen = self.get_screen(name)
        except KeyError:  # quitting uninstalls the screens before every task has stopped
            return None
        return screen if screen.is_mounted else None

    # the tests

    def choose_task(self, wanted: str | None, quiet: bool = False) -> None:
        tasks = self.catalog.tasks if self.catalog else {}
        candidates = [wanted, self._task_in_command(), _saved_task(self.settings.build_root), ":test", ":app:test"]
        path = next((c for c in candidates if c in tasks), next(iter(tasks), None))
        changed = self.test_task is None or self.test_task.path != path
        self.test_task = tasks.get(path) if path else None
        if self.test_task and self.settings.build_root:
            _save_task(self.settings.build_root, self.test_task.path)
        self._index()
        self.selected &= self.selectable
        if changed and not quiet and self.test_task:
            self.notify(f"{self.test_task.path}: {len(self.selectable)} tests")
        self._refresh_screens(rebuild=True)

    def _index(self) -> None:
        """The tree both screens show: the task's tests, plus the tests the current run created."""
        task_entries = self.test_task.entries if self.test_task else []
        self.entries = {e.id: e for e in task_entries}
        self.selectable = {e.id for e in task_entries if e.is_leaf}
        if self.progress is not None:
            for entry in self.progress.registered:
                if entry.parent in self.entries:
                    self.entries.setdefault(entry.id, entry)
        self.kids = {}
        for entry in self.entries.values():
            self.kids.setdefault(entry.parent if entry.parent in self.entries else None, []).append(entry)

    def status(self, entry_id: str) -> str:
        if self.progress is None or self.in_run is None:
            return "idle"
        entry = self.entries.get(entry_id)
        if entry_id not in self.in_run and not (entry is not None and entry.parent in self.in_run):
            return "absent"
        return self.progress.state(entry_id).status

    def state(self, entry_id: str) -> State:
        return self.progress.state(entry_id) if self.progress else State()

    def leaves_under(self, entry_id: str) -> list[str]:
        leaves = []
        for kid in self.kids.get(entry_id, []):
            if kid.id in self.selectable:
                leaves.append(kid.id)
            elif kid.kind == "class":
                leaves += self.leaves_under(kid.id)
        return leaves

    def selection_for(self, entry_id: str) -> set[str]:
        """What runs one tree node again: a test, a class's tests, or for a parameterised test's invocation, which
        Gradle cannot filter on, the whole parameterised test."""
        entry = self.entries.get(entry_id)
        while entry is not None and entry.id not in self.selectable and entry.kind != "class":
            entry = self.entries.get(entry.parent)
        if entry is None:
            return set()
        return {entry.id} if entry.id in self.selectable else set(self.leaves_under(entry.id))

    def window_for(self, entry_id: str) -> Window | None:
        """When a test (or a class's tests) ran, with a second either side, for the views to show that time."""
        state = self.state(entry_id)
        if state.started_at is None:
            return None
        end = (state.finished_at or time.time() * 1000) / 1000
        name = self.entries[entry_id].name if entry_id in self.entries else entry_id
        return state.started_at / 1000 - CORRELATION_SLACK, end + CORRELATION_SLACK, f"during {name}"

    def descendants(self, entry_id: str) -> set[str]:
        found = set()
        for kid in self.kids.get(entry_id, []):
            found.add(kid.id)
            found |= self.descendants(kid.id)
        return found

    def failed_ids(self) -> set[str]:
        """The task's tests that failed last run; a failed parameterised case counts as its test."""
        failed = set()
        for entry_id, entry in self.entries.items():
            if self.status(entry_id) in ("failed", "aborted"):
                failed.add(entry_id if entry_id in self.selectable else entry.parent)
        return failed & self.selectable

    def selection_changed(self) -> None:
        picker = self._screen("picker")
        if picker:
            picker.refresh_summary()
            picker.show_detail()

    def no_task_reason(self) -> str:
        if self.settings.command and not self._command_is_gradle():
            return "\nNot a Gradle command, so there is no test list:\nRun runs the command."
        if self.discovering:
            return "\nListing the build's tests with Gradle…"
        if self.settings.build_root is None:
            return "\nNo Gradle build here and no command:\npass one after --"
        return "\nNo tests found (D lists them again)"

    def describe_test(self, entry_id: str, brief: bool = False) -> Text:
        entry = self.entries.get(entry_id)
        if entry is None:
            return Text("")
        text = Text(entry.class_name + "\n", style="bold")
        if entry.kind == "class":
            leaves = self.leaves_under(entry_id)
            text.append(f"{len(leaves)} tests, {sum(1 for l in leaves if l in self.selected)} selected\n")
            statuses = [self.status(leaf) for leaf in leaves]
            for status in ("passed", "failed", "skipped", "running", "pending"):
                if status in statuses:
                    icon, style = ICON[status]
                    text.append(f"{icon} {statuses.count(status)} {status}  ", style=style)
            return text
        text.append(entry.name + "\n")
        status, state = self.status(entry_id), self.state(entry_id)
        if status in ("idle", "absent"):
            return text.append("not run yet" if status == "idle" else "not in the last run", style="dim")
        icon, style = ICON[status]
        text.append(f"{icon} {status}", style=style)
        now = time.time() * 1000
        if state.started_at:
            text.append(f"  {duration(state.took(now))}  {clock(state.started_at / 1000)}\n", style="dim")
        if state.message:
            message = "\n".join(state.message.splitlines()[:6]) if brief else state.message
            text.append("\n" + message + "\n", style="red" if state.status != "skipped" else "yellow")
        if state.started_at and not brief:
            text.append("\n")
            text.append_text(self.correlate(state.started_at / 1000, (state.finished_at or now) / 1000))
        return text

    def correlate(self, start: float, end: float) -> Text:
        """What WireMock received and what the application warned about while a test ran."""
        start, end = start - CORRELATION_SLACK, end + CORRELATION_SLACK
        text = Text()
        run_screen = self._screen("run")
        for service in self.watcher.services.values():
            if service.kind == "wiremock" and run_screen:
                view = run_screen.service_view(service)
                if not isinstance(view, WireMockView):
                    continue
                events = view.events_between(start, end)
                unmatched = sum(1 for e in events if not e.matched)
                text.append(f"{service.key}: {len(events)} requests" + (f", {unmatched} unmatched" if unmatched else "")
                            + "\n", style="bold")
                for event in sorted(events, key=lambda e: e.at):
                    text.append(f"  {clock(event.at)} {event.status} {event.method} {event.url}",
                                style="" if event.matched else "red")
                    text.append("  no stub matched\n" if not event.matched else "\n", style="red")
            elif service.kind == "kafka" and run_screen:
                view = run_screen.service_view(service)
                if not isinstance(view, KafkaView):
                    continue
                messages = view.messages_between(start, end)
                text.append(f"{service.key}: {len(messages)} messages\n", style="bold")
                for message in messages[-15:]:
                    text.append(f"  {clock(message.at)} {message.topic}")
                    text.append(f"  {message.key or ''}\n", style="dim")
            elif isinstance(run_screen and run_screen.service_view(service), DatabaseView):
                queries = run_screen.service_view(service).queries_between(start, end)
                text.append(f"{service.key}: {len(queries)} queries\n", style="bold")
                for query in queries[-10:]:
                    text.append(f"  {clock(query.at)} ")
                    text.append(f"{query.target + ' ' if query.target else ''}{' '.join(query.text.split())[:100]}\n",
                                style="dim")
            elif service.is_app:
                problems = [l for l in list(service.lines) if start <= l.at <= end and is_logged_problem(l.text)]
                text.append(f"{service.key}: {len(problems)} warnings or errors\n", style="bold")
                for line in problems[:25]:
                    text.append_text(render(line.text, line.at, simple=self.log_simple, show_time=True))
                    text.append("\n")
        return text or Text("No services seen while it ran", style="dim")

    @work(group="discover", exclusive=True)
    async def discover(self) -> None:
        root = self.settings.build_root
        if root is None:
            return
        output_dir = self.settings.run_dir / "discovery"
        shutil.rmtree(output_dir, ignore_errors=True)
        output_dir.mkdir(parents=True)
        try:
            command = gradle.discover_command(root / "gradlew", output_dir)
        except gradle.ListenerUnavailable as e:
            self.notify(f"Cannot list tests: {e}", severity="error")
            return
        self.discovering = True
        self._refresh_screens()
        self._output("── listing tests with Gradle ──", style="bold")
        code = await self.discovery.run(command, str(root), self._output)
        self.discovering = False
        self.catalog.update_from_discovery(output_dir)
        self.choose_task(self.test_task.path if self.test_task else self.settings.task, quiet=True)
        if code != 0 and not self.catalog.tasks:
            self.notify("Listing the tests failed: see Output on the run screen", severity="error")

    def _task_in_command(self) -> str | None:
        if not self.settings.command or not self.catalog:
            return None
        for argument in self.settings.command[1:]:
            for path in self.catalog.tasks:
                if argument in (path, path.rsplit(":", 1)[-1]):
                    return path
        return None

    def _command_is_gradle(self) -> bool:
        return bool(self.settings.command) and gradle.is_gradle(self.settings.command)

    # running

    def base_command(self) -> list[str] | None:
        if self.settings.command:
            return self.settings.command
        if self.test_task and self.settings.build_root:
            return [str(self.settings.build_root / "gradlew"), self.test_task.path]
        return None

    def start_run(self, selection: set[str] | None = None) -> None:
        if self.runner.running:
            self.notify("Already running: stop it first (x, or ctrl+x in the picker)", severity="warning")
            return
        base = self.base_command()
        if base is None:
            self.notify("Nothing to run: no Gradle test task and no command", severity="warning")
            return
        selection = set(self.selected if selection is None else selection) & self.selectable
        self.last_selection = selection
        if self.discovering:
            self.discovery.stop()
        command = base + self.settings.extra_args
        if selection and self.test_task and gradle.is_gradle(command):
            command += catalog.gradle_filters(self.test_task, selection)
        shown = " ".join(command)
        self.listening = False
        if gradle.is_gradle(command):
            try:
                command = gradle.with_listener(command, self.settings.run_dir, not self.settings.show_test_output)
                self.listening = True
            except gradle.ListenerUnavailable as e:
                self.notify(f"Running without test progress: {e}", severity="warning")
        for stale in self.settings.run_dir.glob("progress-*.jsonl"):
            stale.unlink()
        self.run_started, self.run_finished, self.exit_code = time.time(), None, None
        self.port_conflict, self._ports_explained = None, set()
        self.progress = Progress(self.settings.run_dir)
        self.in_run = set()
        self._index()
        self.watcher.new_run(self.run_started)
        self.output.clear()
        self.switch_screen("run")
        run_screen = self._screen("run")
        if run_screen:
            run_screen.clear_output()
            for service in self.watcher.services.values():
                view = run_screen.service_view(service)
                if isinstance(view, (WireMockView, KafkaView, DatabaseView)):
                    view.clear()
            run_screen.show_view("overview")
        self._output(f"▶ {shown}", style="bold")
        self._refresh_screens(rebuild=True)
        self.run_worker(self._execute(command), group="run", exclusive=True)

    def container_claims(self) -> tuple[bool | None, set[str]]:
        """Whether the run's tests use Testcontainers (None until they say) and the labels of their containers."""
        if not self.listening:
            return False, set()
        if self.progress is None:
            return None, set()
        return self.progress.testcontainers, self.progress.container_labels

    def _check_ports(self, text: str) -> None:
        """Docker could not publish a port: say who has it, as it reads like a test failure otherwise."""
        if "already" not in text and "in use" not in text:
            return
        for match in PORT_TAKEN.finditer(text):
            port = match[1] or match[2]
            if port not in self._ports_explained:
                self._ports_explained.add(port)
                self.run_worker(self._explain_port(port), group="ports")

    async def _explain_port(self, port: str) -> None:
        holder = await port_holder(port)
        self.port_conflict = Text.assemble(
            ("✘ ", "red"), (f"Port {port} is taken", "bold red"), f" by {holder}" if holder else "",
            ". Another run is probably using it: wait for it to finish, or stop it, then run again.")
        self.notify(self.port_conflict.plain, title="The run could not start its containers", severity="error",
                    timeout=20)
        self._refresh_screens()

    async def _execute(self, command: list[str]) -> None:
        code = await self.runner.run(command, str(self.settings.workdir), self._output)
        self.exit_code, self.run_finished = code, time.time()
        self._tick()
        took = duration((self.run_finished - self.run_started) * 1000)
        summary = self.run_summary()
        if code == 0:
            self._output(f"passed in {took}", style="green")
            self.notify(f"Passed: {summary['passed']} tests in {took}")
        else:
            self._output(f"failed (exit {code}) in {took}", style="red")
            self.notify(f"Failed: {summary['failed']} of {summary['total']} tests failed (exit {code})", severity="error")

    def action_stop(self) -> None:
        if self.runner.running:
            self.runner.stop()
            self.notify("Stopping: x again kills it")
        elif self.discovering:
            self.discovery.stop()

    def _output(self, line: str, style: str = "") -> None:
        self._check_ports(line)
        codes = {"bold": "1", "green": "32", "red": "31"}
        entry = LogLine(time.time(), f"\x1b[{codes[style]}m{line}\x1b[0m" if style in codes else line)
        self.output.append(entry)
        run_screen = self._screen("run")
        if run_screen:
            run_screen.append_output([entry])

    def output_info(self) -> str:
        command = self.base_command()
        return f"{' '.join(command) if command else ''}   ({self.settings.workdir})"

    # what the screens summarise

    def run_leaves(self) -> list[Entry]:
        if self.progress is None:
            return []
        expanded = {e.parent for e in self.progress.registered}
        return [e for e in self.progress.planned if e.is_leaf and e.id not in expanded] + self.progress.registered

    def run_summary(self) -> dict:
        leaves = self.run_leaves()
        states = [(entry, self.state(entry.id)) for entry in leaves]
        count = lambda *statuses: sum(1 for _, s in states if s.status in statuses)  # noqa: E731
        finished = sorted((pair for pair in states if pair[1].finished_at), key=lambda p: p[1].finished_at, reverse=True)
        failed_first = [p for p in finished if p[1].status in ("failed", "aborted")] + \
                       [p for p in finished if p[1].status not in ("failed", "aborted")]
        return {
            "total": len(leaves), "passed": count("passed"), "failed": count("failed", "aborted"),
            "skipped": count("skipped"), "left": count("pending", "running"),
            "running": [p for p in states if p[1].status == "running"],
            "recent": failed_first,
        }

    def state_text(self) -> Text:
        if self.run_started is None:
            return Text("no run yet", style="dim")
        elapsed = duration(((self.run_finished or time.time()) - self.run_started) * 1000)
        if self.run_finished is None:
            return Text(f"● running {elapsed}", style="cyan")
        if self.exit_code == 0:
            return Text(f"✔ passed in {elapsed}", style="green")
        return Text(f"✘ failed in {elapsed} (exit {self.exit_code})", style="red")

    def activity(self) -> Text:
        """What a run is doing when no test is running: building, setting up a class, or finishing."""
        setting_up = [e for e in self.entries.values() if e.kind == "class" and self.state(e.id).status == "running"]
        if setting_up:
            return Text(f"setting up {setting_up[-1].short_class}", style="cyan")
        return Text("finishing…" if self.run_leaves() else "building…", style="cyan")

    def status_text(self) -> Text:
        """The status bar's two lines: the test running, then the counts and how the run is going."""
        if self.run_started is None:
            return Text("No run yet: select tests, then Enter twice to run them\n", style="dim")
        summary = self.run_summary()
        text = Text()
        if summary["running"]:
            entry, state = summary["running"][0]
            text.append("▶ ", style="cyan")
            text.append(entry.method or entry.name, style="bold cyan")
            if entry.method and entry.name != entry.method:
                text.append(f"  {entry.name}", style="cyan")
            text.append(f"  in {self.group_of(entry)}")
            text.append(f"  {duration(state.took(time.time() * 1000))}", style="dim")
            if len(summary["running"]) > 1:
                text.append(f"  +{len(summary['running']) - 1} more running", style="dim")
        elif self.port_conflict is not None:
            text.append_text(self.port_conflict)
        elif self.run_finished is None:
            text.append_text(self.activity())
        else:
            text.append("Run finished", style="dim")
        text.append("\n")
        for key, status in (("passed", "passed"), ("failed", "failed"), ("skipped", "skipped")):
            icon, style = ICON[status]
            text.append(f"{icon} {summary[key]} {key}   ", style=style if summary[key] else "dim")
        text.append(f"○ {summary['left']} left", style="" if summary["left"] else "dim")
        text.append(f" of {summary['total']}     ", style="dim")
        return text.append_text(self.state_text())

    def group_of(self, entry: Entry) -> str:
        """The classes a test sits in, outermost first, by their display names: Outer › Nested."""
        names = []
        parent = self.entries.get(entry.parent)
        while parent is not None:
            if parent.kind == "class":
                names.append(parent.name)
            parent = self.entries.get(parent.parent)
        return " › ".join(reversed(names)) or entry.short_class

    def title_line(self, place: str) -> Text:
        text = Text.assemble(("testdash ", "bold"), (f"{self.settings.workdir.name}  ", "dim"))
        if self.test_task and not self.settings.command:
            text.append(f"{self.test_task.path}  ")
        text.append_text(self.state_text())
        text.append(f"   {place}", style="bold")
        hint = "   Tab moves between sidebar and panels · 1-9 views · Esc pick tests" if place == "Overview" \
            else "   Esc overview"
        text.append(hint, style="dim")
        return text

    # services (the watcher calls these)

    def all_services(self) -> list[Service]:
        return list(self.watcher.services.values()) + list(self.extra_services.values())

    def service_added(self, service: Service) -> None:
        run_screen = self._screen("run")
        if run_screen:
            run_screen.add_service(service)

    def service_changed(self, service: Service) -> None:
        run_screen = self._screen("run")
        if run_screen:
            run_screen.service_changed(service)

    def service_lines(self, service: Service, lines: list[LogLine]) -> None:
        service.problems += sum(1 for line in lines if is_logged_problem(line.text))
        run_screen = self._screen("run")
        if run_screen:
            run_screen.append_lines(service, lines)

    def service_state(self, service: Service) -> str:
        return {"running": "running", "stopped": "stopped", "previous": "from the last run"}.get(service.state, "")

    def service_info(self, service: Service) -> str:
        role = "the application" if service.is_app else service.kind
        return f"{service.key}  {role}  {service.image}  {(service.container_id or '')[:12]}  {self.service_state(service)}"

    def _follow_extra(self, extra: dict) -> None:
        """A log from .testdash.json, a file or a command's output, shown like a container's."""
        service = Service(extra["name"], "file" if extra.get("file") else "command",
                          extra.get("file") or extra.get("command"))
        self.extra_services[service.key] = service
        self.service_added(service)
        self.run_worker(self._extra_lines(service, extra), group=f"extra-{service.key}")

    async def _extra_lines(self, service: Service, extra: dict) -> None:
        if extra.get("command"):
            process = await asyncio.create_subprocess_shell(
                extra["command"], cwd=self.settings.workdir, stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.STDOUT)
            try:
                while line := await process.stdout.readline():
                    self._extra_append(service, [line.decode("utf-8", errors="replace").rstrip("\n")])
            except asyncio.CancelledError:  # quitting: a `tail -f` or `kubectl logs -f` would outlive testdash
                process.kill()
                raise
            service.state = "stopped"
            self.service_changed(service)
            return
        path = Path(extra["file"])
        path = path if path.is_absolute() else self.settings.workdir / path
        inode, offset = None, 0
        while True:
            try:
                stat = path.stat()
                if stat.st_ino != inode or stat.st_size < offset:
                    inode, offset = stat.st_ino, 0
                if stat.st_size > offset:
                    with path.open("rb") as f:
                        f.seek(offset)
                        chunk = f.read(stat.st_size - offset)
                    offset += len(chunk)
                    self._extra_append(service, chunk.decode("utf-8", errors="replace").splitlines())
            except OSError:
                pass
            await asyncio.sleep(0.5)

    def _extra_append(self, service: Service, texts: list[str]) -> None:
        lines = [LogLine(time.time(), t) for t in texts]
        service.lines.extend(lines)
        self.service_lines(service, lines)

    # refreshing

    def _tick(self) -> None:
        if self.progress is not None:
            changed = self.progress.poll()
            planned = {e.id for e in self.progress.planned if e.is_leaf} | {e.id for e in self.progress.registered}
            if self.in_run is not None and planned - self.in_run:
                changed |= planned - self.in_run
                self.in_run |= planned
                self._index()
            for entry_id in changed:
                state = self.progress.states.get(entry_id)
                if state is not None and state.status in ("failed", "aborted") and state.message:
                    self._check_ports(state.message)
            if changed:
                for screen_name in ("picker", "run"):
                    screen = self._screen(screen_name)
                    if screen:
                        for view in screen.query(TestsView):
                            if screen_name == "run" and planned and not view.nodes:
                                view.rebuild()
                            view.apply(changed)
        self._refresh_screens()

    def _refresh_screens(self, rebuild: bool = False) -> None:
        picker = self._screen("picker")
        if picker:
            if rebuild:
                picker.refresh_all()
            else:
                picker.refresh_summary()
            picker.query_one(StatusBar).refresh_status()
        run_screen = self._screen("run")
        if run_screen:
            if rebuild:
                run_screen.query_one("#detail-tests", TestsView).rebuild()
            run_screen.refresh_all()

    def get_system_commands(self, screen):
        run_screen = self._screen("run")
        if run_screen:
            for number, (view_id, name) in enumerate(run_screen.views, start=1):
                yield SystemCommand(f"Show {name}", f"The {name} view ({number})",
                                    lambda v=view_id: (self.switch_screen("run"), run_screen.show_view(v)))
        yield SystemCommand("Pick tests", "The test picker", lambda: self.switch_screen("picker"))
        yield SystemCommand("Run selected tests", "Run the picker's selection", self.start_run)
        yield SystemCommand("Run failed tests", "Run the tests that failed last time",
                            lambda: self.start_run(self.failed_ids()))
        yield SystemCommand("Stop", "Stop the run (x)", self.action_stop)
        yield SystemCommand("List tests again", "Rediscover the build's tests", self.discover)
        yield from super().get_system_commands(screen)

    def action_quit(self) -> None:
        self.runner.stop()
        self.discovery.stop()
        self.exit()

    def on_unmount(self) -> None:
        for runner in (self.runner, self.discovery):
            if runner.running:
                try:
                    os.killpg(runner.process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        shutil.rmtree(self.settings.run_dir, ignore_errors=True)


def _saved_task(root: Path | None) -> str | None:
    try:
        return json.loads(STATE_FILE.read_text(encoding="utf-8")).get(str(root))
    except (OSError, ValueError):
        return None


def _save_task(root: Path, task: str) -> None:
    try:
        state = json.loads(STATE_FILE.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        state = {}
    state[str(root)] = task
    STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
    STATE_FILE.write_text(json.dumps(state), encoding="utf-8")
