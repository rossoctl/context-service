#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIR
readonly CLUSTER_NAME="${KIND_CLUSTER_NAME:-context-service}"
readonly KUBE_CONTEXT="kind-${CLUSTER_NAME}"
readonly HOST_PORT="${CONTEXT_SERVICE_PORT:-8080}"
IMAGE="context-service:kind-$(date -u +%Y%m%d%H%M%S)-$$"
readonly IMAGE
readonly NAMESPACE="serverless-harness"
readonly LOCAL_PATH_VERSION="v0.0.37"
readonly LOCAL_PATH_MANIFEST="https://raw.githubusercontent.com/rancher/local-path-provisioner/${LOCAL_PATH_VERSION}/deploy/local-path-storage.yaml"
readonly AGENT_SANDBOX_VERSION="v1.0.0"
readonly AGENT_SANDBOX_MANIFEST="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox.yaml"

usage() {
  cat <<'EOF'
Usage: hack/kind-quickstart.sh [up|smoke|down]

  up      Create or reuse a Kind cluster and deploy Context Service.
  smoke   Verify storage discovery and a complete context PVC lifecycle.
  down    Delete the quickstart Kind cluster.

Environment:
  KIND_CLUSTER_NAME      Cluster name (default: context-service)
  CONTEXT_SERVICE_PORT   Host port for the service (default: 8080)
EOF
}

require() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

kubectl_kind() {
  kubectl --context "$KUBE_CONTEXT" "$@"
}

wait_for_http() {
  local endpoint="$1"
  local attempts=60
  local response

  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if response="$(curl --silent --show-error --fail --connect-timeout 2 --max-time 5 "$endpoint" 2>/dev/null)"; then
      printf '%s' "$response"
      return 0
    fi
    sleep 2
  done

  echo "timed out waiting for $endpoint" >&2
  return 1
}

create_cluster() {
  local cluster_config
  local mapped_port

  if ! [[ "$HOST_PORT" =~ ^[0-9]+$ ]] || ((HOST_PORT < 1 || HOST_PORT > 65535)); then
    echo "CONTEXT_SERVICE_PORT must be an integer from 1 to 65535" >&2
    exit 1
  fi

  if kind get clusters 2>/dev/null | grep -Fxq "$CLUSTER_NAME"; then
    mapped_port="$(docker inspect "${CLUSTER_NAME}-control-plane" \
      --format '{{(index (index .HostConfig.PortBindings "30080/tcp") 0).HostPort}}' 2>/dev/null || true)"
    if [[ "$mapped_port" != "$HOST_PORT" ]]; then
      echo "Kind cluster $CLUSTER_NAME maps Context Service to port ${mapped_port:-none}, not $HOST_PORT" >&2
      echo "Run 'make kind-down' before changing CONTEXT_SERVICE_PORT" >&2
      exit 1
    fi
    echo "Reusing Kind cluster $CLUSTER_NAME"
    return
  fi

  cluster_config="$(mktemp)"
  trap 'rm -f "${cluster_config:-}"' RETURN
  sed "s/hostPort: 8080/hostPort: ${HOST_PORT}/" \
    "$ROOT_DIR/deploy/kind/cluster.yaml" >"$cluster_config"
  kind create cluster --name "$CLUSTER_NAME" --config "$cluster_config" --wait 2m
}

up() {
  require docker
  require kind
  require kubectl
  require curl

  docker info >/dev/null
  create_cluster

  kubectl_kind apply -f "$LOCAL_PATH_MANIFEST"
  kubectl_kind -n local-path-storage rollout status deployment/local-path-provisioner --timeout=2m
  kubectl_kind apply -f "$AGENT_SANDBOX_MANIFEST"
  kubectl_kind -n agent-sandbox-system rollout status deployment/agent-sandbox-controller --timeout=2m

  docker build --tag "$IMAGE" "$ROOT_DIR"
  kind load docker-image --name "$CLUSTER_NAME" "$IMAGE"

  kubectl_kind apply -f "$ROOT_DIR/deploy/kind/namespace.yaml"
  kubectl_kind apply -f "$ROOT_DIR/deploy/context-service.yaml"
  kubectl_kind -n "$NAMESPACE" set image deployment/context-service \
    context-service="$IMAGE"
  kubectl_kind -n "$NAMESPACE" set env deployment/context-service \
    CS_SANDBOX_IMAGE=busybox:1.36
  kubectl_kind -n "$NAMESPACE" patch service context-service --type merge \
    --patch '{"spec":{"type":"NodePort","ports":[{"name":"http","port":8080,"targetPort":"http","nodePort":30080}]}}'
  kubectl_kind -n "$NAMESPACE" rollout status deployment/context-service --timeout=2m
  wait_for_http "http://127.0.0.1:${HOST_PORT}/healthz" >/dev/null

  echo "Context Service is ready at http://127.0.0.1:${HOST_PORT}"
}

