#!/usr/bin/env bash
# Integration test: a response gate reports its verdict with a per-run token,
# holding no Kubernetes RBAC at all.
#
# Until now a gate hook had to be granted patch on agentruns so it could
# kubectl-patch its own verdict annotation. That grant has no resourceNames, so
# every gate hook in a namespace could write every other run's verdict, and the
# controller had to mount a ServiceAccount token into a user-supplied container
# to make it work.
#
# The controller now mints a token scoped to this one run, injects it into the
# gate container alone, and the hook POSTs its verdict to the API server.
#
# Proves:
#   1. No Role/RoleBinding is created for the run (no lifecycle.rbac declared)
#   2. The postRun pod mounts no ServiceAccount token
#   3. A busybox gate with no kubectl can deliver a verdict (rewrite lands)
#   4. retry works over the endpoint, producing a successor AgentRun
#   5. A wrong token is refused and the run still resolves via gateDefault,
#      rather than hanging until the postRun deadline
#
# Requires: a cluster with Sympozium deployed, LM Studio accessible on node.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
TIMEOUT="${TEST_TIMEOUT:-420}"
INSTANCE_NAME="inttest-gatetoken-$(date +%s)"

LM_STUDIO_BASE_URL="${LM_STUDIO_BASE_URL:-http://172.18.0.2:9473/proxy/lm-studio/v1}"
LM_STUDIO_MODEL="${LM_STUDIO_MODEL:-qwen/qwen3.5-9b}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS $*${NC}"; }
fail() { echo -e "${RED}FAIL $*${NC}"; FAILED=1; }
info() { echo -e "${YELLOW}---- $*${NC}"; }

FAILED=0

cleanup() {
  info "Cleaning up..."
  for suffix in rewrite retry badtoken; do
    run="${INSTANCE_NAME}-${suffix}"
    kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete agentrun "$run" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete job -n "$NAMESPACE" -l "sympozium.ai/agent-run=${run}" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pvc "${run}-workspace" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  done
  kubectl delete agent "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── Agent ────────────────────────────────────────────────────────────────────

info "Creating test resources in namespace '$NAMESPACE'"
info "Using LM Studio model '${LM_STUDIO_MODEL}' at ${LM_STUDIO_BASE_URL}"

cat <<EOF | kubectl apply -n "$NAMESPACE" -f -
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${INSTANCE_NAME}
spec:
  agents:
    default:
      model: ${LM_STUDIO_MODEL}
      baseURL: ${LM_STUDIO_BASE_URL}
EOF

# create_run <name> <verdict-json> <token-expr>
#
# token-expr is shell evaluated inside the hook, so a test can present the real
# token or a bogus one. Note what is absent from the spec below: no rbac block.
create_run() {
  local run="$1" verdict="$2" token_expr="$3"
  cat <<EOF | kubectl apply -n "$NAMESPACE" -f -
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${run}
  labels:
    sympozium.ai/instance: ${INSTANCE_NAME}
spec:
  agentRef: ${INSTANCE_NAME}
  agentId: default
  sessionKey: gate-token-test
  task: "Reply with exactly: done"
  model:
    provider: lm-studio
    model: ${LM_STUDIO_MODEL}
    baseURL: ${LM_STUDIO_BASE_URL}
    authSecretRef: ""
  timeout: "3m"
  lifecycle:
    gateDefault: block
    retry:
      maxAttempts: 2
      on: ["gate"]
    postRun:
      - name: tokengate
        image: busybox:1.36
        gate: true
        timeout: 2m
        command: ["sh", "-c"]
        args:
          - |
            set -eu
            echo "posting verdict to \$SYMPOZIUM_API_URL"
            wget -q -O- \
              --header "Authorization: Bearer ${token_expr}" \
              --header "Content-Type: application/json" \
              --post-data '${verdict}' \
              "\$SYMPOZIUM_API_URL/api/v1/runs/\$AGENT_RUN_ID/gate-verdict?namespace=\$AGENT_NAMESPACE" \
              || echo "verdict POST failed with \$?"
EOF
}

# wait_terminal <run>
wait_terminal() {
  local run="$1" elapsed=0 phase="" last=""
  while [[ $elapsed -lt $TIMEOUT ]]; do
    phase="$(kubectl get agentrun "$run" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")"
    if [[ -n "$phase" && "$phase" != "$last" ]]; then
      info "[$run] phase: $phase (${elapsed}s)"
      last="$phase"
    fi
    [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]] && return 0
    sleep 3
    elapsed=$((elapsed + 3))
  done
  fail "[$run] timed out after ${TIMEOUT}s (last phase: ${last})"
  kubectl get agentrun "$run" -n "$NAMESPACE" -o yaml 2>/dev/null || true
  return 1
}

