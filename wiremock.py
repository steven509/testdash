"""WireMock's admin API: the request journal, near misses for unmatched requests, and the stub mappings.

Testcontainers rarely publishes WireMock's port on the host (compose services sit behind its socat ambassador),
so the API is called through `docker exec` with the container's own curl, which the official image ships. A
published port is used directly when there is one.
"""

from __future__ import annotations

import asyncio
import json
import urllib.request
from dataclasses import dataclass

from services import Service


@dataclass
class ServeEvent:
    id: str
    at: float  # epoch seconds
    method: str
    url: str
    status: int | None
    matched: bool
    stub: str  # the matched stub's name or id, empty when unmatched
    raw: dict


class WireMock:
    def __init__(self, service: Service):
        self.service = service

    @property
    def port(self) -> str:
        command = self.service.command
        if "--port" in command and command.index("--port") + 1 < len(command):
            return command[command.index("--port") + 1]
        exposed = [p.split("/")[0] for p in self.service.exposed_ports]
        return "8080" if "8080" in exposed or not exposed else exposed[0]

    async def requests(self, limit: int = 300) -> list[ServeEvent]:
        body = await self._get(f"/__admin/requests?limit={limit}")
        return [_serve_event(e) for e in (body or {}).get("requests", [])]

    async def near_misses(self) -> list[dict]:
        body = await self._get("/__admin/requests/unmatched/near-misses")
        return (body or {}).get("nearMisses", [])

    async def mappings(self) -> list[dict]:
        body = await self._get("/__admin/mappings")
        return (body or {}).get("mappings", [])

    async def _get(self, path: str) -> dict | None:
        if self.service.state != "running" or not self.service.container_id:
            return None
        host_port = self.service.host_ports.get(f"{self.port}/tcp")
        try:
            if host_port:
                text = await asyncio.to_thread(_http_get, f"http://127.0.0.1:{host_port}{path}")
            else:
                text = await _exec_get(self.service.container_id, f"http://localhost:{self.port}{path}")
            return json.loads(text) if text else None
        except (OSError, ValueError):
            return None


def stub_label(stub: dict | None) -> str:
    if not stub:
        return ""
    request = stub.get("request") or {}
    url = next((request[k] for k in ("url", "urlPath", "urlPattern", "urlPathPattern", "urlPathTemplate") if request.get(k)), "")
    return stub.get("name") or f"{request.get('method', 'ANY')} {url}".strip() or stub.get("id", "")[:8]


def _serve_event(raw: dict) -> ServeEvent:
    request = raw.get("request") or {}
    response = raw.get("response") or raw.get("responseDefinition") or {}
    matched = raw.get("wasMatched", bool(raw.get("stubMapping")))
    return ServeEvent(
        id=raw.get("id", ""),
        at=(request.get("loggedDate") or 0) / 1000,
        method=request.get("method", ""),
        url=request.get("url", ""),
        status=response.get("status"),
        matched=bool(matched),
        stub=stub_label(raw.get("stubMapping")) if matched else "",
        raw=raw,
    )


def _http_get(url: str) -> str:
    with urllib.request.urlopen(url, timeout=5) as response:
        return response.read().decode("utf-8", errors="replace")


async def _exec_get(container_id: str, url: str) -> str:
    for fetch in (["curl", "-s", "-m", "5", url], ["wget", "-qO-", "-T", "5", url]):
        process = await asyncio.create_subprocess_exec(
            "docker", "exec", container_id, *fetch, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
        stdout, _ = await process.communicate()
        if process.returncode == 0:
            return stdout.decode("utf-8", errors="replace")
    return ""
