"""Full views of a log and of WireMock, and the helpers every view uses to show times, durations and logs."""

from __future__ import annotations

import asyncio
import json
import re
import time
from collections.abc import Callable, Iterable

from rich.text import Text
from textual import work
from textual.actions import SkipAction
from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.widgets import ContentSwitcher, DataTable, Input, RichLog, Static, TabbedContent, TabPane

from databases import Query, query_log
from kafka import GroupLag, Kafka, KafkaMessage
from logformat import is_problem, render
from services import LogLine, Service
from wiremock import ServeEvent, WireMock, stub_label

Window = tuple[float, float, str]  # start and end, epoch seconds, and what they are: "during <test>"

ICON = {"pending": ("○", "dim"), "running": ("▶", "cyan"), "passed": ("✔", "green"), "failed": ("✘", "red"),
        "aborted": ("⚠", "yellow"), "skipped": ("↷", "yellow"), "absent": ("·", "dim"), "idle": ("·", "dim")}


def clock(at: float) -> str:
    return time.strftime("%H:%M:%S", time.localtime(at)) if at else "        "


def duration(millis: float | None) -> str:
    if millis is None:
        return ""
    seconds = millis / 1000
    if seconds < 10:
        return f"{seconds:.1f}s"
    if seconds < 60:
        return f"{seconds:.0f}s"
    return f"{int(seconds // 60)}:{int(seconds % 60):02d}"


class LogView(Vertical):
    """A scrolling log with a regex filter, and follow and errors-only toggles. Lines come from a buffer, so a
    filter applies to everything kept, including what arrived before it was typed, and matches the whole line
    even when the simplified view hides part of it. Simplified or full lines and the time column are the app's
    settings, so every log shows them alike."""

    BINDINGS = [
        Binding("slash", "filter", "Filter"),
        Binding("e", "errors", "Errors only"),
        Binding("f", "follow", "Follow"),
        Binding("w", "wrap", "Wrap"),
        Binding("v", "simple", "Simple/full"),
        Binding("t", "time", "Time"),
        Binding("escape", "clear_filter", "Clear filter", show=False),
    ]

    def __init__(self, source: Callable[[], Iterable[LogLine]], info: Callable[[], str], *, id: str | None = None):
        super().__init__(id=id, classes="view")
        self.source = source
        self.info = info
        self.pattern: re.Pattern | None = None
        self.errors_only = False
        self.window: Window | None = None
        self.style_shown: tuple[bool, bool] | None = None

    def compose(self) -> ComposeResult:
        yield Static(self.info(), classes="viewinfo", markup=False)
        yield Input(placeholder="filter (regex), Enter keeps it, Esc clears it", classes="filter hidden", compact=True)
        yield RichLog(max_lines=20_000, markup=False, highlight=False, wrap=False, auto_scroll=True)

    def on_mount(self) -> None:
        self.rerender()

    def on_show(self) -> None:
        if self.style_shown != self.app.log_style:  # toggled in another log while this one was hidden
            self.rerender()

    @property
    def richlog(self) -> RichLog:
        return self.query_one(RichLog)

    def focus_main(self) -> None:
        self.richlog.focus()

    def append(self, lines: Iterable[LogLine]) -> None:
        if not self.is_mounted:
            return
        richlog = self.richlog
        for line in lines:
            if self._shown(line):
                richlog.write(self._line_text(line))

    def clear(self) -> None:
        if self.is_mounted:
            self.richlog.clear()

    def refresh_info(self) -> None:
        if self.is_mounted:
            self.query_one(".viewinfo", Static).update(self.info() + self._modes())

    def rerender(self) -> None:
        richlog = self.richlog
        richlog.clear()
        for line in list(self.source())[-20_000:]:
            if self._shown(line):
                richlog.write(self._line_text(line))
        self.style_shown = self.app.log_style
        self.refresh_info()

    def _line_text(self, line: LogLine) -> Text:
        simple, show_time = self.app.log_style
        return render(line.text, line.at, simple=simple, show_time=show_time)

    def _shown(self, line: LogLine) -> bool:
        if self.window and not self.window[0] <= line.at <= self.window[1]:
            return False
        if self.errors_only and not is_problem(line.text):
            return False
        return self.pattern is None or bool(self.pattern.search(line.text))

    def set_window(self, window: Window | None) -> None:
        self.window = window
        self.rerender()
        if window:
            self.richlog.scroll_home(animate=False)

    def _modes(self) -> str:
        modes = []
        if self.window:
            modes.append(self.window[2])
        if self.pattern:
            modes.append(f"filter /{self.pattern.pattern}/")
        if self.errors_only:
            modes.append("errors only")
        if not self.app.log_simple:
            modes.append("full lines")
        if not self.richlog.auto_scroll:
            modes.append("paused, f follows")
        return ("   [" + " · ".join(modes) + "]") if modes else ""

    def action_filter(self) -> None:
        field = self.query_one(Input)
        field.remove_class("hidden")
        field.focus()

    def action_clear_filter(self) -> None:
        field = self.query_one(Input)
        if field.has_class("hidden") and self.pattern is None and self.window is None:
            raise SkipAction()  # let Escape go back
        field.value = ""
        field.add_class("hidden")
        self.pattern = self.window = None
        self.rerender()
        self.richlog.focus()

    def on_input_changed(self, event: Input.Changed) -> None:
        event.stop()
        try:
            self.pattern = re.compile(event.value, re.IGNORECASE) if event.value else None
        except re.error:
            return
        self.rerender()

    def on_input_submitted(self, event: Input.Submitted) -> None:
        event.stop()
        if not event.value:
            event.input.add_class("hidden")
        self.richlog.focus()

    def action_follow(self) -> None:
        self.richlog.auto_scroll = not self.richlog.auto_scroll
        if self.richlog.auto_scroll:
            self.richlog.scroll_end(animate=False)
        self.refresh_info()

    def action_errors(self) -> None:
        self.errors_only = not self.errors_only
        self.rerender()

    def action_simple(self) -> None:
        self.app.log_simple = not self.app.log_simple
        self.rerender()

    def action_time(self) -> None:
        self.app.log_time = not self.app.log_time
        self.rerender()

    def action_wrap(self) -> None:
        self.richlog.wrap = not self.richlog.wrap
        self.rerender()