# ── 1. rewrite: the headline no-RBAC path ───────────────────────────────────

REWRITE_RUN="${INSTANCE_NAME}-rewrite"
info "Run 1: a busybox gate with no RBAC rewrites the response"
create_run "$REWRITE_RUN" \
  '{"action":"rewrite","response":"REWRITTEN_BY_TOKEN_GATE"}' \
  '$GATE_VERDICT_TOKEN'
wait_terminal "$REWRITE_RUN" || true

# No RBAC was created, because none was declared.
if kubectl get role "sympozium-lifecycle-${REWRITE_RUN}" -n "$NAMESPACE" >/dev/null 2>&1; then
  fail "a Role was created for the run; the gate should need none"
else
  pass "no Role exists for the run"
fi
if kubectl get rolebinding "sympozium-lifecycle-${REWRITE_RUN}" -n "$NAMESPACE" >/dev/null 2>&1; then
  fail "a RoleBinding was created for the run"
else
  pass "no RoleBinding exists for the run"
fi

# No ServiceAccount token reached the postRun pod.
POSTRUN_VOLUMES="$(kubectl get pod -n "$NAMESPACE" \
  -l "sympozium.ai/agent-run=${REWRITE_RUN},sympozium.ai/component=post-run" \
  -o jsonpath='{.items[*].spec.volumes[*].name}' 2>/dev/null || echo "")"
if [[ -n "$POSTRUN_VOLUMES" ]]; then
  if grep -q "sympozium-run-api-access" <<<"$POSTRUN_VOLUMES"; then
    fail "the postRun pod mounted an API access token: ${POSTRUN_VOLUMES}"
  else
    pass "the postRun pod mounts no API access token (${POSTRUN_VOLUMES})"
  fi
else
  info "postRun pod already reaped; skipping the volume assertion"
fi

# The verdict was applied.
VERDICT="$(kubectl get agentrun "$REWRITE_RUN" -n "$NAMESPACE" -o jsonpath='{.status.gateVerdict}' 2>/dev/null || echo "")"
RESULT="$(kubectl get agentrun "$REWRITE_RUN" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")"
if [[ "$VERDICT" == "rewritten" ]]; then
  pass "status.gateVerdict == rewritten"
else
  fail "status.gateVerdict == '${VERDICT}', want 'rewritten'"
fi
if grep -q "REWRITTEN_BY_TOKEN_GATE" <<<"$RESULT"; then
  pass "the rewritten response reached status.result"
else
  fail "status.result does not carry the gate's text: ${RESULT}"
fi

# ── 2. retry over the endpoint ───────────────────────────────────────────────

RETRY_RUN="${INSTANCE_NAME}-retry"
info "Run 2: the gate asks for a retry (the endpoint used to reject this)"
create_run "$RETRY_RUN" \
  '{"action":"retry","reason":"integration retry"}' \
  '$GATE_VERDICT_TOKEN'
wait_terminal "$RETRY_RUN" || true

if kubectl get agentrun "${RETRY_RUN}-retry-2" -n "$NAMESPACE" >/dev/null 2>&1; then
  pass "a successor AgentRun was created from a retry verdict"
else
  fail "no successor AgentRun; retry did not take effect"
  kubectl get agentruns -n "$NAMESPACE" -o wide 2>/dev/null || true
fi

# ── 3. a wrong token is refused, and the run still resolves ─────────────────

BAD_RUN="${INSTANCE_NAME}-badtoken"
info "Run 3: a wrong token must be refused without hanging the run"
create_run "$BAD_RUN" \
  '{"action":"approve"}' \
  'not-the-real-token'
wait_terminal "$BAD_RUN" || true

BAD_VERDICT="$(kubectl get agentrun "$BAD_RUN" -n "$NAMESPACE" -o jsonpath='{.status.gateVerdict}' 2>/dev/null || echo "")"
BAD_PHASE="$(kubectl get agentrun "$BAD_RUN" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")"
if [[ "$BAD_VERDICT" == "approved" ]]; then
  fail "a bogus token was accepted — the verdict was applied"
else
  pass "the bogus token did not produce an approval (verdict: '${BAD_VERDICT}')"
fi
if [[ "$BAD_PHASE" == "Succeeded" || "$BAD_PHASE" == "Failed" ]]; then
  pass "the run still reached a terminal phase (${BAD_PHASE}) via gateDefault"
else
  fail "the run is stuck in phase '${BAD_PHASE}' after a refused verdict"
fi

# ── Summary ──────────────────────────────────────────────────────────────────

echo
if [[ $FAILED -eq 0 ]]; then
  echo -e "${GREEN}All gate token verdict assertions passed${NC}"
else
  echo -e "${RED}Some gate token verdict assertions failed${NC}"
fi
exit $FAILED
