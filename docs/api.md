# Context Service API

Status: early prototype; the API is not yet stable.

Context Service accepts workload-scoped infrastructure intent rather than Kubernetes manifests. A
client requests sandbox capacity and a workspace topology. Context Service creates or claims the
resources and returns a Kubernetes selector for routing work.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/healthz` | Service health |
| `POST` | `/v1/sandbox-pools` | Create an allocation |
| `GET` | `/v1/sandbox-pools/{name}` | Read allocation status |
| `DELETE` | `/v1/sandbox-pools/{name}` | Release an allocation |

The allocation `name` is its stable identity. Creation is rejected with `409` if owned resources
already exist under that name.

## Create request

```json
{
  "name": "shared-review",
  "replicas": 3,
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
| `500` | `internal_error` | Kubernetes or service failure |

The service does not currently implement authentication. A deployment may enforce authentication
at its ingress gateway.

Artifact storage is not implemented. See the [artifact storage proposal](artifacts-proposal.md).
