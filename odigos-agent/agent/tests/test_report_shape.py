"""Structural invariants for the Report schema and synthesis helpers.

These tests don't call an LLM, don't hit an MCP, and don't touch a
cluster. They lock down the shape contract the frontend relies on:

- `Report` round-trips through pydantic JSON without field drift.
- `proposed_remediation` is gated by what's actually in agent state -
  the synthesizer can't hallucinate a request_id into the UI.
- The routing table covers every Classification literal, plus the
  defensive "no triage" fallback.
- `ProposedRemediation` keeps its op + status literals locked so a
  future mutation type can't sneak through without an explicit ADR.
"""

from __future__ import annotations

import json
import typing

import pytest
from pydantic import ValidationError

from odigos_agent.graph import (
    _format_findings_for_synthesis,
    _override_remediation_from_state,
    route_after_triage,
)
from odigos_agent.state import (
    AgentState,
    Classification,
    Finding,
    ProposedRemediation,
    RemediationOp,
    RemediationStatus,
    Report,
    RootCause,
    TriageResult,
    WorkloadInput,
)


def _sample_remediation(**overrides) -> ProposedRemediation:
    base = {
        "op": "create_source",
        "request_id": "req-abc",
        "yaml": "apiVersion: odigos.io/v1alpha1\nkind: Source\n",
        "diff": "+ apiVersion: odigos.io/v1alpha1\n",
        "rollback_command": "kubectl delete source demo -n odigos-agent-test",
    }
    base.update(overrides)
    return ProposedRemediation(**base)


def _workload() -> WorkloadInput:
    return WorkloadInput(namespace="odigos-agent-test", kind="Deployment", name="demo")


# ---------------------------------------------------------------------------
# Report round-trip + field validation
# ---------------------------------------------------------------------------


def test_report_roundtrips_through_pydantic_json():
    """The frontend deserializes `event: report` data verbatim. If a
    field is added or renamed without coordinating the SSE event schema
    in events.py, this test catches it on the agent side."""
    report = Report(
        root_cause="source_not_instrumented",
        confidence=0.87,
        evidence=["no Source CR", "no InstrumentationConfig"],
        suggested_actions=["create a Source CR"],
        proposed_remediation=_sample_remediation(),
    )
    dumped = report.model_dump_json()
    restored = Report.model_validate_json(dumped)
    assert restored == report
    # And the serialized JSON has the exact keys the frontend expects.
    payload = json.loads(dumped)
    assert set(payload.keys()) == {
        "root_cause",
        "confidence",
        "evidence",
        "suggested_actions",
        "proposed_remediation",
    }


def test_report_remediation_is_optional():
    report = Report(root_cause="collector_misconfigured", confidence=0.7)
    assert report.proposed_remediation is None
    assert report.evidence == []
    assert report.suggested_actions == []


@pytest.mark.parametrize("bad_confidence", [-0.01, 1.01, 2.0, -5.0])
def test_report_rejects_confidence_out_of_range(bad_confidence: float):
    with pytest.raises(ValidationError):
        Report(root_cause="unknown", confidence=bad_confidence)


@pytest.mark.parametrize(
    "root_cause",
    [
        "source_not_instrumented",
        "collector_misconfigured",
        "destination_misconfigured",
        "unknown",
    ],
)
def test_root_cause_literals_accepted(root_cause: str):
    Report(root_cause=root_cause, confidence=0.5)  # no raise


def test_root_cause_literals_locked():
    """The Phase 7 design extends the mutation surface but not the
    root_cause vocabulary - adding a new value here is a deliberate ADR.
    Asserting the literal set means a casual addition trips the test."""
    literals = typing.get_args(RootCause)
    assert set(literals) == {
        "source_not_instrumented",
        "collector_misconfigured",
        "destination_misconfigured",
        "unknown",
    }


def test_root_cause_rejects_unknown_literal():
    with pytest.raises(ValidationError):
        Report(root_cause="source_misconfigured", confidence=0.5)  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# ProposedRemediation literal lockdown
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "op",
    ["create_source", "override_distro", "enable_obi", "disable_obi"],
)
def test_proposed_remediation_op_literals_accepted(op: str):
    """Phase 7 expanded the mutation surface to four ops. All must
    deserialize cleanly so a propose_* tool result lands in state with
    its op preserved."""
    ProposedRemediation(
        op=op,
        request_id="r",
        diff="d",
        rollback_command="c",
    )


