# Moca integration

Moca is the workload-facing control plane. It creates and selects the execution
environments (sandboxes) that run agents. Context Service stores the context those environments
use.

```text
Agent workload --> Moca /runs           --> sandbox lease and execution
Moca           --> Context Service /v1/contexts --> PVC mounted by the sandbox
```

## Context attachment

Moca creates or reads a named context and mounts the returned PVC in its own execution environment:

```text
POST /v1/contexts      {"name": "shared-review", "namespace": "team1", "type": "workspace", ...}
attachment             {"kind": "pvc", "claimName": "context-shared-review"}
```

Moca owns sandbox creation, routing, Redis leases, and release. Context Service owns the context's
lifecycle, revisions, snapshots, access, backup, and sync. Deleting a sandbox never deletes its
context; delete the context explicitly when it is no longer needed.

## Migration from sandbox pools

Earlier integrations called `/v1/sandbox-pools` so that Context Service created Sandboxes and
workspace PVCs. That API has been removed. See [Removed: sandbox pools](api.md#removed-sandbox-pools).
