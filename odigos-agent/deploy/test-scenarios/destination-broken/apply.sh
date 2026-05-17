#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
kubectl apply -f "${SCRIPT_DIR}/manifests.yaml"
kubectl -n odigos-agent-test rollout status deployment/demo --timeout=60s
# Give the autoscaler time to render the new exporter into the gateway
# ConfigMap and the gateway pod time to roll. validate.sh's SETTLE_SECONDS
# adds more wait on top.
sleep 15