class WireMockView(Vertical):
    """WireMock through its admin API: every request and the stub that answered it, the closest stubs for the
    requests no stub matched, the stubs themselves, and the container's log."""

    BINDINGS = [
        Binding("left", "tab(-1)", "← Tab", priority=True),
        Binding("right", "tab(1)", "Tab →", priority=True),
        Binding("u", "unmatched_only", "Unmatched only"),
        Binding("escape", "clear_window", "All requests"),
    ]
    TABS = ("requests", "near-misses", "stubs", "log")
    HINT = "←/→ switch tab   ↑/↓ choose a row, its details show on the right   u unmatched only   Esc back"

    def __init__(self, service: Service, info: Callable[[], str], id: str | None = None):
        super().__init__(id=id, classes="view")
        self.service = service
        self.client = WireMock(service)
        self.info = info
        self.events: list[ServeEvent] = []
        self.near_misses: list[dict] = []
        self.stubs: list[dict] = []
        self.unmatched_only = False
        self.window: Window | None = None
        self._details: dict[str, dict] = {}

    def compose(self) -> ComposeResult:
        yield Static(self.info(), classes="viewinfo", markup=False)
        yield Static(self.HINT, classes="hint")
        with TabbedContent(initial="requests"):
            with TabPane("Requests", id="requests"):
                with Horizontal():
                    yield DataTable(id="wm-requests", cursor_type="row", zebra_stripes=True)
                    yield RichLog(id="wm-requests-detail", classes="detail", wrap=True, markup=False, auto_scroll=False)
            with TabPane("Unmatched", id="near-misses"):
                with Horizontal():
                    yield DataTable(id="wm-near-misses", cursor_type="row", zebra_stripes=True)
                    yield RichLog(id="wm-near-misses-detail", classes="detail", wrap=True, markup=False,
                                  auto_scroll=False)
            with TabPane("Stubs", id="stubs"):
                with Horizontal():
                    yield DataTable(id="wm-stubs", cursor_type="row", zebra_stripes=True)
                    yield RichLog(id="wm-stubs-detail", classes="detail", wrap=True, markup=False, auto_scroll=False)
            with TabPane("Log", id="log"):
                yield LogView(lambda: self.service.lines, lambda: "", id="wm-log")

    def on_mount(self) -> None:
        self.query_one("#wm-requests", DataTable).add_columns("Time", "Status", "Stub", "Method", "URL")
        self.query_one("#wm-near-misses", DataTable).add_columns("Time", "Method", "URL", "Closest stub")
        self.query_one("#wm-stubs", DataTable).add_columns("Stub", "Method", "URL", "Status", "Priority")
        self._fill_requests()
        self._fill_near_misses()
        self._fill_stubs()
        self.poll()

    @property
    def log_view(self) -> LogView:
        return self.query_one("#wm-log", LogView)

    def focus_main(self) -> None:
        active = self.query_one(TabbedContent).active
        if active == "log":
            self.log_view.focus_main()
        else:
            self.query_one(f"#wm-{active}", DataTable).focus()

    def summary(self) -> str:
        unmatched = sum(1 for e in self.events if not e.matched)
        return f"{len(self.events)} requests, {unmatched} unmatched, {len(self.stubs)} stubs"

    def refresh_info(self) -> None:
        if self.is_mounted:
            modes = [m for m in (self.window and self.window[2], self.unmatched_only and "unmatched only") if m]
            mode = f"   [{' · '.join(modes)}]" if modes else ""
            self.query_one(".viewinfo", Static).update(f"{self.info()}   {self.summary()}{mode}")

    def set_window(self, window: Window | None) -> None:
        self.window = window
        self._fill_requests()
        self._fill_near_misses()
        self.refresh_info()
        self.refresh_bindings()
        if window:
            self.query_one(TabbedContent).active = "requests"

    def _in_window(self, event: ServeEvent) -> bool:
        return self.window is None or self.window[0] <= event.at <= self.window[1]

    def check_action(self, action: str, parameters: tuple) -> bool | None:
        if action == "clear_window":
            return self.window is not None
        return True

    def action_clear_window(self) -> None:
        self.set_window(None)

    def clear(self) -> None:
        self.events, self.near_misses, self._details = [], [], {}
        if self.is_mounted:
            self._fill_requests()
            self._fill_near_misses()
            for detail in self.query(".detail").results(RichLog):
                detail.clear()
            self.log_view.clear()

    def append(self, lines: Iterable[LogLine]) -> None:
        if self.is_mounted:
            self.log_view.append(lines)

    @work(exclusive=True, group="wiremock-poll")
    async def poll(self) -> None:
        cycle = 0
        while True:
            if self.service.state == "running":
                events = await self.client.requests()
                new_unmatched = False
                if events and [e.id for e in events] != [e.id for e in self.events]:
                    known = {e.id for e in self.events}
                    new_unmatched = any(not e.matched and e.id not in known for e in events)
                    self.events = events
                    self._fill_requests()
                    self._fill_near_misses()
                if new_unmatched or cycle % 3 == 0:  # straight away: a short run's containers soon stop
                    near_misses = await self.client.near_misses()
                    if near_misses != self.near_misses:
                        self.near_misses = near_misses
                        self._fill_near_misses()
                if cycle % 3 == 0:
                    stubs = await self.client.mappings()
                    if stubs and stubs != self.stubs:
                        self.stubs = stubs
                        self._fill_stubs()
                self.refresh_info()
            cycle += 1
            await asyncio.sleep(1.5)

    def _fill_requests(self) -> None:
        table = self.query_one("#wm-requests", DataTable)
        cursor = table.cursor_row
        table.clear()
        for event in self.events:
            if (self.unmatched_only and event.matched) or not self._in_window(event):
                continue
            status = Text(str(event.status or ""), style="green" if (event.status or 500) < 400 else "red")
            stub = Text("✔", style="green") if event.matched else Text("✘ none", style="bold red")
            table.add_row(clock(event.at), status, stub, event.method, event.url, key=f"r:{event.id}")
            self._details[f"r:{event.id}"] = event.raw
        if table.row_count and cursor is not None:
            table.move_cursor(row=min(cursor, table.row_count - 1))

    def _fill_near_misses(self) -> None:
        """Every request no stub matched, with the closest stub when WireMock reports one as a near miss."""
        table = self.query_one("#wm-near-misses", DataTable)
        table.clear()
        closest: dict[tuple, dict] = {}
        for miss in self.near_misses:
            request = miss.get("request") or {}
            key = (request.get("method"), request.get("url"), request.get("loggedDate"))
            distance = (miss.get("matchResult") or {}).get("distance", 1)
            if key not in closest or distance < (closest[key].get("matchResult") or {}).get("distance", 1):
                closest[key] = miss
        for event in self.events:
            if event.matched or not self._in_window(event):
                continue
            request = event.raw.get("request") or {}
            miss = closest.get((request.get("method"), request.get("url"), request.get("loggedDate")))
            stub = Text(stub_label(miss.get("stubMapping")) if miss else "no stub is close", style="" if miss else "dim")
            table.add_row(clock(event.at), event.method, event.url, stub, key=f"n:{event.id}")
            self._details[f"n:{event.id}"] = {**event.raw, "nearMiss": miss} if miss else event.raw

    def _fill_stubs(self) -> None:
        table = self.query_one("#wm-stubs", DataTable)
        table.clear()
        for stub in self.stubs:
            request, response = stub.get("request") or {}, stub.get("response") or {}
            url = next((request[k] for k in ("url", "urlPath", "urlPattern", "urlPathPattern", "urlPathTemplate")
                        if request.get(k)), "")
            table.add_row(stub.get("name") or stub.get("id", "")[:8], request.get("method", "ANY"), url,
                          str(response.get("status", "")), str(stub.get("priority", "")), key=f"s:{stub.get('id')}")
            self._details[f"s:{stub.get('id')}"] = stub

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        detail = self.query_one(f"#{event.data_table.id}-detail", RichLog)
        detail.clear()
        raw = self._details.get(event.row_key.value if event.row_key else "")
        if raw is not None:
            detail.write(_describe(raw))

    def action_tab(self, step: int) -> None:
        tabs = self.query_one(TabbedContent)
        tabs.active = self.TABS[(self.TABS.index(tabs.active) + step) % len(self.TABS)]
        self.focus_main()

    def action_unmatched_only(self) -> None:
        self.unmatched_only = not self.unmatched_only
        self._fill_requests()
        self.refresh_info()

    def events_between(self, start: float, end: float) -> list[ServeEvent]:
        return [e for e in self.events if start <= e.at <= end]


