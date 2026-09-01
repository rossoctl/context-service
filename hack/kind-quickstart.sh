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
readonly AGENT_SANDBOX_MANIFEST="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox-with-extensions.yaml"

usage() {
  cat <<'EOF'
Usage: hack/kind-quickstart.sh [up|demo|demo-clean|smoke|down]

  up      Create or reuse a Kind cluster and deploy Context Service.
  demo    Create example contexts and sandbox pools, then show their status.
  demo-clean
          Delete the example resources but keep the Kind cluster.
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
  kubectl_kind apply -f "$ROOT_DIR/deploy/examples/sandbox-profile.yaml"
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

contextctl_demo() {
  CS_URL="http://127.0.0.1:${HOST_PORT}" CS_STORAGE_CLASS=local-path \
    "$ROOT_DIR/bin/contextctl" "$@"
}

demo_exists() {
  contextctl_demo "$1" get "$2" >/dev/null 2>&1
}

create_demo_context() {
  local name="$1"
  local type="$2"
  local size="$3"

  if demo_exists ctx "$name"; then
    echo "Reusing context $name"
    return
  fi
  contextctl_demo ctx create "$name" --type "$type" --size "$size" >/dev/null
}

create_demo_pool() {
  local name="$1"
  shift

  if demo_exists sb "$name"; then
    echo "Reusing sandbox pool $name"
    return
  fi
  contextctl_demo sb create "$name" "$@" >/dev/null
}

demo_clean() {
  require kubectl
  require curl
  if [[ ! -x "$ROOT_DIR/bin/contextctl" ]]; then
    echo "bin/contextctl is missing; run 'make build' first" >&2
    exit 1
  fi

  wait_for_http "http://127.0.0.1:${HOST_PORT}/healthz" >/dev/null
  for pool_name in demo-solo demo-team demo-dedicated demo-shared demo-readonly; do
    contextctl_demo sb delete "$pool_name" >/dev/null 2>&1 || true
  done
  kubectl_kind -n "$NAMESPACE" delete pod demo-agent demo-storage-setup --ignore-not-found --wait=true >/dev/null
  for context_name in demo-workspace demo-memory demo-artifacts; do
    contextctl_demo ctx delete "$context_name" >/dev/null 2>&1 || true
  done
  kubectl_kind delete -f "$ROOT_DIR/deploy/kind/demo-rwx.yaml" --ignore-not-found >/dev/null
  echo "Demo resources deleted"
}

demo() {
  require kubectl
  require curl
  if [[ ! -x "$ROOT_DIR/bin/contextctl" ]]; then
    echo "bin/contextctl is missing; run 'make build' first" >&2
    exit 1
  fi

  wait_for_http "http://127.0.0.1:${HOST_PORT}/healthz" >/dev/null

  echo "Creating example contexts..."
  create_demo_context demo-workspace workspace 1Gi
  create_demo_context demo-memory memory 256Mi
  create_demo_context demo-artifacts artifacts 512Mi

  kubectl_kind -n "$NAMESPACE" apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: demo-agent
  labels:
    app.kubernetes.io/name: context-service-demo
spec:
  restartPolicy: Never
  containers:
    - name: agent
      image: busybox:1.36
      command: ["sh", "-c", "sleep infinity"]
      volumeMounts:
        - {name: workspace, mountPath: /workspace}
        - {name: memory, mountPath: /memory}
        - {name: artifacts, mountPath: /artifacts}
  volumes:
    - name: workspace
      persistentVolumeClaim: {claimName: context-demo-workspace}
    - name: memory
      persistentVolumeClaim: {claimName: context-demo-memory}
    - name: artifacts
      persistentVolumeClaim: {claimName: context-demo-artifacts}
EOF
  kubectl_kind -n "$NAMESPACE" wait --for=condition=Ready pod/demo-agent --timeout=2m >/dev/null

  echo "Creating example sandbox pools..."
  kubectl_kind apply -f "$ROOT_DIR/deploy/kind/demo-rwx.yaml" >/dev/null

  kubectl_kind -n "$NAMESPACE" apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: demo-storage-setup
spec:
  restartPolicy: Never
  containers:
    - name: setup
      image: busybox:1.36
      command:
        - sh
        - -c
        - |
          chown 65532:65532 /shared /readonly
          chmod 0770 /shared /readonly
          rm -f /shared/demo.txt /shared/.context-service-shared-check
          echo context-service-demo > /readonly/example.txt
          chown 65532:65532 /readonly/example.txt
      securityContext:
        runAsUser: 0
      volumeMounts:
        - {name: shared, mountPath: /shared}
        - {name: readonly, mountPath: /readonly}
  volumes:
    - name: shared
      hostPath:
        path: /var/context-service-demo/shared
        type: DirectoryOrCreate
    - name: readonly
      hostPath:
        path: /var/context-service-demo/readonly
        type: DirectoryOrCreate
EOF
  kubectl_kind -n "$NAMESPACE" wait --for=jsonpath='{.status.phase}'=Succeeded \
    pod/demo-storage-setup --timeout=2m >/dev/null
  kubectl_kind -n "$NAMESPACE" delete pod demo-storage-setup --wait=true >/dev/null

  create_demo_pool demo-dedicated --sandbox-profile shell --replicas 2 --workspace-size 1Gi
  create_demo_pool demo-shared --sandbox-profile shell --shared --replicas 2 \
    --workspace-size 1Gi --storage-class demo-rwx
  create_demo_pool demo-readonly --sandbox-profile shell --claim demo-readonly-workspace \
    --read-only --replicas 2
  contextctl_demo sb wait demo-dedicated --timeout 2m >/dev/null
  contextctl_demo sb wait demo-shared --timeout 2m >/dev/null
  contextctl_demo sb wait demo-readonly --timeout 2m >/dev/null

  local shared_marker="/workspace/.context-service-shared-check"
  local shared_value
  kubectl_kind -n "$NAMESPACE" exec sandbox-demo-shared-0 -- \
    sh -c "echo shared-workspace-ready > '$shared_marker'"
  shared_value="$(kubectl_kind -n "$NAMESPACE" exec sandbox-demo-shared-1 -- cat "$shared_marker" 2>/dev/null || true)"
  kubectl_kind -n "$NAMESPACE" exec sandbox-demo-shared-0 -- rm -f "$shared_marker"
  if [[ "$shared_value" != "shared-workspace-ready" ]]; then
    echo "shared workspace could not be read from both demo sandboxes" >&2
    exit 1
  fi
  if [[ "$(kubectl_kind -n "$NAMESPACE" exec sandbox-demo-readonly-0 -- cat /workspace/example.txt)" != "context-service-demo" ]]; then
    echo "read-only workspace content was not available to the demo sandbox" >&2
    exit 1
  fi
  if kubectl_kind -n "$NAMESPACE" exec sandbox-demo-readonly-0 -- touch /workspace/should-fail >/dev/null 2>&1; then
    echo "read-only workspace unexpectedly allowed a write" >&2
    exit 1
  fi

  contextctl_demo status
  echo
  echo "Explore with: contextctl status"
  echo "Clean up with: make kind-demo-clean"
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
  demo) demo ;;
  demo-clean) demo_clean ;;
  smoke) smoke ;;
  down) down ;;
  -h|--help|help) usage ;;
  *) usage >&2; exit 2 ;;
esac
