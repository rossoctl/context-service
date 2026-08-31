# Context Service

Context Service provides **Agent Context Infrastructure**: the storage, execution capacity, and
lifecycle needed to make durable context available to agents. Here, context means an agent's
workspaces, memory, knowledge, artifacts, and related runtime state—not an LLM's finite context
window.

A caller describes the number of sandboxes and workspace topology it needs; Context Service
creates or claims that capacity and returns a Kubernetes selector for routing work.

```mermaid
flowchart LR
    Clients["Serverless Harness / Rossoctl"] -->|workload allocation| CS["Context Service"]
    CS -->|direct allocation| Sandboxes["Sandbox resources"]
    CS -->|warm allocation| Claims["SandboxClaims"]
    CS -->|workspace topology| Storage["PVC / CSI storage"]
    Sandboxes --> Pods["Ready sandbox Pods"]
    Claims --> Pods
```

Serverless Harness is the first integration: it requests a pool at workload start, routes runs to
the returned selector, and releases the allocation afterward. Agents do not call Context Service
or create Kubernetes resources directly.

Context Service currently supports:

- Named PVC-backed `workspace`, `memory`, `knowledge`, and `artifacts` resources
- Dedicated RWO workspaces per sandbox
- One shared RWX workspace across a sandbox pool
- An existing PVC mounted explicitly read-only or read-write
- Claims against an existing agent-sandbox WarmPool

Read the long-term [vision](VISION.md), the [core design and workflows](docs/design.md), the
[Context Service API](docs/api.md), its [examples](docs/api-examples.md), the
[Serverless Harness integration](docs/serverless-harness.md), and the
[WarmPool claim design](docs/warm-pools.md).

Status: early prototype. The API is not stable.

## Quick start with Kind

With Docker, Kind, `kubectl`, `curl`, and Go installed:

### Build `contextctl`

```sh
make build
export PATH="$PWD/bin:$PATH"
```

### Start Context Service

```sh
make kind-up
export CS_STORAGE_CLASS=local-path

contextctl sc list
```

### Create a Context

```sh
contextctl ctx create demo
contextctl ctx list
```

A new Context may show `provisioning` until a workload mounts it.

### Create a Sandbox Pool

```sh
contextctl sb create demo-pool
contextctl sb wait demo-pool
contextctl sb list
```

### See what was created

Show Context Service resources alongside their Sandboxes, PVCs, and Pods:

```sh
./hack/context-service-status.sh

# Refresh continuously
watch ./hack/context-service-status.sh
```

### Clean up

```sh
contextctl sb delete demo-pool
contextctl ctx delete demo
make kind-down
```

Context Service runs at `http://127.0.0.1:8080`. Run `make kind-smoke` to test both lifecycles
automatically. The status script runs once and supports `CS_URL`, `CS_NAMESPACE`, `KUBE_CONTEXT`,
and `CONTEXTCTL` overrides.

## Deploy to Kubernetes

```sh
kubectl apply -f deploy/context-service.yaml
kubectl -n serverless-harness rollout status deployment/context-service
```

Before production, update the namespace, Context Service image, and `CS_SANDBOX_IMAGE` in
[`deploy/context-service.yaml`](deploy/context-service.yaml).

**OpenShift:** If `restricted-v2` rejects UID `65532`, remove `runAsUser` and `runAsGroup` from the
Deployment so OpenShift can assign them.

## CLI

Build the small command-line client:

```sh
make build
export PATH="$PWD/bin:$PATH"
```

The resource aliases `ctx`, `sb`, and `sc` are available for interactive use. For example,
`contextctl sb list` is equivalent to `contextctl sandbox-pool list`.

Copy and edit the example configuration, then load it into your shell:

```sh
cp .env.example .env
# Edit .env and replace the token placeholder.
set -a
source .env
set +a
```

The local `.env` contains credentials and is ignored by Git. `.env.example` is safe to commit and documents every setting.

### Dedicated workspace per sandbox

Create one sandbox with its own 1Gi RWO workspace:

```mermaid
flowchart LR
    CS["Context Service"] --> S1["Sandbox 1"] --> P1["RWO PVC 1"]
    CS --> S2["Sandbox 2"] --> P2["RWO PVC 2"]
```

```sh
contextctl sandbox-pool create demo
contextctl sandbox-pool list
contextctl sandbox-pool wait demo
contextctl sandbox-pool get demo
contextctl sandbox-pool delete demo
```

Use `--replicas` to create multiple sandboxes. Each sandbox receives a separate RWO PVC. Deleting
the pool also deletes its managed PVCs.

```sh
contextctl sandbox-pool create review --replicas 3
```

### Shared workspace

Create two sandboxes sharing one RWX workspace:

```mermaid
flowchart LR
    CS["Context Service"] --> S1["Sandbox 1"]
    CS --> S2["Sandbox 2"]
    S1 --> P["Shared RWX PVC"]
    S2 --> P
```

```sh
contextctl sandbox-pool create demo --shared --replicas 2
contextctl sandbox-pool wait demo
contextctl sandbox-pool get demo
contextctl sandbox-pool delete demo
```

Override the sandbox count, workspace size, or storage class as needed:

```sh
contextctl sandbox-pool create review --shared --replicas 3 \
  --workspace-size 5Gi --storage-class ibm-scale-csi
```

Deleting the pool also deletes its managed shared PVC.

### Existing workspace

Attach an existing populated PVC read-only to a sandbox pool:

```mermaid
flowchart LR
    Owner["Producer or external owner"] -->|populate| P["Existing PVC"]
    CS["Context Service"] --> S1["Sandbox 1 · RO"] --> P
    CS --> S2["Sandbox 2 · RO"] --> P
    CS -. never deletes .-> P
```

```sh
contextctl sandbox-pool create readers --claim prepared-workspace --read-only --replicas 3
contextctl sandbox-pool wait readers
contextctl sandbox-pool delete readers
```

Attach it read-write instead:

```sh
contextctl sandbox-pool create writer --claim prepared-workspace --read-write
```

`--claim` requires exactly one of `--read-only` or `--read-write`. Deleting either pool removes its sandboxes but does not delete the externally owned PVC.

### WarmPool claims

Claim ready sandboxes from an existing agent-sandbox `SandboxWarmPool`:

```mermaid
flowchart LR
    T["SandboxTemplate"] --> W["SandboxWarmPool"]
    W --> S1["Warm Sandbox"]
    W --> S2["Warm Sandbox"]
    CS["Context Service"] --> C1["SandboxClaim 1"] --> S1
    CS --> C2["SandboxClaim 2"] --> S2
```

```sh
contextctl sandbox-pool create fast-run --warm-pool research-agents --replicas 3
contextctl sandbox-pool wait fast-run
contextctl sandbox-pool delete fast-run
```

WarmPool claims use the image, environment, and storage already configured by the pool's
`SandboxTemplate`. Deleting the allocation removes its `SandboxClaim` resources; the upstream
controller manages the claimed Sandboxes and replenishes the WarmPool. `--warm-pool` cannot be
combined with Context Service workspace options.

Run `contextctl help` or `contextctl help sandbox-pool create` for defaults, options, and
examples.
