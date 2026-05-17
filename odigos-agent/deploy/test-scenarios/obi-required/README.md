# obi-required

A C++ workload (`Deployment/demo-cpp`) is deployed into
`odigos-agent-test` with a `Source` CR. Odigos has no SDK distro for
C++ (it isn't in `Defaulter.GetDefaultDistroNames()` and no community
YAML registers a `language: cplusplus` distro), so the SDK injection
path can never attach to this workload.

The universal eBPF distro (`opentelemetry-ebpf-instrumentation`,
`language: *`) is the only instrumentation option. The agent should
diagnose this and propose `enable_obi`.

## Expected diagnosis

- `report.root_cause == "source_not_instrumented"`
- `report.proposed_remediation.op == "enable_obi"`
- `report.proposed_remediation.status == "pending_approval"`
- `report.proposed_remediation.context.language` should be `cplusplus`.
- `report.proposed_remediation.context.ebpf_distro_name` should be
  `opentelemetry-ebpf-instrumentation`.
- `report.proposed_remediation.yaml_after` parses as a freshly created
  `odigos-agent-...` `InstrumentationRule` with
  `spec.otelDistros.otelDistroNames = ["opentelemetry-ebpf-instrumentation"]`.

## What `validate.sh` checks

Diagnosis + proposal only. The runner does **not** call
`/api/ai/approve/...`.

## Manual UI checks

Drive the frontend webapp's "Fix with AI" button against `Deployment
odigos-agent-test/demo-cpp` after `apply.sh`, then verify:

1. **Approve actually creates the rule.**
   - Click Approve. The agent calls `apply_enable_obi`.
   - A new `odigos-agent-*` `InstrumentationRule` should appear in
     `odigos-agent-test` pinning the workload to
     `opentelemetry-ebpf-instrumentation`.
   - Report card shows `proposed_remediation.status: approved_applied`.
   - Cleanup: `./cleanup.sh`.

2. **Deny leaves the cluster untouched.**
   - Re-run `apply.sh`. Click Deny. No agent-created rule should appear.

3. **Timeout works.**
   - Re-run `apply.sh`. Leave the modal open 5 minutes. Report flips to
     `proposed_remediation.status: timed_out`.

Note: actual span collection via OBI requires Odiglet to be running on
a node whose kernel supports the eBPF programs OBI loads. The agent's
`check_obi_eligibility` tool deliberately does NOT probe the kernel
version (see ADR-017 - kernel checks would need `nodes/proxy` RBAC).
Confirm telemetry flows end-to-end via odigos's own observability path.
