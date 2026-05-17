#!/usr/bin/env bash
set -euo pipefail
# Also drop any Source CR the agent's apply path may have created during
# the approval flow. Ignore not-found so the script is idempotent.
kubectl delete source.odigos.io demo -n odigos-agent-test --ignore-not-found
kubectl delete namespace odigos-agent-test --ignore-not-found
