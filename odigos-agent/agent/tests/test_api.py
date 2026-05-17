"""Tests for the FastAPI surface.

Mocks the compiled graph with a small async generator so we don't need real
MCPs or Anthropic credentials. Asserts:

- Bearer auth gates /debug and /approve, not /healthz or /readyz.
- Event ordering: session, step*, finding*, report, done.
- Approval round-trip: interrupt -> approval_required -> /approve -> resume.
- /approve returns 404 for unknown session+request_id.

Uses `httpx.AsyncClient` over `ASGITransport` so the API runs in the same
event loop as the test - critical for the approval round-trip where one
task must resolve an `asyncio.Event` while another awaits it.
"""

from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass, field
from typing import Any, AsyncIterator

import httpx
import pytest

from odigos_agent import api as agent_api
from odigos_agent.api import _AppState, app
from odigos_agent.approvals import ApprovalRegistry


TOKEN = "test-token"


@pytest.fixture(autouse=True)
def set_token(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("ODIGOS_AGENT_TOKEN", TOKEN)


@dataclass
class FakeGraph:
    script: list[tuple[str, Any]]
    resume_script: list[tuple[str, Any]] = field(default_factory=list)
    _resumed: bool = False
    yield_delay: float = 0.0

    async def astream(
        self, input_value: Any, config: dict[str, Any], stream_mode: list[str]
    ) -> AsyncIterator[tuple[str, Any]]:
        steps = self.script if not self._resumed else self.resume_script
        self._resumed = True
        for entry in steps:
            if self.yield_delay:
                await asyncio.sleep(self.yield_delay)
            else:
                await asyncio.sleep(0)
            yield entry


def _install_state(graph: Any, approvals: ApprovalRegistry | None = None) -> _AppState:
    state = _AppState(
        graph=graph,
        approvals=approvals or ApprovalRegistry(),
        ready=graph is not None,
        ready_error=None if graph is not None else "graph unavailable",
    )
    app.state.agent = state
    return state


def _parse_sse(text: str) -> list[dict]:
    """Parse a raw SSE response body into [{event, data}, ...]."""
    events: list[dict] = []
    current_event: str | None = None
    data_lines: list[str] = []
    for raw_line in text.split("\n"):
        line = raw_line.rstrip("\r")
        if line == "":
            if current_event is not None:
                payload = "\n".join(data_lines) if data_lines else ""
                events.append(
                    {
                        "event": current_event,
                        "data": json.loads(payload) if payload else {},
                    }
                )
            current_event = None
            data_lines = []
            continue
        if line.startswith(":"):
            continue
        if line.startswith("event:"):
            current_event = line[len("event:") :].strip()
        elif line.startswith("data:"):
            data_lines.append(line[len("data:") :].lstrip())
    return events


def _async_client() -> httpx.AsyncClient:
    transport = httpx.ASGITransport(app=app)
    return httpx.AsyncClient(transport=transport, base_url="http://testserver")


async def test_healthz_no_auth() -> None:
    _install_state(graph=FakeGraph(script=[]))
    async with _async_client() as client:
        r = await client.get("/healthz")
        assert r.status_code == 200
        assert r.json() == {"status": "ok"}


async def test_readyz_503_when_not_ready() -> None:
    _install_state(graph=None)
    async with _async_client() as client:
        r = await client.get("/readyz")
        assert r.status_code == 503
        assert r.json()["status"] == "not_ready"


async def test_readyz_200_when_ready() -> None:
    _install_state(graph=FakeGraph(script=[]))
    async with _async_client() as client:
        r = await client.get("/readyz")
        assert r.status_code == 200


async def test_debug_requires_bearer() -> None:
    _install_state(graph=FakeGraph(script=[]))
    async with _async_client() as client:
        r = await client.post(
            "/debug", json={"namespace": "n", "kind": "Deployment", "name": "x"}
        )
        assert r.status_code == 401


async def test_debug_rejects_wrong_bearer() -> None:
    _install_state(graph=FakeGraph(script=[]))
    async with _async_client() as client:
        r = await client.post(
            "/debug",
            json={"namespace": "n", "kind": "Deployment", "name": "x"},
            headers={"Authorization": "Bearer wrong"},
        )
        assert r.status_code == 401


async def test_debug_happy_path_event_ordering() -> None:
    """No-interrupt run: triage -> source -> synthesize -> done."""
    script = [
        (
            "updates",
            {
                "triage": {
                    "step_log": [
                        {"phase": "triage", "action": "classified as source", "ts": 1.0}
                    ]
                }
            },
        ),
        (
            "updates",
            {
                "source": {
                    "source_findings": {
                        "phase": "source",
                        "summary": "InstrumentationConfig missing",
                    },
                    "step_log": [
                        {"phase": "source", "action": "subgraph completed", "ts": 2.0}
                    ],
                }
            },
        ),
        (
            "updates",
            {
                "synthesize": {
                    "report": {
                        "root_cause": "source_not_instrumented",
                        "confidence": 0.9,
                        "evidence": [],
                        "suggested_actions": [],
                        "proposed_remediation": None,
                    },
                    "step_log": [
                        {
                            "phase": "synthesize",
                            "action": "root_cause=source_not_instrumented",
                            "ts": 3.0,
                        }
                    ],
                }
            },
        ),
    ]
    _install_state(graph=FakeGraph(script=script))
    async with _async_client() as client:
        r = await client.post(
            "/debug",
            json={"namespace": "n", "kind": "Deployment", "name": "x"},
            headers={"Authorization": f"Bearer {TOKEN}"},
        )
        assert r.status_code == 200, r.text
        events = _parse_sse(r.text)
        names = [e["event"] for e in events]
        assert names[0] == "session"
        assert names[-1] == "done"
        assert names.count("step") >= 3
        assert "finding" in names
        assert "report" in names
        assert events[0]["data"]["debug_session_id"]
        finding_event = next(e for e in events if e["event"] == "finding")
        assert finding_event["data"]["phase"] == "source"
        assert "InstrumentationConfig" in finding_event["data"]["summary"]
        report_event = next(e for e in events if e["event"] == "report")
        assert report_event["data"]["root_cause"] == "source_not_instrumented"


async def test_debug_approval_round_trip(monkeypatch: pytest.MonkeyPatch) -> None:
    """Interrupt mid-stream, resolve approval from a sibling task, observe resume."""
    initial_script = [
        (
            "updates",
            {
                "source": {
                    "step_log": [
                        {"phase": "source", "action": "remediation proposed", "ts": 1.0}
                    ]
                }
            },
        ),
        (
            "updates",
            {
                "__interrupt__": {
                    "request_id": "req-1",
                    "op": "create_source",
                    "yaml": "apiVersion: x\n",
                    "diff": "+ apiVersion: x\n",
                    "rollback_command": "kubectl delete ...",
                }
            },
        ),
    ]
    resume_script = [
        (
            "updates",
            {
                "apply_remediation": {
                    "step_log": [
                        {"phase": "apply_remediation", "action": "applied", "ts": 2.0}
                    ]
                }
            },
        ),
        (
            "updates",
            {
                "synthesize": {
                    "report": {
                        "root_cause": "source_not_instrumented",
                        "confidence": 0.95,
                        "proposed_remediation": {
                            "op": "create_source",
                            "request_id": "req-1",
                            "yaml": "apiVersion: x\n",
                            "diff": "+ apiVersion: x\n",
                            "rollback_command": "kubectl delete ...",
                            "status": "approved_applied",
                            "result": "created",
                        },
                    },
                    "step_log": [
                        {
                            "phase": "synthesize",
                            "action": "root_cause=source_not_instrumented",
                            "ts": 3.0,
                        }
                    ],
                }
            },
        ),
    ]
    approvals = ApprovalRegistry()
    graph = FakeGraph(
        script=initial_script, resume_script=resume_script, yield_delay=0.05
    )
    _install_state(graph=graph, approvals=approvals)
    monkeypatch.setattr(agent_api, "APPROVAL_TIMEOUT_SECONDS", 5.0)

    async with _async_client() as client:

        async def resolver() -> None:
            # Wait for the SSE handler to register the pending approval.
            for _ in range(100):
                pending = await _peek_pending(approvals)
                if pending:
                    sid, rid = pending
                    await client.post(
                        f"/approve/{sid}/{rid}",
                        json={"decision": "approve"},
                        headers={"Authorization": f"Bearer {TOKEN}"},
                    )
                    return
                await asyncio.sleep(0.05)
            raise AssertionError("no pending approval appeared")

        resolver_task = asyncio.create_task(resolver())
        try:
            r = await client.post(
                "/debug",
                json={"namespace": "n", "kind": "Deployment", "name": "x"},
                headers={"Authorization": f"Bearer {TOKEN}"},
                timeout=10.0,
            )
        finally:
            await asyncio.wait_for(resolver_task, timeout=5.0)

        assert r.status_code == 200, r.text
        events = _parse_sse(r.text)
        names = [e["event"] for e in events]
        assert "approval_required" in names
        assert "approval_resolved" in names
        assert "report" in names
        assert names[-1] == "done"
        approval_req = next(e for e in events if e["event"] == "approval_required")
        assert approval_req["data"]["request_id"] == "req-1"
        approval_res = next(e for e in events if e["event"] == "approval_resolved")
        assert approval_res["data"]["decision"] == "approve"
        report_event = next(e for e in events if e["event"] == "report")
        assert (
            report_event["data"]["proposed_remediation"]["status"]
            == "approved_applied"
        )


async def test_approve_returns_404_for_unknown() -> None:
    _install_state(graph=FakeGraph(script=[]))
    async with _async_client() as client:
        r = await client.post(
            "/approve/nope/nope",
            json={"decision": "approve"},
            headers={"Authorization": f"Bearer {TOKEN}"},
        )
        assert r.status_code == 404


async def test_approve_requires_bearer() -> None:
    _install_state(graph=FakeGraph(script=[]))
    async with _async_client() as client:
        r = await client.post("/approve/s/r", json={"decision": "approve"})
        assert r.status_code == 401


async def test_debug_approval_deny_path(monkeypatch: pytest.MonkeyPatch) -> None:
    """User denies the approval. SSE should carry approval_resolved with
    decision=deny, then the resumed graph emits a report with status=denied."""
    initial_script = [
        (
            "updates",
            {
                "__interrupt__": {
                    "request_id": "req-deny",
                    "op": "create_source",
                    "yaml": "y\n",
                    "diff": "d\n",
                    "rollback_command": "rc",
                }
            },
        ),
    ]
    resume_script = [
        (
            "updates",
            {
                "synthesize": {
                    "report": {
                        "root_cause": "source_not_instrumented",
                        "confidence": 0.5,
                        "proposed_remediation": {
                            "op": "create_source",
                            "request_id": "req-deny",
                            "yaml": "y\n",
                            "diff": "d\n",
                            "rollback_command": "rc",
                            "status": "denied",
                            "result": None,
                        },
                    },
                    "step_log": [
                        {"phase": "synthesize", "action": "done", "ts": 2.0}
                    ],
                }
            },
        ),
    ]
    approvals = ApprovalRegistry()
    graph = FakeGraph(
        script=initial_script, resume_script=resume_script, yield_delay=0.02
    )
    _install_state(graph=graph, approvals=approvals)
    monkeypatch.setattr(agent_api, "APPROVAL_TIMEOUT_SECONDS", 5.0)

    async with _async_client() as client:

        async def resolver() -> None:
            for _ in range(100):
                pending = await _peek_pending(approvals)
                if pending:
                    sid, rid = pending
                    await client.post(
                        f"/approve/{sid}/{rid}",
                        json={"decision": "deny"},
                        headers={"Authorization": f"Bearer {TOKEN}"},
                    )
                    return
                await asyncio.sleep(0.05)
            raise AssertionError("no pending approval appeared")

        resolver_task = asyncio.create_task(resolver())
        try:
            r = await client.post(
                "/debug",
                json={"namespace": "n", "kind": "Deployment", "name": "x"},
                headers={"Authorization": f"Bearer {TOKEN}"},
                timeout=10.0,
            )
        finally:
            await asyncio.wait_for(resolver_task, timeout=5.0)

        assert r.status_code == 200, r.text
        events = _parse_sse(r.text)
        names = [e["event"] for e in events]
        assert "approval_required" in names
        assert "approval_resolved" in names
        assert "report" in names
        assert names[-1] == "done"
        resolved = next(e for e in events if e["event"] == "approval_resolved")
        assert resolved["data"]["decision"] == "deny"
        report = next(e for e in events if e["event"] == "report")
        assert report["data"]["proposed_remediation"]["status"] == "denied"


async def test_debug_approval_timeout_path(monkeypatch: pytest.MonkeyPatch) -> None:
    """No one approves. SSE should carry approval_resolved with
    decision=timed_out, then a report with status=timed_out."""
    initial_script = [
        (
            "updates",
            {
                "__interrupt__": {
                    "request_id": "req-tmo",
                    "op": "create_source",
                    "yaml": "y\n",
                    "diff": "d\n",
                    "rollback_command": "rc",
                }
            },
        ),
    ]
    resume_script = [
        (
            "updates",
            {
                "synthesize": {
                    "report": {
                        "root_cause": "source_not_instrumented",
                        "confidence": 0.5,
                        "proposed_remediation": {
                            "op": "create_source",
                            "request_id": "req-tmo",
                            "yaml": "y\n",
                            "diff": "d\n",
                            "rollback_command": "rc",
                            "status": "timed_out",
                            "result": None,
                        },
                    },
                    "step_log": [
                        {"phase": "synthesize", "action": "done", "ts": 2.0}
                    ],
                }
            },
        ),
    ]
    approvals = ApprovalRegistry()
    graph = FakeGraph(script=initial_script, resume_script=resume_script)
    _install_state(graph=graph, approvals=approvals)
    monkeypatch.setattr(agent_api, "APPROVAL_TIMEOUT_SECONDS", 0.2)

    async with _async_client() as client:
        r = await client.post(
            "/debug",
            json={"namespace": "n", "kind": "Deployment", "name": "x"},
            headers={"Authorization": f"Bearer {TOKEN}"},
            timeout=10.0,
        )
        assert r.status_code == 200, r.text
        events = _parse_sse(r.text)
        names = [e["event"] for e in events]
        assert "approval_required" in names
        assert "approval_resolved" in names
        assert names[-1] == "done"
        resolved = next(e for e in events if e["event"] == "approval_resolved")
        assert resolved["data"]["decision"] == "timed_out"


async def test_debug_empty_token_serves_unauthenticated_with_warning(
    monkeypatch: pytest.MonkeyPatch, caplog
) -> None:
    """Empty ODIGOS_AGENT_TOKEN is a dev convenience; ensure it logs once."""
    monkeypatch.setenv("ODIGOS_AGENT_TOKEN", "")
    # Reset the module-level warned flag so the warning fires for this test.
    monkeypatch.setattr(agent_api, "_warned_missing_token", False)
    _install_state(graph=FakeGraph(script=[]))
    async with _async_client() as client:
        with caplog.at_level("WARNING", logger="odigos_agent.api"):
            r = await client.post(
                "/debug",
                json={"namespace": "n", "kind": "Deployment", "name": "x"},
            )
        assert r.status_code == 200, r.text
        assert any(
            "ODIGOS_AGENT_TOKEN not set" in record.message for record in caplog.records
        )


async def _peek_pending(registry: ApprovalRegistry) -> tuple[str, str] | None:
    async with registry._lock:  # noqa: SLF001 - test helper
        for key, pending in registry._pending.items():  # noqa: SLF001
            if pending.decision is None:
                return key
    return None
