"""testdash's two screens: the test picker, and the run screen with a sidebar of views, the first an overview.

    picker ──Enter on Run──▶ run screen: overview ⇄ tests, output, each service (sidebar, 1-9)
           ◀──────Esc───────            ◀──Esc── from any other view
"""

from __future__ import annotations

import re
import time

from rich.text import Text
from textual import events
from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.geometry import Offset
from textual.screen import Screen
from textual.widgets import (Button, ContentSwitcher, Footer, Label, ListItem, ListView, Select, Static, TabbedContent,
                             TabPane, Tree)

from services import Service
from tests_view import TestsView, TestTree
from databases import QUERY_KINDS
from views import ICON, DatabaseView, KafkaView, LogView, WireMockView, Window, clock, duration
from wiremock import ServeEvent

ID_SEGMENT = re.compile(r"\d")
VERSION_SEGMENT = re.compile(r"^v\d+$")


class StatusBar(Static):
    """The run in two lines: the test running and its class, then the counts so far."""

    def refresh_status(self) -> None:
        self.update(self.app.status_text())


PICKER_KEYS = Text("\n").join(Text.assemble((f"{key:<7}", "bold"), what) for key, what in (
    ("type", "filter the tests"),
    ("↑ ↓", "move"),
    ("← →", "collapse, expand"),
    ("space", "select"),
    ("^a", "select all or none"),
    ("^f", "select last run's failures"),
    ("^t", "test task"),
    ("Enter", "Run button, again to run"),
))


def _count(number: int, one: str, many: str | None = None) -> str:
    return f"{number} {one if number == 1 else many or one + 's'}"


class TaskSelect(Select[str]):
    BINDINGS = [Binding("escape", "screen.focus_tests", "Tests")]


class PickerScreen(Screen):
    """Typing filters the test tree, so this screen's keys are control keys."""

    BINDINGS = [
        Binding("ctrl+q", "app.quit", "Quit"),
        Binding("ctrl+f", "pick_failed", "Failed"),
        Binding("ctrl+t", "focus_task", "Task"),
        Binding("ctrl+x", "stop", "Stop run"),
        Binding("escape", "to_run", "Watch run"),
    ]
    task_options: list[tuple[str, str]] = []

    def compose(self) -> ComposeResult:
        yield Static("", id="picker-title", classes="title")
        with Horizontal(id="picker-body"):
            yield TestsView(picking=True, id="picker-tests")
            with Vertical(id="picker-side"):
                yield Static("Test task", classes="label")
                yield TaskSelect([], id="task-select", allow_blank=True, prompt="no test tasks yet")
                yield Static("", id="picker-summary")
                yield Button("▶ Run", id="run-button", variant="success")
                yield Static("", id="picker-hint", classes="hint")
                yield Static("", id="picker-detail")
        yield StatusBar(classes="statusbar")
        yield Footer()

    def on_mount(self) -> None:
        self.refresh_all()
        self.query_one(TestsView).focus_main()

    def on_screen_resume(self) -> None:
        self.refresh_all()
        self.call_after_refresh(self.tests.focus_main)

    @property
    def tests(self) -> TestsView:
        return self.query_one(TestsView)

    def refresh_all(self) -> None:
        app = self.app
        tasks = [(f"{path}  ({len(task.leaves())})", path) for path, task in (app.catalog.tasks if app.catalog else {}).items()]
        select = self.query_one("#task-select", Select)
        with select.prevent(Select.Changed):  # only a choice made in the dropdown is a Changed
            if tasks != self.task_options:  # setting them again would close the dropdown under the user
                self.task_options = tasks
                select.set_options(tasks)
            if app.test_task is not None and app.test_task.path in {p for _, p in tasks}:
                select.value = app.test_task.path
        self.tests.rebuild()
        self.refresh_summary()
        self.show_detail()

    def show_detail(self) -> None:
        node = self.tests.test_tree.cursor_node
        self.query_one("#picker-detail", Static).update(
            self.app.describe_test(node.data, brief=True) if node is not None and node.data else "")

    def refresh_summary(self) -> None:
        app = self.app
        title = Text.assemble(("testdash ", "bold"), (f"{app.settings.workdir.name}  ", "dim"), "pick the tests to run")
        if app.discovering:
            title.append("   listing tests…", style="dim")
        self.query_one("#picker-title", Static).update(title)
        button = self.query_one("#run-button", Button)
        summary = self.query_one("#picker-summary", Static)
        total = len(app.selectable)
        picked = len(app.selected & app.selectable)
        if app.test_task is None:
            summary.update(app.no_task_reason())
            button.label, button.disabled = "▶ Run", app.base_command() is None
        else:
            classes = {app.entries[i].class_name for i in app.selected if i in app.entries}
            summary.update(f"\n{picked} of {_count(total, 'test')} selected" + (
                f"\nin {_count(len(classes), 'class', 'classes')}" if picked else "\nnothing selected runs them all"))
            button.label = f"▶ Run {_count(picked, 'test')}" if picked else f"▶ Run all {_count(total, 'test')}"
            button.disabled = False
        if app.runner.running:
            button.label, button.disabled = "Running… Esc watches, ^x stops", True
        self.refresh_bindings()
        self.query_one("#picker-hint", Static).update(PICKER_KEYS)

    def on_test_tree_submit(self, message: TestTree.Submit) -> None:
        self.query_one("#run-button", Button).focus()

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "run-button":
            self.app.start_run()

    def on_select_changed(self, event: Select.Changed) -> None:
        if isinstance(event.value, str):
            self.app.choose_task(event.value)
        elif self.app.test_task is not None:  # the blank entry, there only for before discovery finds any task
            with event.select.prevent(Select.Changed):
                event.select.value = self.app.test_task.path
        self.call_after_refresh(self.tests.focus_main)  # a task is chosen to pick from its tests

    def on_tree_node_highlighted(self, event: Tree.NodeHighlighted[str]) -> None:
        if event.node.data:
            self.query_one("#picker-detail", Static).update(self.app.describe_test(event.node.data, brief=True))

    def action_focus_task(self) -> None:
        self.query_one("#task-select", Select).focus()

    def action_focus_tests(self) -> None:
        self.tests.focus_main()

    def action_pick_failed(self) -> None:
        failed = self.app.failed_ids()
        if not failed:
            self.app.notify("No failed tests in the last run")
            return
        self.app.selected = failed
        self.tests.relabel_all()
        self.app.selection_changed()

    def check_action(self, action: str, parameters: tuple) -> bool | None:
        if action == "stop":
            return self.app.runner.running
        if action == "to_run":
            return self.app.run_started is not None
        return True

    def action_stop(self) -> None:
        self.app.action_stop()

    def action_to_run(self) -> None:
        self.app.switch_screen("run")


