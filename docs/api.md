# Context Service API

Status: early prototype; the API is not yet stable.

Context Service provides the API for Agent Context Infrastructure. It accepts workload-scoped
infrastructure intent rather than Kubernetes manifests. A client requests named context storage;
Context Service creates the PVC and returns the attachment a runtime mounts. Execution environments
(sandboxes) are owned by the runtime, such as Moca, not by Context Service.

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

The context `name` is its stable identity within a namespace. Creation is rejected with `409` if a
context with that name already exists.

### Removed: sandbox pools

The `/v1/sandbox-pools` endpoints, sandbox profiles (`SandboxTemplate`), and WarmPool claims have
been removed; those routes now return `404`. Context Service no longer creates, claims, or deletes
`agents.x-k8s.io` Sandbox resources, and `CS_SANDBOX_IMAGE` is ignored. To migrate, create a context
with `POST /v1/contexts` and mount the returned `attachment.claimName` PVC in the execution
environment your runtime creates. Existing context PVCs are unaffected. Sandboxes and workspace PVCs
created by earlier versions are not deleted; remove them with `kubectl` once no longer needed.

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

Named resources let an integration provision storage independently from its execution environment. The
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

## Validation and errors

- `name` must be a lowercase Kubernetes name of at most 50 characters.
- `type` must be `workspace`, `state`, `memory`, `knowledge`, or `artifacts`.
- `storage.size` must be a positive Kubernetes storage quantity.
- `storage.accessMode` must be `ReadWriteOnce` or `ReadWriteMany`.
- Unknown JSON fields are rejected.
- Request bodies are limited to 1 MiB.

```json
{
  "error": "invalid_request",
  "message": "storage.size must be a positive Kubernetes quantity"
}
```

| Status | Error | Meaning |
|---|---|---|
| `400` | `invalid_request` | Malformed or unsupported request |
| `404` | `not_found` | Context or snapshot not found |
| `409` | `already_exists` | Context resources already exist |
| `409` | `in_use` | Context has declared or active consumers |
| `501` | `unsupported` | The storage backend cannot perform the requested lifecycle operation |
| `500` | `internal_error` | Kubernetes or service failure |

The service expects authentication at its ingress gateway. `contextctl` sends its configured token
as `X-SH-Auth` and `CS_SUBJECT` as `X-Context-Subject`. A production gateway must strip untrusted
subject headers and inject the authenticated `kind:name` identity. See [Context access and
consumers](context-access.md) for grants, Kubernetes service-account mapping, discovery, and safe
deletion.

Artifact publication is a local `contextctl` workflow whose portable bundles use the existing PVC
and S3 synchronization paths. See [Artifact contexts](artifacts.md).

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
