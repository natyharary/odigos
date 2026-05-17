#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
kubectl apply -f "${SCRIPT_DIR}/manifests.yaml"
kubectl -n odigos-agent-test rollout status deployment/demo --timeout=60s