class DatabaseView(Vertical):
    """A database's queries, as it records them itself (see databases.py), and its log."""

    BINDINGS = [
        Binding("left", "tab(-1)", "← Tab", priority=True),
        Binding("right", "tab(1)", "Tab →", priority=True),
        Binding("n", "noise", "Session chatter"),
        Binding("escape", "clear_window", "All queries"),
    ]
    TABS = ("queries", "log")
    HINT = "←/→ switch tab   ↑/↓ choose a query, all of it shows on the right"
    MAX_QUERIES = 20_000

    def __init__(self, service: Service, info: Callable[[], str], id: str | None = None):
        super().__init__(id=id, classes="view")
        self.service = service
        self.client = query_log(service)
        self.info = info
        self.queries: list[Query] = []
        self.window: Window | None = None
        self.show_noise = False
        self._by_key: dict[str, Query] = {}

    def compose(self) -> ComposeResult:
        yield Static(self.info(), classes="viewinfo", markup=False)
        yield Static(self.HINT + ("" if self.service.kind == "mongodb" else
                                  "   n session chatter (SET, SHOW, connects)"), classes="hint")
        with TabbedContent(initial="queries"):
            with TabPane("Queries", id="queries"):
                with Horizontal():
                    yield DataTable(id="db-queries", cursor_type="row", zebra_stripes=True)
                    yield RichLog(id="db-queries-detail", classes="detail", wrap=True, markup=False, auto_scroll=False)
            with TabPane("Log", id="log"):
                yield LogView(lambda: self.service.lines, lambda: "", id="db-log")

    def on_mount(self) -> None:
        table = self.query_one("#db-queries", DataTable)
        if self.service.kind == "mongodb":
            table.add_columns("Time", "Op", "Collection", "ms", "Command")
        else:
            table.add_columns("Time", "Conn", "Kind", "Query")
        if self.client is not None:
            self.follow()

    @property
    def log_view(self) -> LogView:
        return self.query_one("#db-log", LogView)

    def focus_main(self) -> None:
        if self.query_one(TabbedContent).active == "log":
            self.log_view.focus_main()
        else:
            self.query_one("#db-queries", DataTable).focus()

    def summary(self) -> str:
        if self.client and self.client.error:
            return f"cannot read queries: {self.client.error}"
        chatter = sum(1 for q in self.queries if q.noise)
        return f"{len(self.queries) - chatter} queries" + (f", {chatter} session chatter" if chatter else "")

    def refresh_info(self) -> None:
        if self.is_mounted:
            modes = [m for m in (self.window and self.window[2], self.show_noise and "with session chatter") if m]
            mode = f"   [{' · '.join(modes)}]" if modes else ""
            self.query_one(".viewinfo", Static).update(f"{self.info()}   {self.summary()}{mode}")

    def clear(self) -> None:
        self.queries, self._by_key = [], {}
        if self.is_mounted:
            self._fill_queries()
            self.query_one("#db-queries-detail", RichLog).clear()
            self.log_view.clear()

    def append(self, lines: Iterable[LogLine]) -> None:
        if self.is_mounted:
            self.log_view.append(lines)

    @work(group="db-queries")
    async def follow(self) -> None:
        await self.client.follow(self._received)

    def _received(self, batch: list[Query]) -> None:
        self.queries.extend(batch)
        trimmed = len(self.queries) > self.MAX_QUERIES
        if trimmed:
            del self.queries[:len(self.queries) - self.MAX_QUERIES]
        if self.is_mounted:
            if trimmed:
                self._fill_queries()
            else:
                self._add_rows(batch)
            self.refresh_info()

    def _shown(self, query: Query) -> bool:
        if query.noise and not self.show_noise:
            return False
        return self.window is None or self.window[0] <= query.at <= self.window[1]

    def _fill_queries(self) -> None:
        self.query_one("#db-queries", DataTable).clear()
        self._by_key = {}
        self._add_rows(self.queries)

    def _add_rows(self, queries: list[Query]) -> None:
        table = self.query_one("#db-queries", DataTable)
        following = table.row_count == 0 or table.cursor_row >= table.row_count - 1
        for query in queries:
            if not self._shown(query):
                continue
            key = str(len(self._by_key))
            self._by_key[key] = query
            style = "dim" if query.noise else ""
            if query.target:
                millis = "" if query.millis is None else str(query.millis)
                table.add_row(clock(query.at), query.kind, query.target, millis, _preview(query.text, 90), key=key)
            else:
                table.add_row(Text(clock(query.at), style), Text(query.connection, "dim"), Text(query.kind, style),
                              Text(_preview(query.text, 110).plain, style), key=key)
        if table.row_count and following:
            table.move_cursor(row=table.row_count - 1)

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        detail = self.query_one("#db-queries-detail", RichLog)
        detail.clear()
        query = self._by_key.get(event.row_key.value if event.row_key else "")
        if query is not None:
            detail.write(_describe_query(query))

    def set_window(self, window: Window | None) -> None:
        self.window = window
        self._fill_queries()
        self.refresh_info()
        self.refresh_bindings()
        tabs = self.query_one(TabbedContent)
        if window and tabs.active != "queries":
            tabs.active = "queries"

    def check_action(self, action: str, parameters: tuple) -> bool | None:
        if action == "clear_window":
            return self.window is not None
        if action == "noise":
            return self.service.kind != "mongodb"
        return True

    def action_clear_window(self) -> None:
        self.set_window(None)

    def action_noise(self) -> None:
        self.show_noise = not self.show_noise
        self._fill_queries()
        self.refresh_info()

    def action_tab(self, step: int) -> None:
        tabs = self.query_one(TabbedContent)
        tabs.active = self.TABS[(self.TABS.index(tabs.active) + step) % len(self.TABS)]
        self.focus_main()

    def queries_between(self, start: float, end: float) -> list[Query]:
        return [q for q in self.queries if start <= q.at <= end and not q.noise]


