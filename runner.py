"""Runs a command in its own process group and streams its output line by line."""

from __future__ import annotations

import asyncio
import os
import signal
from typing import Callable


class Runner:
    def __init__(self):
        self.process: asyncio.subprocess.Process | None = None
        self._stops = 0

    @property
    def running(self) -> bool:
        return self.process is not None and self.process.returncode is None

    async def run(self, command: list[str], cwd: str, on_line: Callable[[str], None]) -> int:
        self._stops = 0
        try:
            self.process = await asyncio.create_subprocess_exec(
                *command, cwd=cwd, stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.STDOUT, start_new_session=True)
        except OSError as e:
            on_line(f"testdash: cannot run {command[0]}: {e}")
            return 127
        while line := await self.process.stdout.readline():
            on_line(line.decode("utf-8", errors="replace").rstrip("\r\n"))
        return await self.process.wait()

    def stop(self) -> None:
        """Interrupts the command (Gradle cancels its build cleanly); a second stop kills it."""
        if not self.running:
            return
        self._stops += 1
        try:
            os.killpg(self.process.pid, signal.SIGINT if self._stops == 1 else signal.SIGKILL)
        except ProcessLookupError:
            pass