class Panel(Static, can_focus=True):
    """A summary on the overview; Enter or a click opens its full view."""

    BINDINGS = [Binding("enter", "open", "Open")]

    def __init__(self, title: str, content, target, **kwargs):
        super().__init__("", **kwargs)
        self.panel_title = title
        self.content_for = content  # (height, width) -> Text
        self.target = target  # () -> the view Enter opens, or None

    def refresh_panel(self) -> None:
        self.border_title = self.panel_title
        self.update(self.content_for(max(1, self.size.height - 2), max(10, self.size.width - 4)))

    def action_open(self) -> None:
        if self.target():
            self.screen.show_view(self.target())

    def on_click(self) -> None:
        self.action_open()


class NavRow(Horizontal):
    """A sidebar item's row. Hovering shows what its icons mean beside the sidebar: ListItem stops hover events
    before they reach the screen, and a tooltip under the pointer would cover the items around it."""

    def on_enter(self, event: events.Enter) -> None:
        self.screen.show_nav_tip(self.parent)

    def on_leave(self, event: events.Leave) -> None:
        if not self.is_mouse_over:
            self.screen.hide_nav_tip(self.parent)


class RunScreen(Screen):
    DEFAULT_CSS = """
    RunScreen { layers: default nav-tip; }
    #nav-tip { layer: nav-tip; width: auto; height: 1; padding: 0 1; background: $panel; color: $text; display: none; }
    """
    BINDINGS = [
        Binding("escape", "back", "Back"),
        Binding("left_square_bracket", "log_tab(-1)", "Previous log", show=False),
        Binding("right_square_bracket", "log_tab(1)", "Switch log", key_display="[ ]"),
        Binding("p", "picker", "Pick tests"),
        Binding("r", "rerun", "Run again"),
        Binding("F", "run_failed", "Run failed"),
        Binding("x", "app.stop", "Stop"),
        Binding("colon", "app.command_palette", "Jump"),
        Binding("q", "app.quit", "Quit"),
        *[Binding(str(n), f"show_number({n})", show=False) for n in range(1, 10)],
    ]

    def __init__(self):
        super().__init__()
        self.views: list[tuple[str, str]] = []  # (view id, name), in sidebar order
        self.services: dict[str, Service] = {}  # view id -> service
        self.log_tabs: list[str] = []  # the overview's log tab ids, in the order shown
        self.nav_tips: dict[str, str] = {}  # sidebar item id -> what its icons mean, shown beside it on hover

    def compose(self) -> ComposeResult:
        yield Static("", id="run-title", classes="title")
        with Horizontal(id="run-main"):
            yield ListView(id="nav")
            with ContentSwitcher(id="run-body", initial="overview"):
                with Vertical(id="overview"):
                    with Horizontal(id="overview-top"):
                        yield Panel("Tests", self._tests_panel, lambda: "detail-tests", id="ov-tests")
                        yield Panel("WireMock", self._wiremock_panel, self._wiremock_view_id, id="ov-wiremock")
                        yield Panel("Kafka", self._kafka_panel, self._kafka_view_id, id="ov-kafka")
                    with Vertical(id="ov-logs"):
                        yield TabbedContent(id="ov-log-tabs")
                yield TestsView(picking=False, id="detail-tests")
                yield LogView(lambda: self.app.output, self.app.output_info, id="detail-output")
        yield StatusBar(id="run-status", classes="statusbar hidden")
        yield Footer()
        yield Static(id="nav-tip", markup=False)

    def on_mount(self) -> None:
        logs = self.query_one("#ov-logs")
        logs.border_title = "Logs"
        logs.border_subtitle = "[ ] switch   / filter   e errors only   v simple/full"
        self._add_view("overview", "Overview")
        self._add_view("detail-tests", "Tests")
        self._add_view("detail-output", "Output")
        self.query_one("#ov-log-tabs", TabbedContent).add_pane(TabPane(
            "Output", LogView(lambda: self.app.output, self.app.output_info, id="ov-log-output"),
            id="tab-output"))
        self.log_tabs.append("tab-output")
        for service in self.app.all_services():
            self.add_service(service)
        self.call_after_refresh(self.query_one("#ov-tests", Panel).focus)

    # the sidebar

    def _add_view(self, view_id: str, name: str, index: int | None = None) -> None:
        index = len(self.views) if index is None else index
        self.views.insert(index, (view_id, name))
        label, icons, hint = self._nav_label(view_id, name)
        item = ListItem(NavRow(Label(label, classes="nav-name"), Label(icons, classes="nav-icons")),
                        id=f"nav-{view_id}")
        self.nav_tips[item.id] = hint
        self.query_one("#nav", ListView).insert(index, [item])

    def _refresh_nav_item(self, view_id: str, name: str) -> None:
        labels = self.query(f"#nav-{view_id} Label")
        if len(labels) < 2:  # still mounting
            return
        label, icons, hint = self._nav_label(view_id, name)
        labels.first(Label).update(label)
        labels.last(Label).update(icons)
        self.nav_tips[f"nav-{view_id}"] = hint

    def _nav_label(self, view_id: str, name: str) -> tuple[Text, Text, str | None]:
        """A view's name, its icons (shown right-aligned), and a hint that says what they mean."""
        number = [v for v, _ in self.views].index(view_id) + 1
        label = Text(f"{number} " if number <= 9 else "  ", style="dim")
        icons = Text()
        if view_id == "detail-tests":
            label.append("Tests")
            summary = self.app.run_summary()
            parts = []
            for key, status in (("passed", "passed"), ("failed", "failed"), ("left", "pending")):
                if summary[key]:
                    icon, style = ICON[status]
                    icons.append(f" {icon}{summary[key]}", style=style)
                    parts.append(f"{summary[key]} {key}")
            return label, icons, ", ".join(parts) or None
        if view_id == "detail-output":
            label.append("Output")
            if self.app.runner.running or self.app.discovering:
                icons.append(" ▶", style="cyan")
                return label, icons, "Gradle is running"
            return label, icons, "Gradle's output"
        service = self.services.get(view_id)
        if service is None:
            return label.append(name), icons, None
        label.append("★ " if service.is_app else "", style="yellow")
        label.append(service.key, style="dim" if service.state == "previous" else "")
        hint = [{"running": "container running", "stopped": "container stopped"}.get(
            service.state, "from the previous run")]
        if service.problems:
            icons.append(f" ⚠{service.problems}", style="yellow")
            hint.append(f"{service.problems} warnings or errors logged")
        icons.append(" ●" if service.state == "running" else " ■", style="green" if service.state == "running" else "dim")
        return label, icons, ", ".join(hint)

    def show_nav_tip(self, item) -> None:
        tip = self.query_one("#nav-tip", Static)
        text = self.nav_tips.get(item.id or "")
        tip.display = bool(text)
        if text:
            tip.update(text)
            tip.absolute_offset = Offset(self.query_one("#nav").region.right, item.region.y)
            self._nav_tip_for = item

    def hide_nav_tip(self, item) -> None:
        if getattr(self, "_nav_tip_for", None) is item:
            self.query_one("#nav-tip").display = False

    def on_list_view_highlighted(self, event: ListView.Highlighted) -> None:
        if event.item is not None and event.item.id and self.query_one("#nav").has_focus:
            self.show_view(event.item.id.removeprefix("nav-"), focus=False)

    def on_list_view_selected(self, event: ListView.Selected) -> None:
        if event.item is not None and event.item.id:
            self.show_view(event.item.id.removeprefix("nav-"))

    # views

    def add_service(self, service: Service) -> None:
        slug = re.sub(r"[^A-Za-z0-9_-]", "-", service.key)
        view_id = f"detail-svc-{slug}"
        if view_id in self.services:
            return
        self.services[view_id] = service
        info = lambda: self.app.service_info(service)  # noqa: E731
        view = (WireMockView(service, info, id=view_id) if service.kind == "wiremock"
                else KafkaView(service, info, id=view_id) if service.kind == "kafka"
                else DatabaseView(service, info, id=view_id) if service.kind in QUERY_KINDS
                else LogView(lambda: service.lines, info, id=view_id))
        self.query_one("#run-body", ContentSwitcher).add_content(view, id=view_id)

        ids = [v for v, _ in self.views]
        apps = [i for i, v in enumerate(ids) if self.services.get(v) is not None and self.services[v].is_app]
        index = (apps[-1] + 1 if apps else ids.index("detail-tests") + 1) if service.is_app else ids.index("detail-output")
        self._add_view(view_id, service.key, index)

        # the overview's log tabs: applications first, the first one shown, then the others, then Output
        tabs = self.query_one("#ov-log-tabs", TabbedContent)
        pane = TabPane(("★ " if service.is_app else "") + service.key,
                       LogView(lambda: service.lines, info, id=f"ov-log-{slug}"), id=f"tab-{slug}")
        apps = sum(1 for s in self.services.values() if s.is_app and s is not service)
        position = apps if service.is_app else self.log_tabs.index("tab-output")
        tabs.add_pane(pane, before=self.log_tabs[position])
        self.log_tabs.insert(position, pane.id)
        if service.is_app and apps == 0:
            self.call_after_refresh(setattr, tabs, "active", pane.id)

    def service_view(self, service: Service):
        found = self.query(f"#detail-svc-{re.sub(r'[^A-Za-z0-9_-]', '-', service.key)}")
        return found.first() if found else None

    def _overview_log(self, service: Service) -> LogView | None:
        found = self.query(f"#ov-log-{re.sub(r'[^A-Za-z0-9_-]', '-', service.key)}")
        return found.first() if found else None

    def append_lines(self, service: Service, lines) -> None:
        for view in (self.service_view(service), self._overview_log(service)):
            if view is not None:
                view.append(lines)

    def service_changed(self, service: Service) -> None:
        for view in (self.service_view(service), self._overview_log(service)):
            if view is not None:
                if service.state == "previous":
                    view.clear()
                view.refresh_info()

    def append_output(self, lines) -> None:
        for view_id in ("#detail-output", "#ov-log-output"):
            found = self.query(view_id)
            if found:
                found.first().append(lines)

    def clear_output(self) -> None:
        for view_id in ("#detail-output", "#ov-log-output"):
            found = self.query(view_id)
            if found:
                found.first().clear()

    def show_view(self, view_id: str, focus: bool = True) -> None:
        ids = [v for v, _ in self.views]
        if view_id not in ids:
            return
        self.query_one("#run-body", ContentSwitcher).current = view_id
        nav = self.query_one("#nav", ListView)
        if nav.index != ids.index(view_id):
            nav.index = ids.index(view_id)
        self.query_one("#run-status", StatusBar).set_class(view_id == "overview", "hidden")
        self.refresh_bindings()  # the footer's keys depend on the view
        if focus:
            target = self.query_one("#ov-tests") if view_id == "overview" else self.query_one(f"#{view_id}")
            self.call_after_refresh(target.focus if view_id == "overview" else target.focus_main)
        self.refresh_all()

    def refresh_all(self) -> None:
        current = self.query_one("#run-body", ContentSwitcher).current
        if current == "overview":
            for panel in self.query(Panel):
                panel.refresh_panel()
            self.query_one("#ov-wiremock").display = self._wiremock_view_id() is not None
            self.query_one("#ov-kafka").display = self._kafka_view_id() is not None
        for view_id, name in self.views:
            self._refresh_nav_item(view_id, name)
        self.query_one("#run-status", StatusBar).refresh_status()
        name = dict(self.views).get(current, current)
        self.query_one("#run-title", Static).update(self.app.title_line(name))

    def check_action(self, action: str, parameters: tuple) -> bool | None:
        if action == "log_tab":
            return self.query_one("#run-body", ContentSwitcher).current == "overview"
        return True

    def action_back(self) -> None:
        if self.query_one("#run-body", ContentSwitcher).current != "overview":
            self.show_view("overview")
        else:
            self.app.switch_screen("picker")

    def action_log_tab(self, step: int) -> None:
        tabs = self.query_one("#ov-log-tabs", TabbedContent)
        ids = self.log_tabs
        tabs.active = ids[(ids.index(tabs.active) + step) % len(ids)] if tabs.active in ids else ids[0]

    def action_picker(self) -> None:
        self.app.switch_screen("picker")

    def action_rerun(self) -> None:
        self.app.start_run(self.app.last_selection)

    def action_run_failed(self) -> None:
        failed = self.app.failed_ids()
        if failed:
            self.app.start_run(failed)
        else:
            self.app.notify("No failed tests in the last run")

    def action_show_number(self, number: int) -> None:
        if number <= len(self.views):
            self.show_view(self.views[number - 1][0])

    # the overview's panels

    def _wiremock_view_id(self) -> str | None:
        return next((v for v, s in self.services.items() if s.kind == "wiremock"), None)

    def _kafka_view_id(self) -> str | None:
        return next((v for v, s in self.services.items() if s.kind == "kafka"), None)

    def _is(self, service: Service, kind: str) -> bool:
        if kind == "app":
            return service.is_app
        return service.kind in QUERY_KINDS if kind == "database" else service.kind == kind

    def has_service(self, kind: str) -> bool:
        return any(self._is(s, kind) for s in self.services.values())

    def show_during(self, kind: str, window: Window) -> None:
        """The application's log, WireMock's requests, Kafka's messages or the databases' queries from one test's
        time. Every database's view gets the time, so switching between them keeps it."""
        view_ids = [v for v, s in self.services.items() if self._is(s, kind)]
        if not view_ids:
            self.app.notify(f"No {'application' if kind == 'app' else kind} in this run", severity="warning")
            return
        for view_id in view_ids if kind == "database" else view_ids[:1]:
            self.query_one(f"#{view_id}").set_window(window)
        self.show_view(view_ids[0])

    def _tests_panel(self, height: int, width: int) -> Text:
        app = self.app
        summary = app.run_summary()
        text = Text()
        if app.port_conflict is not None:
            text.append_text(app.port_conflict)
            text.append("\n")
        text.append_text(app.state_text())
        text.append("   ")
        for key, status in (("passed", "passed"), ("failed", "failed"), ("skipped", "skipped"), ("left", "pending")):
            icon, style = ICON[status]
            text.append(f"{icon} {summary[key]}  ", style=style if summary[key] else "dim")
        total = summary["total"]
        done = summary["passed"] + summary["failed"] + summary["skipped"]
        bar = max(10, width - 12)
        filled = int(bar * done / total) if total else 0
        text.append("\n" + "█" * filled, style="red" if summary["failed"] else "green")
        text.append("░" * (bar - filled) + f" {done}/{total}\n", style="dim")
        lines = 2
        if summary["running"]:
            for entry, state in summary["running"]:
                text.append(f"▶ {entry.short_class} › {entry.name}", style="cyan")
                text.append(f"  {duration(state.took(time.time() * 1000))}\n", style="dim")
                lines += 1
        elif app.run_finished is None and app.run_started is not None:
            text.append_text(app.activity())
            text.append("\n")
            lines += 1
        for entry, state in summary["recent"]:
            if lines >= height:
                break
            icon, style = ICON[state.status]
            failed = state.status in ("failed", "aborted")
            text.append(f"{icon} ", style=style)
            text.append(f"{entry.short_class} › {entry.name}\n", style="red" if failed else "")
            lines += 1
            if failed and state.message and lines < height:
                text.append(f"    {state.message.splitlines()[0]}\n", style="dim red")
                lines += 1
        return text

    def _wiremock_panel(self, height: int, width: int) -> Text:
        view_id = self._wiremock_view_id()
        view = self.query(f"#{view_id}").first() if view_id else None
        if not isinstance(view, WireMockView):
            return Text("No WireMock in this run", style="dim")
        service = self.services[view_id]
        events = view.events
        unmatched = [e for e in events if not e.matched]
        text = Text.assemble(("● " if service.state == "running" else "■ ", "green" if service.state == "running" else "dim"),
                             (f"{len(events)} requests", "bold"), "   ",
                             (f"{len(unmatched)} unmatched", "bold red" if unmatched else "dim"), "   ",
                             (f"{len(view.stubs)} stubs", "dim"), "\n")
        if not events:
            return text.append("waiting for requests…", style="dim")
        room = max(1, height - 1)
        unmatched_rows = _endpoints(unmatched, by_latest=True)
        matched_rows = _endpoints([e for e in events if e.matched], by_latest=False)
        if unmatched_rows:
            text.append("Unmatched\n", style="bold red")
            room -= 1
            for (method, path), (count, status, _) in unmatched_rows[:max(1, room // 2 if matched_rows else room)]:
                text.append(f"  {status} {method} {path}", style="red")
                text.append(f"  ×{count}\n" if count > 1 else "\n", style="dim")
                room -= 1
        if matched_rows and room > 1:
            text.append("Most requested\n", style="bold")
            room -= 1
            for (method, path), (count, status, _) in matched_rows[:room]:
                text.append(f"  {status} {method} {path}")
                text.append(f"  ×{count}\n" if count > 1 else "\n", style="dim")
        return text


    def _kafka_panel(self, height: int, width: int) -> Text:
        view_id = self._kafka_view_id()
        view = self.query(f"#{view_id}").first() if view_id else None
        if not isinstance(view, KafkaView):
            return Text("No Kafka in this run", style="dim")
        running = self.services[view_id].state == "running"
        messages = view.messages
        counts: dict[str, int] = {}
        for message in messages:
            counts[message.topic] = counts.get(message.topic, 0) + 1
        lagging = {g.group for g in view.groups if g.lag}
        text = Text.assemble(("● " if running else "■ ", "green" if running else "dim"),
                             (f"{len(messages)} messages", "bold"), "   ", (f"{len(counts)} topics", "dim"))
        if lagging:
            text.append(f"   {len(lagging)} groups behind", style="bold red")
        text.append("\n")
        if view.client.error:
            return text.append(f"cannot read messages: {view.client.error}", style="red")
        if not messages:
            return text.append("waiting for messages…", style="dim")
        room = max(1, height - 1)
        busiest = sorted(counts.items(), key=lambda item: -item[1])[:max(1, room // 2 - 1)]
        text.append("Busiest topics\n", style="bold")
        room -= 1
        for topic, count in busiest:
            text.append(f"  {topic}")
            text.append(f"  ×{count}\n", style="dim")
            room -= 1
        if room > 1:
            text.append("Latest\n", style="bold")
            room -= 1
            for message in messages[-room:]:
                text.append(f"  {clock(message.at)} ", style="dim")
                text.append(message.topic)
                text.append(f"  {' '.join((message.value or '').split())[:width]}\n", style="dim")
        return text


def _endpoints(events: list[ServeEvent], by_latest: bool) -> list[tuple[tuple[str, str], tuple[int, int | None, float]]]:
    """Requests grouped by method and path, ids in the path as {id}: (count, last status, last time)."""
    groups: dict[tuple[str, str], tuple[int, int | None, float]] = {}
    for event in events:
        key = (event.method, _endpoint(event.url))
        count, status, latest = groups.get(key, (0, None, 0.0))
        if event.at >= latest:
            status, latest = event.status, event.at
        groups[key] = (count + 1, status, latest)
    order = (lambda item: -item[1][2]) if by_latest else (lambda item: (-item[1][0], -item[1][2]))
    return sorted(groups.items(), key=order)


def _endpoint(url: str) -> str:
    path = url.split("?", 1)[0]
    return "/".join("{id}" if ID_SEGMENT.search(part) and not VERSION_SEGMENT.match(part) else part
                    for part in path.split("/"))