smoke() {
  local endpoint="http://127.0.0.1:${HOST_PORT}"
  local context_name="quickstart-$$"
  local consumer_name="context-${context_name}-consumer"
  local pool_name="quickstart-pool-$$"
  local response

  require kubectl
  require curl
  if [[ ! -x "$ROOT_DIR/bin/contextctl" ]]; then
    echo "bin/contextctl is missing; run 'make build' first" >&2
    exit 1
  fi

  wait_for_http "$endpoint/healthz" >/dev/null
  response="$(curl --silent --show-error --fail --connect-timeout 2 --max-time 10 \
    "$endpoint/v1/storage-classes")"
  if ! grep -q '"name":"local-path"' <<<"$response"; then
    echo "local-path StorageClass was not returned: $response" >&2
    exit 1
  fi

  cleanup_smoke() {
    CS_URL="$endpoint" "$ROOT_DIR/bin/contextctl" sandbox-pool delete "$pool_name" >/dev/null 2>&1 || true
    kubectl_kind -n "$NAMESPACE" delete pod "$consumer_name" --ignore-not-found --wait=false >/dev/null 2>&1 || true
    curl --silent --show-error --connect-timeout 2 --max-time 10 --request DELETE \
      "$endpoint/v1/namespaces/$NAMESPACE/contexts/$context_name" >/dev/null 2>&1 || true
  }
  trap cleanup_smoke EXIT

  response="$(curl --silent --show-error --fail \
    --connect-timeout 2 --max-time 10 \
    --header 'Content-Type: application/json' \
    --data "{\"name\":\"$context_name\",\"namespace\":\"$NAMESPACE\",\"type\":\"workspace\",\"storage\":{\"backend\":\"pvc\",\"size\":\"128Mi\",\"accessMode\":\"ReadWriteOnce\",\"storageClass\":\"local-path\"}}" \
    "$endpoint/v1/contexts")"
  if ! grep -q "\"name\":\"$context_name\"" <<<"$response"; then
    echo "context creation returned an unexpected response: $response" >&2
    exit 1
  fi

  kubectl_kind -n "$NAMESPACE" apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $consumer_name
spec:
  restartPolicy: Never
  containers:
    - name: consumer
      image: busybox:1.36
      command: ["sh", "-c", "echo quickstart > /context/ready && sleep 3600"]
      volumeMounts:
        - name: context
          mountPath: /context
  volumes:
    - name: context
      persistentVolumeClaim:
        claimName: context-$context_name
EOF

  kubectl_kind -n "$NAMESPACE" wait --for=condition=Ready "pod/$consumer_name" --timeout=2m
  kubectl_kind -n "$NAMESPACE" wait --for=jsonpath='{.status.phase}'=Bound "pvc/context-$context_name" --timeout=2m

  response="$(curl --silent --show-error --fail --connect-timeout 2 --max-time 10 \
    "$endpoint/v1/namespaces/$NAMESPACE/contexts/$context_name")"
  if ! grep -q '"status":"ready"' <<<"$response"; then
    echo "context did not become ready: $response" >&2
    exit 1
  fi

  kubectl_kind -n "$NAMESPACE" delete pod "$consumer_name" --wait=true >/dev/null
  curl --silent --show-error --fail --connect-timeout 2 --max-time 10 --request DELETE \
    "$endpoint/v1/namespaces/$NAMESPACE/contexts/$context_name" >/dev/null
  kubectl_kind -n "$NAMESPACE" wait --for=delete "pvc/context-$context_name" --timeout=2m

  CS_URL="$endpoint" CS_STORAGE_CLASS=local-path \
    "$ROOT_DIR/bin/contextctl" sandbox-pool create "$pool_name"
  CS_URL="$endpoint" "$ROOT_DIR/bin/contextctl" sandbox-pool wait "$pool_name" --timeout 2m
  kubectl_kind -n "$NAMESPACE" get sandboxes,pvc \
    -l "context.rossoctl.io/pool=$pool_name"
  CS_URL="$endpoint" "$ROOT_DIR/bin/contextctl" sandbox-pool delete "$pool_name" >/dev/null
  trap - EXIT

  echo "Smoke test passed: context storage and sandbox pool lifecycles are working"
}

down() {
  require kind
  kind delete cluster --name "$CLUSTER_NAME"
}

case "${1:-up}" in
  up) up ;;
  smoke) smoke ;;
  down) down ;;
  -h|--help|help) usage ;;
  *) usage >&2; exit 2 ;;
esac