def _describe_query(query: Query) -> Text:
    text = Text()
    stamp = time.strftime("%H:%M:%S", time.localtime(query.at)) + f".{int(query.at * 1000) % 1000:03d}"
    if not query.target:
        text.append(f"{query.kind}", style="bold")
        text.append(f"  connection {query.connection}  {stamp}\n\n", style="dim")
        return text.append(query.text, style="cyan")
    entry = query.detail
    text.append(f"{query.kind} {query.target}", style="bold")
    text.append(f"  {stamp}" + (f"  {query.millis} ms" if query.millis is not None else "") + "\n", style="dim")
    stats = [f"{name} {entry[key]}" for key, name in (("planSummary", "plan"), ("keysExamined", "keys examined"),
                                                      ("docsExamined", "docs examined"), ("nreturned", "returned"),
                                                      ("nMatched", "matched"), ("nModified", "modified"),
                                                      ("ninserted", "inserted"), ("ndeleted", "deleted"))
             if entry.get(key) is not None]
    if stats:
        text.append(" · ".join(stats) + "\n", style="dim")
    if entry.get("errMsg"):
        text.append(f"{entry['errMsg']}\n", style="red")
    text.append("\n")
    return text.append(json.dumps(json.loads(query.text), indent=2), style="cyan")


class KafkaView(Vertical):
    """Kafka through its own tools: every message on every topic, the topics, the consumer groups' lag, and the
    container's log."""

    BINDINGS = [
        Binding("left", "tab(-1)", "← Tab", priority=True),
        Binding("right", "tab(1)", "Tab →", priority=True),
        Binding("escape", "clear_filters", "All messages"),
    ]
    TABS = ("messages", "topics", "groups", "log")
    HINT = "←/→ switch tab   ↑/↓ choose a row, its details show on the right   Enter on a topic shows its messages"
    MAX_MESSAGES = 20_000

    def __init__(self, service: Service, info: Callable[[], str], id: str | None = None):
        super().__init__(id=id, classes="view")
        self.service = service
        self.client = Kafka(service)
        self.info = info
        self.messages: list[KafkaMessage] = []
        self.groups: list[GroupLag] = []
        self.topics: list[str] = []
        self.topic: str | None = None
        self.window: Window | None = None
        self._seen: set[tuple[str, int, int]] = set()
        self._by_key: dict[str, KafkaMessage] = {}

    def compose(self) -> ComposeResult:
        yield Static(self.info(), classes="viewinfo", markup=False)
        yield Static(self.HINT, classes="hint")
        with TabbedContent(initial="messages"):
            with TabPane("Messages", id="messages"):
                with Horizontal():
                    yield DataTable(id="kafka-messages", cursor_type="row", zebra_stripes=True)
                    yield RichLog(id="kafka-messages-detail", classes="detail", wrap=True, markup=False,
                                  auto_scroll=False)
            with TabPane("Topics", id="topics"):
                yield DataTable(id="kafka-topics", cursor_type="row", zebra_stripes=True)
            with TabPane("Groups", id="groups"):
                yield DataTable(id="kafka-groups", cursor_type="row", zebra_stripes=True)
            with TabPane("Log", id="log"):
                yield LogView(lambda: self.service.lines, lambda: "", id="kafka-log")

    def on_mount(self) -> None:
        self.query_one("#kafka-messages", DataTable).add_columns("Time", "Topic", "Key", "Value")
        self.query_one("#kafka-topics", DataTable).add_columns("Topic", "Messages", "Last", "Lag")
        self.query_one("#kafka-groups", DataTable).add_columns("Group", "Topic", "Partition", "Offset", "End", "Lag",
                                                               "Consumer")
        self.follow()
        self.poll()

    @property
    def log_view(self) -> LogView:
        return self.query_one("#kafka-log", LogView)

    def focus_main(self) -> None:
        active = self.query_one(TabbedContent).active
        if active == "log":
            self.log_view.focus_main()
        else:
            self.query_one(f"#kafka-{active}", DataTable).focus()

    def summary(self) -> str:
        if self.client.error:
            return f"cannot read messages: {self.client.error}"
        return f"{len(self.messages)} messages on {len({m.topic for m in self.messages})} topics"

    def refresh_info(self) -> None:
        if self.is_mounted:
            modes = [m for m in (self.topic and f"topic {self.topic}", self.window and self.window[2]) if m]
            mode = f"   [{' · '.join(modes)}]" if modes else ""
            self.query_one(".viewinfo", Static).update(f"{self.info()}   {self.summary()}{mode}")

    def clear(self) -> None:
        self.messages, self.groups, self.topics, self._seen, self._by_key = [], [], [], set(), {}
        if self.is_mounted:
            self._fill_messages()
            self._fill_topics()
            self.query_one("#kafka-groups", DataTable).clear()
            self.query_one("#kafka-messages-detail", RichLog).clear()
            self.log_view.clear()

    def append(self, lines: Iterable[LogLine]) -> None:
        if self.is_mounted:
            self.log_view.append(lines)

    @work(group="kafka-follow")
    async def follow(self) -> None:
        await self.client.follow(self._received)

    @work(group="kafka-poll")
    async def poll(self) -> None:
        """The topics once, then topics and lag only while their tabs are on screen, as each poll starts JVMs
        in the broker's container."""
        while True:
            showing = isinstance(self.parent, ContentSwitcher) and self.parent.current == self.id
            on_tab = showing and self.query_one(TabbedContent).active in ("topics", "groups")
            if self.service.state == "running" and (on_tab or not self.topics):
                self.topics = await self.client.topics() or self.topics
                if on_tab:
                    self.groups = await self.client.groups()
                    self._fill_groups()
                self._fill_topics()
            self.refresh_info()
            await asyncio.sleep(5)

    def _received(self, batch: list[KafkaMessage]) -> None:
        new = []
        for message in batch:  # a rebalance can deliver a message again, within a batch too
            if (message.topic, message.partition, message.offset) not in self._seen:
                self._seen.add((message.topic, message.partition, message.offset))
                new.append(message)
        if not new:
            return
        self.messages.extend(new)
        trimmed = len(self.messages) > self.MAX_MESSAGES
        if trimmed:
            del self.messages[:len(self.messages) - self.MAX_MESSAGES]
        if self.is_mounted:
            if trimmed:
                self._fill_messages()
            else:
                self._add_rows(new)
            self._fill_topics()
            self.refresh_info()

    def _shown(self, message: KafkaMessage) -> bool:
        if self.topic and message.topic != self.topic:
            return False
        return self.window is None or self.window[0] <= message.at <= self.window[1]

    def _fill_messages(self) -> None:
        self.query_one("#kafka-messages", DataTable).clear()
        self._by_key = {}
        self._add_rows(self.messages)

    def _add_rows(self, messages: list[KafkaMessage]) -> None:
        """Rows in the order messages arrive, the cursor kept on the newest while it is there."""
        table = self.query_one("#kafka-messages", DataTable)
        following = table.row_count == 0 or table.cursor_row >= table.row_count - 1
        for message in messages:
            if not self._shown(message):
                continue
            key = f"{message.topic}:{message.partition}:{message.offset}"
            self._by_key[key] = message
            table.add_row(clock(message.at), message.topic, _preview(message.key, 24), _preview(message.value, 90),
                          key=key)
        if table.row_count and following:
            table.move_cursor(row=table.row_count - 1)

    def _fill_topics(self) -> None:
        table = self.query_one("#kafka-topics", DataTable)
        cursor = table.cursor_row
        table.clear()
        counts: dict[str, int] = {}
        last: dict[str, float] = {}
        for message in self.messages:
            counts[message.topic] = counts.get(message.topic, 0) + 1
            last[message.topic] = message.at
        lag: dict[str, int] = {}
        for group in self.groups:
            lag[group.topic] = lag.get(group.topic, 0) + (group.lag or 0)
        for topic in sorted(set(self.topics) | set(counts)):
            behind = lag.get(topic)
            table.add_row(topic, str(counts.get(topic, 0)), clock(last[topic]) if topic in last else "",
                          Text(str(behind), style="bold red" if behind else "green") if behind is not None else "",
                          key=topic)
        if table.row_count and cursor is not None:
            table.move_cursor(row=min(cursor, table.row_count - 1))

    def _fill_groups(self) -> None:
        table = self.query_one("#kafka-groups", DataTable)
        table.clear()
        for group in sorted(self.groups, key=lambda g: (g.group, g.topic, g.partition)):
            lag = Text(str(group.lag if group.lag is not None else "-"), style="bold red" if group.lag else "green")
            table.add_row(group.group, group.topic, str(group.partition), str(group.offset or "-"),
                          str(group.end or "-"), lag, group.consumer or Text("no active member", style="dim"))

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        if event.data_table.id != "kafka-messages":
            return
        detail = self.query_one("#kafka-messages-detail", RichLog)
        detail.clear()
        message = self._by_key.get(event.row_key.value if event.row_key else "")
        if message is not None:
            detail.write(_describe_message(message))

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        if event.data_table.id == "kafka-topics" and event.row_key and event.row_key.value:
            self.topic = event.row_key.value
            self._filtered()

    def set_window(self, window: Window | None) -> None:
        self.window = window
        self._filtered()

    def _filtered(self) -> None:
        self._fill_messages()
        self.refresh_info()
        self.refresh_bindings()
        tabs = self.query_one(TabbedContent)
        if tabs.active != "messages":
            tabs.active = "messages"
            self.focus_main()

    def check_action(self, action: str, parameters: tuple) -> bool | None:
        if action == "clear_filters":
            return bool(self.topic or self.window)
        return True

    def action_clear_filters(self) -> None:
        self.topic = self.window = None
        self._filtered()

    def action_tab(self, step: int) -> None:
        tabs = self.query_one(TabbedContent)
        tabs.active = self.TABS[(self.TABS.index(tabs.active) + step) % len(self.TABS)]
        self.focus_main()

    def messages_between(self, start: float, end: float) -> list[KafkaMessage]:
        return [m for m in self.messages if start <= m.at <= end]


