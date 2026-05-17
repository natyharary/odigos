"""Tests for the ProposedRemediation extractor.

The source subgraph captures a pending mutation by walking back through
the ReAct conversation for the most recent successful `propose_*`
ToolMessage. These tests cover the three content shapes
langchain-mcp-adapters can produce (string, content-block list, dict),
the failure / no-call paths, and the four ops introduced in Phase 7.
"""

from __future__ import annotations

import json

from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from odigos_agent.graph import _extract_proposed_remediation


def _make_propose_tool_message(
    payload: dict,
    *,
    status: str = "success",
    tool_name: str = "propose_create_source",
) -> ToolMessage:
    message = ToolMessage(
        content=json.dumps(payload),
        tool_call_id="call-1",
        name=tool_name,
    )
    message.status = status
    return message


def _create_source_payload(**overrides) -> dict:
    base = {
        "op": "create_source",
        "request_id": "req-abc-123",
        "yaml": "apiVersion: odigos.io/v1alpha1\nkind: Source\n",
        "diff": "+ apiVersion: odigos.io/v1alpha1\n",
        "rollback_command": "kubectl delete source -n default -l x=y",
    }
    base.update(overrides)
    return base


def test_extracts_from_string_content():
    messages = [
        HumanMessage(content="hi"),
        AIMessage(content="thinking"),
        _make_propose_tool_message(_create_source_payload()),
    ]
    proposed = _extract_proposed_remediation(messages)
    assert proposed is not None
    assert proposed.op == "create_source"
    assert proposed.request_id == "req-abc-123"
    assert proposed.yaml.startswith("apiVersion")
    assert proposed.status == "pending_approval"


def test_extracts_from_content_block_list():
    payload = _create_source_payload(request_id="req-block", yaml="y", diff="d", rollback_command="kubectl delete")
    message = ToolMessage(
        content=[{"type": "text", "text": json.dumps(payload)}],
        tool_call_id="call-2",
        name="propose_create_source",
    )
    message.status = "success"
    proposed = _extract_proposed_remediation([message])
    assert proposed is not None
    assert proposed.request_id == "req-block"


def test_extracts_from_json_typed_content_block():
    """langchain-mcp-adapters may surface structured tool output as
    {"type": "json", "json": <dict>} blocks. The extractor must handle
    this transport shape, not just text blocks."""
    payload = _create_source_payload(request_id="req-json-block", yaml="y", diff="d", rollback_command="kubectl delete")
    message = ToolMessage(
        content=[{"type": "json", "json": payload}],
        tool_call_id="call-j",
        name="propose_create_source",
    )
    message.status = "success"
    proposed = _extract_proposed_remediation([message])
    assert proposed is not None
    assert proposed.request_id == "req-json-block"


def test_extracts_from_bare_dict_content_block():
    """Some transport versions surface raw dict blocks without a "type"
    wrapper. Treat them as the payload itself."""
    payload = _create_source_payload(request_id="req-bare-dict", yaml="y", diff="d", rollback_command="kubectl delete")
    message = ToolMessage(
        content=[payload],
        tool_call_id="call-b",
        name="propose_create_source",
    )
    message.status = "success"
    proposed = _extract_proposed_remediation([message])
    assert proposed is not None
    assert proposed.request_id == "req-bare-dict"


def test_returns_none_when_no_propose_call():
    messages = [
        HumanMessage(content="hi"),
        AIMessage(content="all good, fully instrumented"),
    ]
    assert _extract_proposed_remediation(messages) is None


def test_skips_failed_propose():
    failure = ToolMessage(
        content="Source already exists for foo/bar - nothing to create",
        tool_call_id="call-3",
        name="propose_create_source",
    )
    failure.status = "error"
    assert _extract_proposed_remediation([failure]) is None


def test_picks_most_recent_proposal():
    first = _make_propose_tool_message(_create_source_payload(request_id="old", yaml="", diff="", rollback_command=""))
    second = _make_propose_tool_message(_create_source_payload(request_id="new", yaml="", diff="", rollback_command=""))
    proposed = _extract_proposed_remediation([first, AIMessage(content="..."), second])
    assert proposed is not None
    assert proposed.request_id == "new"


def test_ignores_other_tool_calls():
    other = ToolMessage(
        content=json.dumps({"request_id": "unrelated"}),
        tool_call_id="call-x",
        name="get_source",
    )
    other.status = "success"
    assert _extract_proposed_remediation([other]) is None


def test_ignores_malformed_json():
    bad = ToolMessage(
        content="not json at all",
        tool_call_id="call-bad",
        name="propose_create_source",
    )
    bad.status = "success"
    assert _extract_proposed_remediation([bad]) is None


def test_extracts_override_distro_with_before_after_yaml():
    payload = {
        "op": "override_distro",
        "request_id": "req-override",
        "yaml_before": "apiVersion: odigos.io/v1alpha1\nkind: InstrumentationRule\nspec: {}\n",
        "yaml_after": "apiVersion: odigos.io/v1alpha1\nkind: InstrumentationRule\nspec:\n  otelDistros:\n    otelDistroNames: [java-community]\n",
        "diff": "- spec: {}\n+ spec:\n+   otelDistros:\n",
        "rollback_command": "kubectl apply ...",
        "context": {
            "language": "java",
            "from_distro": "java-legacy",
            "to_distro": "java-community",
        },
    }
    message = _make_propose_tool_message(payload, tool_name="propose_override_distro")
    proposed = _extract_proposed_remediation([message])
    assert proposed is not None
    assert proposed.op == "override_distro"
    assert proposed.yaml == ""
    assert proposed.yaml_after.endswith("[java-community]\n")
    assert proposed.context["to_distro"] == "java-community"


def test_extracts_enable_obi_with_context():
    payload = {
        "op": "enable_obi",
        "request_id": "req-obi",
        "yaml_before": "",
        "yaml_after": "apiVersion: odigos.io/v1alpha1\nkind: InstrumentationRule\n",
        "diff": "+ kind: InstrumentationRule\n",
        "rollback_command": "kubectl delete instrumentationrule ...",
        "context": {
            "language": "cplusplus",
            "ebpf_distro_name": "opentelemetry-ebpf-instrumentation",
            "prior_distro": "",
        },
    }
    message = _make_propose_tool_message(payload, tool_name="propose_enable_obi")
    proposed = _extract_proposed_remediation([message])
    assert proposed is not None
    assert proposed.op == "enable_obi"
    assert proposed.context["ebpf_distro_name"] == "opentelemetry-ebpf-instrumentation"


def test_skips_propose_message_with_unknown_op():
    """The extractor must refuse to build a ProposedRemediation if the
    MCP returns an op the agent doesn't recognise - otherwise pydantic's
    literal validation raises mid-stream and crashes the subgraph."""
    payload = {
        "op": "restart_workload",
        "request_id": "req-x",
        "diff": "",
        "rollback_command": "",
    }
    message = _make_propose_tool_message(payload, tool_name="propose_create_source")
    assert _extract_proposed_remediation([message]) is None
