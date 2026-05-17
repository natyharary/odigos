#!/usr/bin/env bash
#
# Phase 6 end-to-end validation runner.
#
# For each scenario (or the one named on the command line), this script:
#   1. Applies the scenario's manifests against the current kube context.
#   2. Waits SETTLE_SECONDS for odigos to reconcile.
#   3. Port-forwards the in-cluster odigos-ai-agent service and POSTs to
#      /debug, streaming the SSE response and capturing the final
#      `report` event.
#   4. Compares report.root_cause and the presence of proposed_remediation
#      against the scenario's expected.json.
#   5. Cleans up unless KEEP=1.
#
# Required tools on PATH: kubectl, curl, jq.
#
# Required env / cluster state:
#   - kubectl context points at a cluster with odigos + odigos-ai-agent
#     installed in odigos-system.
#   - Either ODIGOS_AGENT_TOKEN env var is set, or a Secret named
#     `odigos-ai-agent-token` (raw kustomize default) exists in
#     odigos-system with key `ODIGOS_AGENT_TOKEN`. Override the secret
#     name with TOKEN_SECRET.
#
# Optional env:
#   SETTLE_SECONDS=60   Seconds to wait between apply and /debug.
#   KEEP=1              Skip cleanup so manifests stay behind for inspection.
#   PORT=18765          Local port for the port-forward.
#   STREAM_TIMEOUT=180  Hard cap (seconds) on the /debug SSE stream.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETTLE_SECONDS="${SETTLE_SECONDS:-60}"
KEEP="${KEEP:-0}"
PORT="${PORT:-18765}"
STREAM_TIMEOUT="${STREAM_TIMEOUT:-180}"
TOKEN_SECRET="${TOKEN_SECRET:-odigos-ai-agent-token}"
AGENT_SERVICE="${AGENT_SERVICE:-odigos-ai-agent}"
AGENT_NAMESPACE="${AGENT_NAMESPACE:-odigos-system}"

ALL_SCENARIOS=(source-missing destination-broken collector-broken)

color() {
  local code="$1"; shift
  if [[ -t 1 ]]; then printf '\033[%sm%s\033[0m\n' "$code" "$*"; else printf '%s\n' "$*"; fi
}
log()  { color "1;34" "[validate] $*"; }
ok()   { color "1;32" "[ pass  ] $*"; }
fail() { color "1;31" "[ fail  ] $*"; }

require() {
  command -v "$1" >/dev/null 2>&1 || { fail "missing required tool: $1"; exit 2; }
}
require kubectl
require curl
require jq

resolve_token() {
  if [[ -n "${ODIGOS_AGENT_TOKEN:-}" ]]; then
    printf '%s' "${ODIGOS_AGENT_TOKEN}"
    return
  fi
  # Don't swallow kubectl errors - missing-secret is the most common
  # first-run failure and the underlying "secrets not found" message
  # tells the user exactly which name to pass via TOKEN_SECRET=.
  kubectl -n "${AGENT_NAMESPACE}" get secret "${TOKEN_SECRET}" \
    -o jsonpath='{.data.ODIGOS_AGENT_TOKEN}' | base64 -d
}

start_port_forward() {
  local local_port="$1"
  kubectl -n "${AGENT_NAMESPACE}" port-forward "svc/${AGENT_SERVICE}" \
    "${local_port}:8765" >/tmp/odigos-agent-pf.log 2>&1 &
  PF_PID=$!
  # Wait until the port is actually listening.
  for _ in $(seq 1 30); do
    if curl -sf "http://127.0.0.1:${local_port}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "port-forward never became ready (see /tmp/odigos-agent-pf.log)"
  return 1
}

stop_port_forward() {
  if [[ -n "${PF_PID:-}" ]] && kill -0 "${PF_PID}" 2>/dev/null; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
  fi
  PF_PID=""
}

trap stop_port_forward EXIT

