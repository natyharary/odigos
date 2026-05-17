# destination-broken

Workload `Deployment/demo` is instrumented (it has a `Source`); the
gateway is configured with `Destination/bad-otlp` whose
`OTLP_GRPC_ENDPOINT` resolves to a host that does not exist.

## Expected diagnosis

- `report.root_cause == "destination_misconfigured"`
- `report.proposed_remediation == null`
- `report.suggested_actions` mentions fixing the endpoint or rotating
  the destination config (text-only - v1 has no destination mutation).

## What `validate.sh` checks

- The agent's destination subgraph fires (either directly from triage
  classifying as `destination`, or via the `ambiguous` fan-out).
- The synthesized report's `root_cause` is `destination_misconfigured`.
- No `proposed_remediation` is attached.

## Notes

- The `OTLP_GRPC_ENDPOINT` host `this-host-does-not-exist.invalid` is
  reserved by RFC 6761 and guaranteed not to resolve, so
  `probe_destination_endpoint` will reliably fail.
- The destination type `otlp` is built into odigos - no extra image is
  required.
