"""The test tree: picking tests on the picker screen, following their results on the run screen."""

from __future__ import annotations

import os
import re
import time

from rich.text import Text
from textual import events
from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.message import Message
from textual.reactive import reactive
from textual.timer import Timer
from textual.widgets import Input, RichLog, Static, Tree
from textual.widgets.tree import TreeNode

from catalog import Entry
from views import ICON, clock, duration

PICKED = ("●", "bold green")  # distinct from a result's ✔
PARTLY = ("◐", "bold yellow")
UNPICKED = ("·", "dim")
NOT_SEARCHED = re.compile(r"[^a-z0-9]")  # spaces and punctuation: "signup" finds "a sign-up stores"


def searchable(text: str) -> str:
    return NOT_SEARCHED.sub("", text.lower())


class TestTree(Tree[str]):
    """Left and right collapse and expand. When picking, typing filters the tree, space selects and enter moves on
    to the Run button, so the picker's other keys are control keys."""

    BINDINGS = [
        Binding("left", "collapse", "Collapse", show=False),
        Binding("right", "expand", "Expand", show=False),
        Binding("space", "pick", "Select"),
        Binding("ctrl+a", "pick_all", "All/none"),
        Binding("enter", "submit", "Run…"),
        Binding("enter", "run_this", "Run this"),
    ]
    # Nerd Font chevrons, as neo-tree and lazygit draw them; most fonts have no centred down caret otherwise
    ICON_NODE, ICON_NODE_EXPANDED = ("\uf460 ", "\uf47c ") if os.environ.get("TESTDASH_NERD_FONT") else ("▸ ", "▾ ")

    class Typed(Message):
        def __init__(self, character: str | None):
            super().__init__()
            self.character = character  # None for backspace

    class Pick(Message):
        def __init__(self, node: TreeNode[str] | None):
            super().__init__()
            self.node = node  # None picks or unpicks everything shown

    class Submit(Message):
        pass

    class RunThis(Message):
        def __init__(self, node: TreeNode[str] | None):
            super().__init__()
            self.node = node

    def __init__(self, picking: bool, **kwargs):
        super().__init__("tests", **kwargs)
        self.picking = picking
        self.show_root = False
        self.guide_depth = 3

    def render_label(self, node: TreeNode[str], base_style, style) -> Text:
        label = super().render_label(node, base_style, style)
        if node.allow_expand:
            label.stylize("dim", 0, len(self.ICON_NODE))
            return label
        return Text.assemble(("  ", base_style), label)  # so a test's marker lines up with a nested class's

    def check_action(self, action: str, parameters: tuple) -> bool | None:
        if action in ("pick", "pick_all", "submit") and not self.picking:
            return False  # the run screen's tree shows results, and Enter runs a test again
        if action == "run_this" and self.picking:
            return False
        return True

    def on_key(self, event: events.Key) -> None:
        if not self.picking:
            return
        if event.key == "backspace":
            self.post_message(self.Typed(None))
        elif event.is_printable and event.character and event.character != " ":
            self.post_message(self.Typed(event.character))
        else:
            return
        event.stop()
        event.prevent_default()  # a typed letter filters; it is not a shortcut

    def action_collapse(self) -> None:
        node = self.cursor_node
        if node is None:
            return
        if node.is_expanded and node.children:
            node.collapse()
        elif node.parent is not None and node.parent is not self.root:
            self.move_cursor(node.parent)

    def action_expand(self) -> None:
        node = self.cursor_node
        if node is None or not node.children:
            return
        if not node.is_expanded:
            node.expand()
        else:
            self.move_cursor(node.children[0])

    def action_pick(self) -> None:
        self.post_message(self.Pick(self.cursor_node))

    def action_pick_all(self) -> None:
        self.post_message(self.Pick(None))

    def action_submit(self) -> None:
        self.post_message(self.Submit())

    def action_run_this(self) -> None:
        self.post_message(self.RunThis(self.cursor_node))


