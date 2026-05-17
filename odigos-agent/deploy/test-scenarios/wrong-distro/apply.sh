#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
kubectl apply -f "${SCRIPT_DIR}/manifests.yaml"
# Java image needs longer than busybox for the JDK to start.
kubectl -n odigos-agent-test rollout status deployment/demo-java --timeout=180s
