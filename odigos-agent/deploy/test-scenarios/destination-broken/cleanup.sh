#!/usr/bin/env bash
set -euo pipefail
kubectl delete destination.odigos.io bad-otlp -n odigos-system --ignore-not-found
kubectl delete namespace odigos-agent-test --ignore-not-found
