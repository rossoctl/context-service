# Context Service API

Status: early prototype; the API is not yet stable.

Context Service provides the API for Agent Context Infrastructure. It accepts workload-scoped
infrastructure intent rather than Kubernetes manifests. A client requests sandbox capacity and a
workspace topology. Context Service creates or claims the resources and returns a Kubernetes
selector for routing work.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/healthz` | Service health |
| `GET` | `/v1/storage-classes` | List storage choices available for context resources |
| `POST` | `/v1/contexts` | Create a named PVC-backed context resource |
| `GET` | `/v1/namespaces/{namespace}/contexts` | List named context resources |
| `GET` | `/v1/namespaces/{namespace}/contexts/{name}` | Read a named context resource |
| `GET` | `/v1/namespaces/{namespace}/contexts/{name}/revisions` | List published context revisions |
| `POST` | `/v1/namespaces/{namespace}/contexts/{name}/revisions` | Publish a verified context revision |
| `PUT` | `/v1/namespaces/{namespace}/contexts/{name}/query-index` | Publish query records for the current revision |
| `POST` | `/v1/namespaces/{namespace}/query` | Query accessible memory and knowledge contexts |
| `GET`, `PUT`, `DELETE` | `/v1/namespaces/{namespace}/contexts/{name}/grants` | Inspect, set, or revoke access |
| `GET`, `PUT`, `DELETE` | `/v1/namespaces/{namespace}/contexts/{name}/consumers` | Inspect, attach, or detach consumers |
| `GET` | `/v1/namespaces/{namespace}/contexts/{name}/audit` | Read grant and attachment events |
| `DELETE` | `/v1/namespaces/{namespace}/contexts/{name}` | Delete a named context resource |
| `POST` | `/v1/sandbox-pools` | Create an allocation |
| `GET` | `/v1/sandbox-pools` | List allocations |
| `GET` | `/v1/sandbox-pools/{name}` | Read allocation status |
| `DELETE` | `/v1/sandbox-pools/{name}` | Release an allocation |

The allocation `name` is its stable identity. Creation is rejected with `409` if owned resources
already exist under that name.

### Storage-class discovery

`GET /v1/storage-classes` returns a stable, purpose-built view of the Kubernetes
StorageClasses that callers can select. It does not expose raw Kubernetes objects:

```json
{
  "items": [
    {
      "name": "ibm-scale-csi",
      "default": false,
      "provisioner": "spectrumscale.csi.ibm.com",
      "volumeBindingMode": "Immediate",
      "reclaimPolicy": "Delete",
      "allowVolumeExpansion": true
    }
  ]
}
```

StorageClass objects do not declare supported PVC access modes, so the response
does not claim whether a class supports `ReadWriteOnce` or `ReadWriteMany`.

## Named context resources

Named resources let an integration provision storage independently from sandbox capacity. The
initial implementation supports five classifications over the same PVC-backed contract:
`workspace`, `state`, `memory`, `knowledge`, and `artifacts`. Classification is metadata today; it
does not yet change provisioning or lifecycle semantics. `state` contains native harness data;
`memory` is reserved for portable facts and summaries retained across sessions.

```json
{
  "name": "research-memory",
  "namespace": "team1",
  "type": "memory",
  "storage": {
    "backend": "pvc",
    "size": "5Gi",
    "accessMode": "ReadWriteMany",
    "storageClass": "ibm-scale-csi"
  }
}
```

Creation returns the stable PVC attachment that a runtime can mount:

```json
{
  "name": "research-memory",
  "namespace": "team1",
  "type": "memory",
  "status": "provisioning",
  "storage": {
    "backend": "pvc",
    "size": "5Gi",
    "accessMode": "ReadWriteMany",
    "storageClass": "ibm-scale-csi"
  },
  "attachment": {
    "kind": "pvc",
    "claimName": "context-research-memory"
  }
}
```

Deletion removes the managed PVC. Consumers should treat `attachment.kind` as a discriminator so
future storage backends can use a different attachment contract.

Successful sync publishes a content-addressed revision containing its creation time, producer,
source revisions, transformation parameters, and file totals. `GET .../revisions` returns the
ordered history without mounting the PVC. See [Context revisions and provenance](context-revisions.md).

## Sandbox-pool create request

```json
{
  "name": "shared-review",
  "replicas": 3,
  "sandboxProfile": "developer",
  "workspace": {
    "size": "5Gi",
    "accessMode": "ReadWriteMany",
    "storageClass": "ibm-scale-csi"
  }
}
```

Exactly one allocation strategy is selected by the request:

- Managed `ReadWriteOnce` workspace: one PVC per sandbox
- Managed `ReadWriteMany` workspace: one shared PVC
- Existing PVC: `claimName` with an explicit `readOnly` value
- Existing WarmPool: `warmPoolRef` with no workspace settings

`sandboxProfile` is optional and names a platform-managed `SandboxTemplate` in the Context
Service namespace. Context Service copies its Sandbox runtime blueprint and injects the selected
workspace into the first container at `/workspace`. If omitted, Context Service uses its built-in
runtime configured by `CS_SANDBOX_IMAGE`.

The profile must define at least one container. It must not define `volumeClaimTemplates` or a
volume or volume mount named `workspace`; persistent workspace storage is requested through the
Context Service API. `sandboxProfile` cannot be combined with `warmPoolRef`; a WarmPool already
selects its own `SandboxTemplate`.

## Sandbox-pool list response

`GET /v1/sandbox-pools` returns every pool currently represented by managed Sandboxes,
SandboxClaims, or workspace PVCs:

```json
{
  "items": [
    {
      "name": "shared-review",
      "status": "ready",
      "replicas": 3,
      "readyReplicas": 3,
      "sandboxSelector": "context.rossoctl.io/pool=shared-review",
      "sandboxProfile": "developer",
      "workspace": {
        "size": "5Gi",
        "accessMode": "ReadWriteMany",
        "storageClass": "ibm-scale-csi"
      },
      "resources": [
        {"kind": "sandbox", "name": "sandbox-shared-review-0", "status": "Ready"},
        {"kind": "pod", "name": "sandbox-shared-review-0", "status": "Running"},
        {"kind": "pvc", "name": "shared-review-workspace", "status": "Bound"}
      ]
    }
  ]
}
```

`resources` groups the Kubernetes objects behind each logical pool. Resource status is a snapshot;
clients should use the pool's `status` and `readyReplicas` fields for allocation readiness.

Workspace topology must be declared before sandbox creation. Kubernetes cannot add a PVC mount to
an already-running Pod.

See [API examples](api-examples.md) for complete requests and topology diagrams.

## Creation response

```json
{
  "name": "shared-review",
  "status": "provisioning",
  "replicas": 3,
  "readyReplicas": 0,
  "sandboxSelector": "context.rossoctl.io/pool=shared-review",
  "sandboxProfile": "developer",
  "workspace": {
    "size": "5Gi",
    "accessMode": "ReadWriteMany",
    "storageClass": "ibm-scale-csi"
  }
}
```

Context Service applies the allocation name to managed Sandboxes, PVCs, and SandboxClaims:

```text
CS name:          shared-review
Kubernetes label: context.rossoctl.io/pool=shared-review
CS selector:      context.rossoctl.io/pool=shared-review
```

The `sandboxSelector` is the complete label selector a runtime uses to find eligible Sandbox Pods.
`status` becomes `ready` when `readyReplicas` equals `replicas`.

Serverless Harness exposes this allocation as a `workloadId`. Its identity and Redis behavior are
described in [Serverless Harness integration](serverless-harness.md).

## Release behavior

Successful deletion returns `204 No Content`.

- Managed Sandboxes and PVCs are deleted.
- An existing PVC referenced by `workspace.claimName` is never deleted.
- A WarmPool allocation deletes its SandboxClaims, not the WarmPool or SandboxTemplate.

## Validation and errors

- `name` must be a lowercase Kubernetes name of at most 50 characters.
- `replicas` must be between 1 and 100.
- `sandboxProfile`, when set, must name an existing `SandboxTemplate`.
- Managed workspaces require a positive Kubernetes storage quantity.
- Managed `accessMode` must be `ReadWriteOnce` or `ReadWriteMany`.
- Unknown JSON fields are rejected.
- Request bodies are limited to 1 MiB.

```json
{
  "error": "invalid_request",
  "message": "workspace.size is required"
}
```

| Status | Error | Meaning |
|---|---|---|
| `400` | `invalid_request` | Malformed or unsupported request |
| `404` | `not_found` | Allocation not found |
| `409` | `already_exists` | Allocation resources already exist |
| `409` | `in_use` | Context has declared or active consumers |
| `501` | `unsupported` | The storage backend cannot perform the requested lifecycle operation |
| `500` | `internal_error` | Kubernetes or service failure |

The service expects authentication at its ingress gateway. `contextctl` sends its configured token
as `X-SH-Auth` and `CS_SUBJECT` as `X-Context-Subject`. A production gateway must strip untrusted
subject headers and inject the authenticated `kind:name` identity. See [Context access and
consumers](context-access.md) for grants, Kubernetes service-account mapping, discovery, and safe
deletion.

Object-storage artifact backends are not implemented. The current `artifacts` context type is
PVC-backed classification only. See the [artifact storage proposal](artifacts-proposal.md).

# Context lifecycle

```text
POST /v1/namespaces/{namespace}/contexts/{name}/snapshots
GET  /v1/namespaces/{namespace}/contexts/{name}/snapshots
GET  /v1/namespaces/{namespace}/contexts/{name}/snapshots/{snapshot}
POST /v1/namespaces/{namespace}/contexts/{name}/clones
POST /v1/namespaces/{namespace}/contexts/{name}/restore
PUT  /v1/namespaces/{namespace}/contexts/{name}/retention
POST /v1/namespaces/{namespace}/contexts/{name}/gc?dryRun=true
GET  /v1/namespaces/{namespace}/contexts/{name}/capabilities
```

Lifecycle routes use the same subject header and access rules as contexts. Snapshot creation and
restore require `write`, clone requires `derive`, inspection requires `read`, and retention or
garbage collection requires `administer`.

## Memory and knowledge query

`POST /v1/namespaces/{namespace}/query` accepts query text or exact record IDs, optional context,
type, source-context, and revision filters, a limit from 1 to 100, and an opaque cursor. Results are
ordered by descending score and stable record identity. Each result contains both its owning
context revision and source provenance. `unavailable` identifies authorized contexts whose index
does not match their current revision.

`PUT /v1/namespaces/{namespace}/contexts/{name}/query-index` publishes records for a successfully
synced `memory` or `knowledge` revision. The revision must match the context's current revision.
