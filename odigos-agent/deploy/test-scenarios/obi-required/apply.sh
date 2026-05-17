#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
kubectl apply -f "${SCRIPT_DIR}/manifests.yaml"
# gcc image is large and compiles on startup; allow extra time.
kubectl -n odigos-agent-test rollout status deployment/demo-cpp --timeout=240s