def test_proposed_remediation_op_literals_locked():
    """Adding a new op without coordinating the agent prompt, the MCP
    tool catalog, the UI approval modal, and RBAC is a foot-gun. Pin
    the literal set so any future addition is a deliberate ADR step."""
    literals = typing.get_args(RemediationOp)
    assert set(literals) == {
        "create_source",
        "override_distro",
        "enable_obi",
        "disable_obi",
    }


def test_proposed_remediation_rejects_unknown_op():
    with pytest.raises(ValidationError):
        ProposedRemediation(
            op="restart_workload",  # type: ignore[arg-type]
            request_id="r",
            diff="d",
            rollback_command="c",
        )


def test_proposed_remediation_create_source_uses_greenfield_yaml():
    remediation = ProposedRemediation(
        op="create_source",
        request_id="r",
        yaml="apiVersion: odigos.io/v1alpha1\nkind: Source\n",
        diff="+ apiVersion: odigos.io/v1alpha1\n",
        rollback_command="kubectl delete source ...",
    )
    assert remediation.yaml.startswith("apiVersion")
    assert remediation.yaml_before == ""
    assert remediation.yaml_after == ""


def test_proposed_remediation_override_distro_uses_before_after_yaml():
    remediation = ProposedRemediation(
        op="override_distro",
        request_id="r",
        yaml_before="apiVersion: odigos.io/v1alpha1\nkind: InstrumentationRule\n",
        yaml_after="apiVersion: odigos.io/v1alpha1\nkind: InstrumentationRule\nspec:\n  otelDistros:\n    otelDistroNames: [java-community]\n",
        diff="+ spec:\n+   otelDistros:\n",
        rollback_command="kubectl apply -f - <<EOF\n... EOF",
        context={
            "language": "java",
            "from_distro": "java-legacy",
            "to_distro": "java-community",
        },
    )
    assert remediation.yaml == ""
    assert "InstrumentationRule" in remediation.yaml_after
    assert remediation.context["to_distro"] == "java-community"


def test_proposed_remediation_enable_obi_context_round_trip():
    remediation = ProposedRemediation(
        op="enable_obi",
        request_id="r",
        yaml_before="",
        yaml_after="apiVersion: odigos.io/v1alpha1\nkind: InstrumentationRule\n",
        diff="+ kind: InstrumentationRule\n",
        rollback_command="kubectl delete instrumentationrule ...",
        context={
            "language": "cplusplus",
            "ebpf_distro_name": "opentelemetry-ebpf-instrumentation",
            "prior_distro": "",
        },
    )
    dumped = remediation.model_dump_json()
    restored = ProposedRemediation.model_validate_json(dumped)
    assert restored == remediation
    assert restored.context["ebpf_distro_name"] == "opentelemetry-ebpf-instrumentation"


@pytest.mark.parametrize(
    "status",
    ["pending_approval", "approved_applied", "denied", "timed_out", "failed"],
)
def test_proposed_remediation_status_literals_accepted(status: str):
    _sample_remediation(status=status)  # no raise


def test_proposed_remediation_status_literals_locked():
    literals = typing.get_args(RemediationStatus)
    assert set(literals) == {
        "pending_approval",
        "approved_applied",
        "denied",
        "timed_out",
        "failed",
    }


def test_proposed_remediation_rejects_unknown_status():
    with pytest.raises(ValidationError):
        _sample_remediation(status="approved")  # type: ignore[arg-type]


def test_proposed_remediation_defaults_to_pending_approval():
    remediation = _sample_remediation()
    assert remediation.status == "pending_approval"
    assert remediation.result is None


# ---------------------------------------------------------------------------
# Remediation gating: state is the source of truth
# ---------------------------------------------------------------------------


def test_override_remediation_clears_when_state_has_none():
    """An LLM may hallucinate a remediation block in the structured
    Report even when no propose_create_source actually ran. The
    override helper must drop it - a fake request_id would 404 on the
    apply path and confuse the UI."""
    llm_report = Report(
        root_cause="source_not_instrumented",
        confidence=0.9,
        proposed_remediation=_sample_remediation(request_id="hallucinated"),
    )
    state: AgentState = {"input_workload": _workload()}
    out = _override_remediation_from_state(llm_report, state)
    assert out.proposed_remediation is None
    # Other fields untouched.
    assert out.root_cause == "source_not_instrumented"
    assert out.confidence == 0.9


