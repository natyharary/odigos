# collector-broken

Workload `Deployment/demo` is instrumented and the destination is a
healthy `debug` exporter. A `Processor` named `agent-test-bad-batch`
ships with an inverted batch config (`send_batch_max_size: 1` is smaller
than `send_batch_size: 512`); the gateway rejects the rendered config
and stops processing spans.

## Expected diagnosis

- `report.root_cause == "collector_misconfigured"`
- `report.proposed_remediation == null`
- `report.evidence` mentions the gateway pod restart count or a config
  validation log line referencing the batch processor.
- `report.suggested_actions` recommends fixing or removing the
  `agent-test-bad-batch` Processor.

## What `validate.sh` checks

- The agent's collector subgraph fires.
- The synthesized report's `root_cause` is `collector_misconfigured`.
- No `proposed_remediation` is attached.

## Notes

- If the autoscaler aggressively validates Processors before applying
  them to the ConfigMap and refuses to render the bad config, change
  `send_batch_max_size` to a negative value or set `type:` to something
  the collector doesn't recognize. The agent should still classify the
  failure as `collector_misconfigured` because the broken `Processor`
  CR will show up in `get_processors()` and the gateway pipeline will
  diverge from what the workload's `InstrumentationConfig` expects.
