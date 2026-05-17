# Test scenarios

Reproducible broken-cluster fixtures for Phase 6 end-to-end validation of
the odigos-agent against a live kind cluster.

Each subdirectory reproduces one of the three failure modes the agent
must distinguish:

- [source-missing/](source-missing/) - workload present, no `Source` CR;
  agent should classify `source` and emit `propose_create_source`.
- [destination-broken/](destination-broken/) - instrumented workload +
  `Destination` pointing at a bogus endpoint; agent should classify
  `destination`.
- [collector-broken/](collector-broken/) - instrumented workload + valid
  destination + invalid `Processor` CR that breaks the gateway pipeline;
  agent should classify `collector`.

All three scenarios share the test namespace `odigos-agent-test` and a
single busybox `Deployment/demo` workload (see [shared/](shared/)).

## Prerequisites

- A kind cluster with odigos installed (Helm chart from
  [helm/odigos](../../../helm/odigos)).
- The agent itself installed via the chart at
  [deploy/helm/odigos-ai-agent](../helm/odigos-ai-agent) **or** the raw
  manifests at [deploy/raw](../raw). The agent's `Service`
  (`odigos-ai-agent.odigos-system:8765`) and `Secret` holding
  `ODIGOS_AGENT_TOKEN` must exist before running `validate.sh`.

## Running

```bash
# Drive all three scenarios end-to-end. Apply -> wait -> hit the agent
# /debug endpoint -> compare report.root_cause to the expected value ->
# cleanup. The source-missing scenario stops at the proposal (the
# approve/deny flow is a manual UI step).
./validate.sh

# Keep manifests behind for manual inspection.
KEEP=1 ./validate.sh

# Run a single scenario.
./validate.sh source-missing

# Override the wait between apply and /debug (default 60s; long enough
# for odigos reconcilers to produce InstrumentationConfig and the
# collectors to roll out the new pipeline).
SETTLE_SECONDS=90 ./validate.sh
```

Each scenario also exposes `apply.sh` / `cleanup.sh` for use without the
runner (e.g. when driving the agent from the UI by hand).

## Approval flow (source-missing only)

`source-missing` is the only scenario that exercises the v1 mutation
surface. `validate.sh` confirms the agent produces a `proposed_remediation`
with `op=create_source` and a non-empty `request_id`, but it does NOT call
`/api/ai/approve/...`. The Phase 6 exit criteria for the approval path -
Approve actually creates the CR / Deny leaves the cluster untouched /
5-minute timeout works - is a manual UI step against the frontend webapp.
See [source-missing/README.md](source-missing/README.md) for the
checklist.
