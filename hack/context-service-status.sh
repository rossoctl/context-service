#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIR
readonly CONTEXTCTL="${CONTEXTCTL:-$ROOT_DIR/bin/contextctl}"
readonly NAMESPACE="${CS_NAMESPACE:-serverless-harness}"
readonly KUBE_CONTEXT="${KUBE_CONTEXT:-kind-context-service}"

section() {
  printf '\n== %s ==\n' "$1"
}

if [[ ! -x "$CONTEXTCTL" ]]; then
  echo "contextctl not found at $CONTEXTCTL; run 'make build' or set CONTEXTCTL" >&2
  exit 1
fi

section "Sandbox pools"
"$CONTEXTCTL" sb list

section "Storage classes"
"$CONTEXTCTL" sc list

section "Contexts"
"$CONTEXTCTL" ctx list --namespace "$NAMESPACE"

section "Managed Kubernetes resources · namespace $NAMESPACE"
kubectl --context "$KUBE_CONTEXT" --namespace "$NAMESPACE" get \
  sandboxes,persistentvolumeclaims,pods \
  --selector app.kubernetes.io/managed-by=context-service \
  --output wide

if kubectl --context "$KUBE_CONTEXT" api-resources \
  --api-group extensions.agents.x-k8s.io \
  --output name 2>/dev/null | grep -Fxq sandboxclaims; then
  section "Managed SandboxClaims · namespace $NAMESPACE"
  kubectl --context "$KUBE_CONTEXT" --namespace "$NAMESPACE" get sandboxclaims \
    --selector app.kubernetes.io/managed-by=context-service \
    --output wide
fi