def test_override_remediation_attaches_state_remediation():
    llm_report = Report(root_cause="source_not_instrumented", confidence=0.9)
    real_remediation = _sample_remediation(request_id="req-real-1")
    state: AgentState = {
        "input_workload": _workload(),
        "proposed_remediation": real_remediation,
    }
    out = _override_remediation_from_state(llm_report, state)
    assert out.proposed_remediation == real_remediation


def test_override_remediation_replaces_llm_value_with_state_value():
    """If both the LLM and state have a remediation, state wins. The MCP
    approval cache is the only place that knows which request_ids
    actually exist."""
    fake = _sample_remediation(request_id="llm-fake")
    real = _sample_remediation(request_id="state-real")
    llm_report = Report(
        root_cause="source_not_instrumented",
        confidence=0.9,
        proposed_remediation=fake,
    )
    state: AgentState = {
        "input_workload": _workload(),
        "proposed_remediation": real,
    }
    out = _override_remediation_from_state(llm_report, state)
    assert out.proposed_remediation == real
    assert out.proposed_remediation.request_id == "state-real"


# ---------------------------------------------------------------------------
# Findings formatter ordering
# ---------------------------------------------------------------------------


def test_format_findings_orders_source_collector_destination():
    """The synthesis prompt's tiebreaker rule (source > collector >
    destination) relies on findings being presented in that order
    regardless of which subgraphs ran."""
    state: AgentState = {
        "input_workload": _workload(),
        "triage": TriageResult(classification="ambiguous", reasoning="multi"),
        "destination_findings": Finding(phase="destination", summary="dest summary"),
        "collector_findings": Finding(phase="collector", summary="coll summary"),
        "source_findings": Finding(phase="source", summary="src summary"),
    }
    rendered = _format_findings_for_synthesis(state)
    src_idx = rendered.index("src summary")
    coll_idx = rendered.index("coll summary")
    dest_idx = rendered.index("dest summary")
    assert src_idx < coll_idx < dest_idx


def test_format_findings_includes_evidence_and_actions():
    state: AgentState = {
        "input_workload": _workload(),
        "triage": TriageResult(classification="source", reasoning="no Source CR"),
        "source_findings": Finding(
            phase="source",
            summary="missing Source CR",
            evidence=["no Source object", "InstrumentationConfig absent"],
            suggested_actions=["create a Source CR"],
        ),
    }
    rendered = _format_findings_for_synthesis(state)
    assert "missing Source CR" in rendered
    assert "no Source object" in rendered
    assert "InstrumentationConfig absent" in rendered
    assert "create a Source CR" in rendered
    assert "Triage classification: source" in rendered


def test_format_findings_returns_placeholder_when_empty():
    state: AgentState = {"input_workload": _workload()}
    rendered = _format_findings_for_synthesis(state)
    assert rendered == "(no findings produced)"


# ---------------------------------------------------------------------------
# Routing-table coverage (extends test_routing.py with the missing-state path)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "classification,expected",
    [
        ("source", ["source"]),
        ("collector", ["collector"]),
        ("destination", ["destination"]),
        ("ambiguous", ["source", "collector", "destination"]),
        ("unknown", ["synthesize"]),
    ],
)
def test_route_after_triage_covers_all_classifications(
    classification: str, expected: list[str]
):
    state: AgentState = {
        "triage": TriageResult(classification=classification, reasoning="t"),
    }
    assert route_after_triage(state) == expected


def test_route_after_triage_routes_to_synthesize_when_triage_missing():
    """Defensive path - if triage somehow produced no structured result,
    we should still terminate cleanly through synthesize rather than
    raise."""
    assert route_after_triage({}) == ["synthesize"]


def test_classification_literals_locked():
    """Triage classification literals also act as the conditional-edge
    fan-out keys. Adding one without updating route_after_triage would
    crash LangGraph at compile time - this test makes the intent
    explicit."""
    literals = typing.get_args(Classification)
    assert set(literals) == {
        "source",
        "collector",
        "destination",
        "ambiguous",
        "unknown",
    }
