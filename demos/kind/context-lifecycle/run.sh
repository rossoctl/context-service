#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-context-service}"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="${CS_NAMESPACE:-serverless-harness}"
HOST_PORT="${CONTEXT_SERVICE_PORT:-8080}"
CSI_HOSTPATH_VERSION="v1.18.0"
SNAPSHOTTER_VERSION="v8.6.0"

kctl() { kubectl --context "$KUBE_CONTEXT" "$@"; }
cctl() {
  CS_URL="http://127.0.0.1:${HOST_PORT}" CS_NAMESPACE="$NAMESPACE" \
    "$ROOT_DIR/bin/contextctl" "$@"
}

# start_pod NAME CONTEXT mounts the context PVC at /workspace in a plain Pod.
start_pod() {
  kctl -n "$NAMESPACE" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $1
spec:
  restartPolicy: Never
  containers:
    - name: shell
      image: busybox:1.36
      command: ["sh", "-c", "sleep infinity"]
      volumeMounts:
        - {name: workspace, mountPath: /workspace}
  volumes:
    - name: workspace
      persistentVolumeClaim: {claimName: context-$2}
EOF
  kctl -n "$NAMESPACE" wait --for=condition=Ready "pod/$1" --timeout=3m >/dev/null
}

stop_pod() {
  kctl -n "$NAMESPACE" delete pod "$1" --ignore-not-found --wait=true >/dev/null 2>&1 || true
}

cleanup_demo() {
  for pod in lifecycle-consumer lifecycle-source lifecycle-producer; do
    stop_pod "$pod"
  done
  cctl ctx delete lifecycle-copy --backend pvc >/dev/null 2>&1 || true
  cctl ctx delete lifecycle-demo --backend pvc >/dev/null 2>&1 || true
  kctl -n "$NAMESPACE" delete volumesnapshot context-lifecycle-demo-baseline --ignore-not-found >/dev/null
}

install_snapshot_storage() {
  local base="https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}"
  echo "Installing snapshot APIs and controller..."
  kctl apply -f "${base}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml" >/dev/null
  kctl apply -f "${base}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml" >/dev/null
  kctl apply -f "${base}/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml" >/dev/null
  kctl wait --for=condition=Established crd/volumesnapshots.snapshot.storage.k8s.io --timeout=2m >/dev/null
  kctl apply -f "${base}/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml" >/dev/null
  kctl apply -f "${base}/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml" >/dev/null
  kctl -n kube-system rollout status deployment/snapshot-controller --timeout=3m >/dev/null

  if ! kctl get csidriver hostpath.csi.k8s.io >/dev/null 2>&1; then
    echo "Installing the CSI hostpath test driver..."
    work_dir="$(mktemp -d)"
    kubeconfig_file="$(mktemp)"
    trap 'rm -rf "$work_dir"; rm -f "$kubeconfig_file"' RETURN
    kind get kubeconfig --name "$CLUSTER_NAME" >"$kubeconfig_file"
    curl -fsSL "https://github.com/kubernetes-csi/csi-driver-host-path/archive/refs/tags/${CSI_HOSTPATH_VERSION}.tar.gz" -o "$work_dir/driver.tar.gz"
    mkdir -p "$work_dir/driver"
    tar -xzf "$work_dir/driver.tar.gz" -C "$work_dir/driver" --strip-components=1
    KUBECONFIG="$kubeconfig_file" "$work_dir/driver/deploy/kubernetes-1.34/deploy.sh" >/dev/null
    KUBECONFIG="$kubeconfig_file" kubectl apply -f "$work_dir/driver/examples/csi-storageclass.yaml" >/dev/null
    rm -rf "$work_dir"
    rm -f "$kubeconfig_file"
    trap - RETURN
  fi
  kctl annotate volumesnapshotclass csi-hostpath-snapclass snapshot.storage.kubernetes.io/is-default-class=true --overwrite >/dev/null
}

if [[ "${1:-}" == "clean" ]]; then
  cleanup_demo
  echo "Context lifecycle demo resources removed."
  exit 0
fi

install_snapshot_storage
cleanup_demo

echo "Creating a CSI-backed context and producer..."
cctl ctx create lifecycle-demo --storage-class csi-hostpath-sc >/dev/null
start_pod lifecycle-producer lifecycle-demo
kctl -n "$NAMESPACE" exec lifecycle-producer -- sh -c 'printf "saved by producer\n" > /workspace/handoff.txt && sync'
stop_pod lifecycle-producer

echo "Creating immutable snapshot baseline..."
cctl ctx snapshot create lifecycle-demo baseline --backend pvc

echo "Changing the source after the snapshot..."
start_pod lifecycle-source lifecycle-demo
kctl -n "$NAMESPACE" exec lifecycle-source -- sh -c 'printf "newer source data\n" > /workspace/newer.txt && sync'
stop_pod lifecycle-source

echo "Cloning baseline into a writable context..."
cctl ctx snapshot clone lifecycle-demo@baseline lifecycle-copy --backend pvc >/dev/null
start_pod lifecycle-consumer lifecycle-copy
value="$(kctl -n "$NAMESPACE" exec lifecycle-consumer -- cat /workspace/handoff.txt)"
[[ "$value" == "saved by producer" ]]
if kctl -n "$NAMESPACE" exec lifecycle-consumer -- test -e /workspace/newer.txt >/dev/null 2>&1; then
  echo "snapshot isolation failed: clone contains newer source data" >&2
  exit 1
fi
kctl -n "$NAMESPACE" exec lifecycle-consumer -- sh -c 'printf "clone only\n" > /workspace/clone.txt && sync'

echo
cctl ctx snapshot list lifecycle-demo --backend pvc
echo
echo "Verified immutable snapshot content and writable clone isolation."