run_scenario() {
  local scenario="$1"
  local dir="${SCRIPT_DIR}/${scenario}"
  if [[ ! -d "${dir}" ]]; then
    fail "unknown scenario: ${scenario}"
    return 1
  fi

  local expected_root_cause
  local expects_remediation
  local workload_ns workload_kind workload_name
  expected_root_cause=$(jq -r '.expected_root_cause' "${dir}/expected.json")
  expects_remediation=$(jq -r '.expects_proposed_remediation' "${dir}/expected.json")
  workload_ns=$(jq -r '.workload.namespace' "${dir}/expected.json")
  workload_kind=$(jq -r '.workload.kind' "${dir}/expected.json")
  workload_name=$(jq -r '.workload.name' "${dir}/expected.json")

  log "scenario: ${scenario}"

  # A scenario can take minutes between apply and /debug; in the
  # meantime kubectl port-forward sometimes gets reaped by a transient
  # API-server blip. Re-probe before each scenario so a dead tunnel
  # surfaces as a port-forward error, not a confusing /debug failure.
  if ! curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    log "port-forward unhealthy, restarting"
    stop_port_forward
    start_port_forward "${PORT}"
  fi

  log "applying manifests"
  bash "${dir}/apply.sh"

  log "waiting ${SETTLE_SECONDS}s for odigos to reconcile"
  sleep "${SETTLE_SECONDS}"

  log "POST /debug ${workload_kind} ${workload_ns}/${workload_name}"
  local raw_log report
  raw_log="$(mktemp)"
  if ! curl -sS --max-time "${STREAM_TIMEOUT}" -N \
      -H "Authorization: Bearer ${TOKEN}" \
      -H "Content-Type: application/json" \
      -d "{\"namespace\":\"${workload_ns}\",\"kind\":\"${workload_kind}\",\"name\":\"${workload_name}\"}" \
      "http://127.0.0.1:${PORT}/debug" > "${raw_log}"; then
    fail "${scenario}: /debug request failed (see ${raw_log})"
    [[ "${KEEP}" == "1" ]] || bash "${dir}/cleanup.sh" || true
    return 1
  fi

  # Pull the data line that follows `event: report`. sse-starlette emits
  # CRLF-terminated lines (DEFAULT_SEPARATOR = "\r\n"), so strip a
  # trailing \r before matching - otherwise `event: report\r` never
  # matches `^event: report$` and the runner reports "no report event".
  report="$(awk '
    { sub(/\r$/, "") }
    /^event: report$/ { want=1; next }
    want && /^data:/ { sub(/^data: ?/, ""); print; want=0 }
  ' "${raw_log}")"
  if [[ -z "${report}" ]]; then
    fail "${scenario}: no report event in SSE stream (raw: ${raw_log})"
    [[ "${KEEP}" == "1" ]] || bash "${dir}/cleanup.sh" || true
    return 1
  fi

  local actual_root_cause has_remediation remediation_op
  actual_root_cause="$(printf '%s' "${report}" | jq -r '.root_cause')"
  has_remediation="$(printf '%s' "${report}" | jq -r '.proposed_remediation != null')"
  remediation_op="$(printf '%s' "${report}" | jq -r '.proposed_remediation.op // ""')"

  local scenario_failed=0
  if [[ "${actual_root_cause}" != "${expected_root_cause}" ]]; then
    fail "${scenario}: root_cause mismatch (expected ${expected_root_cause}, got ${actual_root_cause})"
    scenario_failed=1
  fi
  if [[ "${expects_remediation}" == "true" && "${has_remediation}" != "true" ]]; then
    fail "${scenario}: expected proposed_remediation, got none"
    scenario_failed=1
  fi
  if [[ "${expects_remediation}" == "false" && "${has_remediation}" == "true" ]]; then
    fail "${scenario}: did not expect proposed_remediation but got op=${remediation_op}"
    scenario_failed=1
  fi

  if [[ "${scenario_failed}" -eq 0 ]]; then
    ok "${scenario}: root_cause=${actual_root_cause} remediation=${has_remediation}"
    [[ -n "${remediation_op}" ]] && log "  proposed op: ${remediation_op}"
  fi

  log "raw SSE stream kept at ${raw_log}"

  if [[ "${KEEP}" == "1" ]]; then
    log "KEEP=1; leaving scenario resources in place"
  else
    log "cleanup"
    bash "${dir}/cleanup.sh" || true
  fi

  return "${scenario_failed}"
}

main() {
  local -a scenarios
  if [[ $# -gt 0 ]]; then
    scenarios=("$@")
  else
    scenarios=("${ALL_SCENARIOS[@]}")
  fi

  TOKEN="$(resolve_token)"
  if [[ -z "${TOKEN}" ]]; then
    fail "could not resolve agent token (set ODIGOS_AGENT_TOKEN or ensure Secret ${TOKEN_SECRET} exists in ${AGENT_NAMESPACE})"
    exit 2
  fi

  log "port-forwarding ${AGENT_SERVICE}.${AGENT_NAMESPACE} -> 127.0.0.1:${PORT}"
  start_port_forward "${PORT}"

  local total=0 failures=0
  for scenario in "${scenarios[@]}"; do
    total=$((total + 1))
    if ! run_scenario "${scenario}"; then
      failures=$((failures + 1))
    fi
  done

  stop_port_forward

  log "------------------------------------------------------------"
  if [[ "${failures}" -eq 0 ]]; then
    ok "all ${total} scenario(s) passed"
    exit 0
  else
    fail "${failures}/${total} scenario(s) failed"
    exit 1
  fi
}

main "$@"
