# source-missing

Workload `Deployment/demo` is deployed into `odigos-agent-test` with no
`Source` CR. odigos does not instrument it.

## Expected diagnosis

- `report.root_cause == "source_not_instrumented"`
- `report.proposed_remediation.op == "create_source"`
- `report.proposed_remediation.status == "pending_approval"`
- `report.proposed_remediation.yaml` parses as a `Source` CR pointing at
  `Deployment/odigos-agent-test/demo`.

## What `validate.sh` checks

Diagnosis + proposal only. The runner does **not** call
`/api/ai/approve/...` - it leaves the cluster in the "pending approval"
state so the cleanup step can wipe it without an applied `Source`.

## Manual UI checks (Phase 6 exit criteria for the approval flow)

Drive the frontend webapp's "Fix with AI" button against `Deployment
odigos-agent-test/demo` after running `apply.sh`, then verify:

1. **Approve actually creates the CR.**
   - Click Approve in the modal.
   - `kubectl get source.odigos.io -n odigos-agent-test` should show
     `demo` within a few seconds.
   - Report card shows `proposed_remediation.status: approved_applied`.
   - Cleanup: `./cleanup.sh` drops the namespace + the Source.

2. **Deny leaves the cluster untouched.**
   - Re-run `apply.sh` (fresh namespace, no Source).
   - Click Deny.
   - `kubectl get source.odigos.io -n odigos-agent-test` should return
     nothing.
   - Report card shows `proposed_remediation.status: denied`.

3. **Timeout works.**
   - Re-run `apply.sh`.
   - Leave the approval modal open for 5 minutes (countdown visible in
     UI).
   - Report card should flip to
     `proposed_remediation.status: timed_out`. No `Source` is created.

Each of the three sub-checks should pass twice in a row to satisfy the
Phase 6 exit bar.
