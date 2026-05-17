#!/usr/bin/env bash
set -euo pipefail
# Drop any agent-created InstrumentationRules (`odigos-agent-` GenerateName
# prefix) plus the test namespace. Ignore not-found so the script is
# idempotent.
kubectl get instrumentationrule.odigos.io -n odigos-agent-test \
  -o name 2>/dev/null \
  | grep -E '/odigos-agent-' \
  | xargs -r kubectl delete -n odigos-agent-test --ignore-not-found
kubectl delete source.odigos.io source-demo-cpp -n odigos-agent-test --ignore-not-found
kubectl delete namespace odigos-agent-test --ignore-not-found
