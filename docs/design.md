# Design and workflow

## Purpose

Kubernetes cannot add or remove PVCs from a running pod. A workload that needs different storage must therefore create or replace its sandbox compute with the required volumes already attached.

Context Service makes that operation available through an API. Its first responsibility is narrow: create and remove a sandbox pool together with its shared durable workspace.

## Serverless-harness workflow

1. Cluster setup installs Context Service, the agent-sandbox controller, and a suitable StorageClass.
2. Before a pipeline run, serverless-harness requests a sandbox pool.
3. Context Service creates one workspace PVC and the requested number of Sandbox resources with that PVC mounted.
4. Context Service returns the label selector for the pool.
5. Each serverless-harness run request carries that selector. The harness selects and leases a sandbox from the pool.
6. When the pipeline is finished, serverless-harness releases the pool through Context Service.

The pipeline's agent logic does not create Kubernetes resources or call Context Service directly.

## First slice

The first demonstration creates several sandboxes sharing one IBM Storage Scale RWX workspace, runs BugStone through that dynamically created pool, and removes the pool afterward.

This proves that sandbox and workspace topology can be chosen at run time instead of being fixed in cluster installation YAML.

## Current boundaries

- Context Service creates sandbox compute and attached workspace storage.
- Serverless-harness owns task dispatch, sandbox selection, leases, and agent execution.
- The agent-sandbox controller owns the Sandbox-to-Pod lifecycle.
- The CSI driver owns volume provisioning and attachment.
- Session history, semantic memory, snapshots, retention policy, and Rossoctl integration are not part of this first slice.

## Workspace ownership

Context Service supports two workspace lifecycles:

- A managed workspace is created with its sandbox pool and deleted with the pool. RWX creates one shared PVC; RWO creates one dedicated PVC per sandbox.
- An attached workspace references an existing PVC. The caller explicitly chooses a read-only or read-write mount. Context Service never deletes the external claim when the sandbox pool is released.

Read-only is a mount property rather than a PVC access mode. IBM Storage Scale CSI does not dynamically provision `ReadOnlyMany` volumes, but it does enforce read-only mounts of an existing RWX claim.