def _preview(text: str | None, width: int) -> Text:
    if text is None:
        return Text("null", style="dim")
    flat = " ".join(text.split())
    return Text(flat if len(flat) <= width else flat[:width - 1] + "…")


def _describe_message(message: KafkaMessage) -> Text:
    text = Text()
    text.append(f"{message.topic}", style="bold")
    text.append(f"  partition {message.partition}, offset {message.offset}\n", style="dim")
    text.append(time.strftime("%H:%M:%S", time.localtime(message.at)) + f".{int(message.at * 1000) % 1000:03d}\n",
                style="dim")
    text.append("key ", style="dim")
    text.append(f"{message.key}\n" if message.key is not None else "null\n")
    for name, value in message.headers.items():
        text.append(f"{name}: {value}\n", style="dim")
    text.append("\n")
    text.append(_pretty(message.value) if message.value is not None else "null", style="cyan")
    return text


def _describe(raw: dict) -> Text:
    """A request WireMock received (with its response and stub), a near miss, or a stub, readably."""
    text = Text()
    request = raw.get("request") or {}
    if "loggedDate" in request:  # a request WireMock received, not a stub's request pattern
        text.append(f"{request.get('method', '')} {request.get('absoluteUrl') or request.get('url', '')}\n", style="bold")
        for name, value in (request.get("headers") or {}).items():
            text.append(f"{name}: {value}\n", style="dim")
        if request.get("body"):
            text.append("\n" + _pretty(request["body"]) + "\n")
        response = raw.get("response") or raw.get("responseDefinition")
        if response:
            text.append(f"\n→ {response.get('status', '')}\n", style="bold")
            if response.get("body"):
                text.append(_pretty(response["body"]) + "\n")
        stub = raw.get("stubMapping")
        near_miss = raw.get("nearMiss")
        if raw.get("wasMatched") is False and near_miss:
            text.append(f"\nNo stub matched. Closest: {stub_label(near_miss.get('stubMapping'))}\n", style="red")
            text.append(json.dumps(near_miss.get("stubMapping"), indent=2) + "\n", style="cyan")
        elif raw.get("wasMatched") is False:  # WireMock records its built-in 404 as the "stub" here
            text.append("\nNo stub matched, and none is close. The Stubs tab lists them all.\n", style="red")
        elif stub:
            text.append(f"\nstub {stub_label(stub)}\n", style="bold")
            text.append(json.dumps(stub, indent=2) + "\n", style="cyan")
        return text
    text.append(json.dumps(raw, indent=2), style="cyan")
    return text


def _pretty(body: str) -> str:
    try:
        return json.dumps(json.loads(body), indent=2)
    except (ValueError, TypeError):
        return body
