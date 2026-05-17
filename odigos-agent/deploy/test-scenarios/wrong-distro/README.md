# wrong-distro

A Java workload (`Deployment/demo-java`) is deployed into
`odigos-agent-test` with:

- a `Source` CR (so odigos runs runtime detection and identifies
  `language=java` on the demo container), and
- a user-authored `InstrumentationRule` (`misconfigured-java-ebpf`) that
  pins the workload to the universal eBPF distro
  (`opentelemetry-ebpf-instrumentation`).

This is a realistic misconfiguration: the user pinned an eBPF distro
when the language SDK distro (`java-community`) is the right choice for
this Java workload.

## Expected diagnosis

- `report.root_cause == "source_not_instrumented"` (the source taxonomy
  v1 only carries one source bucket; phase 8+ may split out
  "wrong-distro" as its own root_cause).
- `report.proposed_remediation.op == "override_distro"`
- `report.proposed_remediation.status == "pending_approval"`
- `report.proposed_remediation.context.from_distro` should be
  `opentelemetry-ebpf-instrumentation`.
- `report.proposed_remediation.context.to_distro` should be
  `java-community`.
- `report.proposed_remediation.yaml_after` parses as an
  `InstrumentationRule` setting
  `spec.otelDistros.otelDistroNames = ["java-community"]` for the
  workload.

## What `validate.sh` checks

Diagnosis + proposal only. The runner does **not** call
`/api/ai/approve/...`. Cleanup tears down both the user-authored rule
and any agent-created rule.

## Manual UI checks

Drive the frontend webapp's "Fix with AI" button against `Deployment
odigos-agent-test/demo-java` after `apply.sh`, then verify:

1. **Approve actually patches the rule.**
   - Click Approve. The agent calls `apply_override_distro`.
   - Expect either the user's rule to be patched in-place to
     `otelDistroNames: [java-community]`, OR a new
     `odigos-agent-...` workload-scoped rule with that distro to appear
     (depending on which rule the agent picks as its "managed" target).
   - Report card shows `proposed_remediation.status: approved_applied`.
   - Cleanup: `./cleanup.sh`.

2. **Deny leaves the cluster untouched.**
   - Re-run `apply.sh` (fresh namespace).
   - Click Deny. The existing `misconfigured-java-ebpf` rule should
     remain unchanged. No `odigos-agent-...` rule should appear.

3. **Timeout works.**
   - Re-run `apply.sh`. Leave the modal open 5 minutes. Report flips to
     `proposed_remediation.status: timed_out`.
