#!/usr/bin/env bash
set -euo pipefail
kubectl delete processor.odigos.io agent-test-bad-batch -n odigos-system --ignore-not-found
kubectl delete destination.odigos.io agent-test-debug -n odigos-system --ignore-not-found
kubectl delete namespace odigos-agent-test --ignore-not-found
