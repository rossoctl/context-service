# Moca integration

Moca is the workload-facing control plane. It creates and selects the execution
environments (sandboxes) that run agents. Context Service stores the context those environments
use.

```text
Agent workload --> Moca /runs           --> sandbox lease and execution
Moca           --> Context Service /v1/contexts --> PVC mounted by the sandbox
```

Moca gets context into a sandbox in one of two ways:

- **Bundle delivery** for Moca's shared, long-running sandboxes, which can't mount a new PVC. Used
  today.
- **PVC attachment** for sandboxes Moca creates per workload, planned in
  [rossoctl/moca#476](https://github.com/rossoctl/moca/issues/476).

## PVC attachment

Moca creates or reads a named context and mounts the returned PVC in a sandbox it created for that
workload:

```text
POST /v1/contexts      {"name": "shared-review", "namespace": "team1", "type": "workspace", ...}
attachment             {"kind": "pvc", "claimName": "context-shared-review"}
```

Moca owns sandbox creation, routing, Redis leases, and release. Context Service owns the context's
lifecycle, revisions, snapshots, access, backup, and sync. Deleting a sandbox never deletes its
context; delete the context explicitly when it is no longer needed.

## Bundle delivery

Moca creates the Context, has the client upload a bundle, freezes the revision, and downloads that
revision as a bundle that it copies into the sandbox. See
[How context uploads work](context-upload.md).

## Migration from sandbox pools

Earlier integrations called `/v1/sandbox-pools` so that Context Service created Sandboxes and
workspace PVCs. That API has been removed. See [Removed: sandbox pools](api.md#removed-sandbox-pools).
