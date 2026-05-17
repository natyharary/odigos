#!/usr/bin/env bash
set -euo pipefail
# Drop everything the scenario created plus any InstrumentationRules the
# agent's apply path may have produced via the approval flow. Ignore
# not-found so the script is idempotent.
kubectl delete instrumentationrule.odigos.io -n odigos-agent-test \
  -l odigos.io/agent-test=true --ignore-not-found
# Catch agent-created rules (`odigos-agent-` GenerateName prefix) too.
kubectl get instrumentationrule.odigos.io -n odigos-agent-test \
  -o name 2>/dev/null \
  | grep -E '/odigos-agent-' \
  | xargs -r kubectl delete -n odigos-agent-test --ignore-not-found
kubectl delete source.odigos.io source-demo-java -n odigos-agent-test --ignore-not-found
kubectl delete namespace odigos-agent-test --ignore-not-found