class TestsView(Vertical):
    """The current task's tests. The tree's state lives on the app (app.entries, app.selected, app.status) so
    the picker and the run screen show the same tests and selection."""

    BINDINGS = [
        Binding("slash", "find", "Find"),
        Binding("s", "cycle_show", "Show"),
        Binding("l", "during('app')", "Its log"),
        Binding("w", "during('wiremock')", "Its WireMock"),
        Binding("k", "during('kafka')", "Its Kafka"),
        Binding("d", "during('database')", "Its queries"),
        Binding("escape", "clear_find", "Clear filter"),
    ]
    SHOW = ("all", "failed", "not passed")
    text = reactive("", repaint=False, bindings=True)  # Esc clears it, or does the screen's job when empty

    def __init__(self, picking: bool, id: str | None = None):
        super().__init__(id=id, classes="view")
        self.picking = picking
        self.nodes: dict[str, TreeNode[str]] = {}
        self.show = "all"
        self._find_timer: Timer | None = None

    def compose(self) -> ComposeResult:
        if self.picking:
            yield Static("", id="findbar", classes="findbar")
        else:
            yield Input(placeholder="find tests by name or class, Esc clears", classes="filter hidden", compact=True)
        with Horizontal():
            yield TestTree(self.picking)
            if not self.picking:
                yield RichLog(id="test-detail", classes="detail", wrap=True, markup=False, auto_scroll=False)

    def on_mount(self) -> None:
        self.rebuild()
        self._show_findbar()

    def check_action(self, action: str, parameters: tuple) -> bool | None:
        if self.picking and action in ("find", "cycle_show"):
            return False  # typing filters the picker's tree
        if action == "clear_find":
            return bool(self.text)
        if action == "during":
            return not self.picking and self.screen.has_service(parameters[0])
        return True

    @property
    def test_tree(self) -> TestTree:
        return self.query_one(TestTree)

    def focus_main(self) -> None:
        self.test_tree.focus()

    def rebuild(self) -> None:
        if not self.is_mounted:
            return
        app = self.app
        tree = self.test_tree
        cursor = tree.cursor_node.data if tree.cursor_node else None
        tree.clear()
        self.nodes = {}
        visible = self._visible()
        expand = bool(self.text) or self.show != "all" or not self.picking

        def add(parent: TreeNode[str], entry: Entry) -> None:
            if entry.id not in visible:
                return
            kids = app.kids.get(entry.id, [])
            if not kids:
                node = parent.add_leaf(self._label(entry), data=entry.id)
            else:
                node = parent.add(self._label(entry), data=entry.id, expand=expand or entry.kind == "template")
            self.nodes[entry.id] = node
            for kid in kids:
                add(node, kid)

        for top in app.kids.get(None, []):
            add(tree.root, top)
        if len(self.nodes) <= 40:
            tree.root.expand_all()
        if cursor in self.nodes:
            tree.move_cursor(self.nodes[cursor])
        elif self.nodes:
            tree.cursor_line = 0

    def apply(self, changed: set[str]) -> None:
        """Relabels what changed; adds tests a run created (parameterised invocations) under their template."""
        if not self.is_mounted:
            return
        app = self.app
        for entry_id in changed:
            if entry_id not in self.nodes and entry_id in app.entries:
                entry = app.entries[entry_id]
                parent = self.nodes.get(entry.parent)
                if parent is not None and self._wanted(entry):
                    self.nodes[entry_id] = parent.add_leaf(self._label(entry), data=entry_id)
        for entry_id in changed:
            self._relabel_up(entry_id)
        if not self.picking and self.test_tree.cursor_node is not None and self.test_tree.cursor_node.data in changed:
            self._show_detail(self.test_tree.cursor_node.data)

    def relabel_all(self) -> None:
        if self.is_mounted:
            for entry_id, node in self.nodes.items():
                node.set_label(self._label(self.app.entries[entry_id]))

    # what is shown

    def _visible(self) -> set[str]:
        app = self.app
        visible: set[str] = set()
        needle = searchable(self.text)

        def walk(entry: Entry) -> bool:
            kids = [kid for kid in app.kids.get(entry.id, []) if walk(kid)]
            class_match = entry.kind == "class" and needle and needle in searchable(entry.class_name)
            own = entry.is_leaf and self._wanted(entry) and (
                not needle or needle in searchable(entry.name) or needle in searchable(entry.class_name))
            if own or kids or class_match:
                visible.add(entry.id)
                if class_match:
                    visible.update(i for i in app.descendants(entry.id) if self._wanted(app.entries[i]))
                return True
            return False

        for top in app.kids.get(None, []):
            walk(top)
        return visible

    def _wanted(self, entry: Entry) -> bool:
        status = self.app.status(entry.id)
        if not self.picking and status == "absent":
            return False  # the run screen shows the run's tests only
        if self.show == "failed":
            return status in ("failed", "aborted")
        if self.show == "not passed":
            return status not in ("passed", "skipped")
        return True

    def _relabel_up(self, entry_id: str | None) -> None:
        entries = self.app.entries
        while entry_id in entries:
            node = self.nodes.get(entry_id)
            if node is not None:
                node.set_label(self._label(entries[entry_id]))
            entry_id = entries[entry_id].parent

    def _label(self, entry: Entry) -> Text:
        app = self.app
        if entry.kind == "class":
            leaves = app.leaves_under(entry.id)
            statuses = [app.status(leaf) for leaf in leaves]
            if self.picking:
                picked = sum(1 for leaf in leaves if leaf in app.selected)
                mark = PICKED if leaves and picked == len(leaves) else PARTLY if picked else UNPICKED
                count = f"{picked}/{len(leaves)}" if 0 < picked < len(leaves) else str(len(leaves))
                text = Text.assemble((mark[0] + " ", mark[1]), (self._class_name(entry), "bold"), (f"  {count}", "dim"))
                if any(s in ("failed", "aborted") for s in statuses):
                    text.append("  ✘ last run", style="red")
                return text
            in_run = [s for s in statuses if s != "absent"]
            done = sum(1 for s in in_run if s in ("passed", "failed", "aborted", "skipped"))
            status = ("failed" if any(s in ("failed", "aborted") for s in in_run) else
                      "running" if "running" in in_run else
                      "passed" if in_run and done == len(in_run) else "pending")
            icon, style = ICON[status]
            return Text.assemble((icon + " ", style), (self._class_name(entry), "bold"), (f"  {done}/{len(in_run)}", "dim"))

        status = app.status(entry.id)
        if self.picking:
            mark = (PICKED if entry.id in app.selected else UNPICKED) if entry.id in app.selectable else ("", "")
            text = Text.assemble((mark[0] + " " if mark[0] else "  ", mark[1]), entry.name)
            if status in ("passed", "failed", "aborted", "skipped"):
                icon, style = ICON[status]
                text.append(f"  {icon}", style=style)
            return text
        icon, style = ICON[status if status in ICON else "pending"]
        took = duration(app.state(entry.id).took(time.time() * 1000)) if app.state(entry.id).started_at else ""
        return Text.assemble((icon + " ", style), (f"{took:>6} ", "dim"), entry.name)

    def _class_name(self, entry: Entry) -> str:
        """A nested class by its own name, since the tree already shows the class it is in."""
        parent = self.app.entries.get(entry.parent)
        return entry.name if parent is not None and parent.kind == "class" else entry.short_class

    # interaction

    def on_test_tree_pick(self, message: TestTree.Pick) -> None:
        app = self.app
        if message.node is None:
            targets = {i for i in self.nodes if i in app.selectable}
        elif message.node.data in app.entries:
            entry = app.entries[message.node.data]
            targets = ({entry.id} if entry.is_leaf else set(app.leaves_under(entry.id))) & app.selectable
            if self.text:
                targets &= set(self.nodes)  # a class under a filter selects only its matching tests
        else:
            return
        if targets and targets <= app.selected:
            app.selected -= targets
        else:
            app.selected |= targets
        for entry_id in targets:
            self._relabel_up(entry_id)
        if message.node is not None and message.node.data in app.selectable:
            self.test_tree.action_cursor_down()
        app.selection_changed()

    def on_tree_node_highlighted(self, event: Tree.NodeHighlighted[str]) -> None:
        if not self.picking and event.node.data:
            self._show_detail(event.node.data)

    def _show_detail(self, entry_id: str) -> None:
        detail = self.query_one("#test-detail", RichLog)
        detail.clear()
        detail.write(self.app.describe_test(entry_id))

    # finding: typed into the picker's tree, or through the run screen's find field

    def on_test_tree_typed(self, message: TestTree.Typed) -> None:
        self.text = self.text[:-1] if message.character is None else self.text + message.character
        self._show_findbar()
        if self._find_timer is not None:
            self._find_timer.stop()
        self._find_timer = self.set_timer(0.12, self._find)  # one rebuild per burst of typing

    def _find(self) -> None:
        self.rebuild()
        self._show_findbar()
        if self.picking:
            self.screen.show_detail()

    def _show_findbar(self) -> None:
        if not self.picking or not self.is_mounted:
            return
        bar = self.query_one("#findbar", Static)
        if not self.text:
            bar.update(Text("type to filter by test or class name", style="dim"))
            return
        matching = sum(1 for i in self.nodes if i in self.app.selectable)
        bar.update(Text.assemble(("filter ", "dim"), (self.text, "bold"), ("▏", "blink"),
                                 (f"   {matching} matching tests   Esc clears", "dim")))

    def action_find(self) -> None:
        field = self.query_one(Input)
        field.remove_class("hidden")
        field.focus()

    def action_during(self, kind: str) -> None:
        node = self.test_tree.cursor_node
        window = self.app.window_for(node.data) if node is not None and node.data else None
        if window is None:
            self.app.notify("That has not run yet", severity="warning")
            return
        self.screen.show_during(kind, window)

    def on_test_tree_run_this(self, message: TestTree.RunThis) -> None:
        if message.node is not None and message.node.data:
            self.app.start_run(self.app.selection_for(message.node.data))

    def action_clear_find(self) -> None:
        self.text = ""
        if not self.picking:
            field = self.query_one(Input)
            field.value = ""
            field.add_class("hidden")
        self._find()
        self.test_tree.focus()

    def on_input_changed(self, event: Input.Changed) -> None:
        event.stop()
        self.text = event.value
        self.rebuild()

    def on_input_submitted(self, event: Input.Submitted) -> None:
        event.stop()
        if not event.value:
            event.input.add_class("hidden")
        self.test_tree.focus()

    def action_cycle_show(self) -> None:
        self.show = self.SHOW[(self.SHOW.index(self.show) + 1) % len(self.SHOW)]
        self.rebuild()
        self.app.notify(f"Showing {self.show} tests")
