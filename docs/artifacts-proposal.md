# Artifact storage proposal

Status: proposal; not implemented.

## Purpose

Context Service currently gives every workload a filesystem workspace. Workloads also need an
optional destination for completed results such as reports, verdicts, logs, and datasets.

The distinction is lifecycle rather than file format:

- A **workspace** is mutable working state used while agents run.
- **Artifacts** are outputs intended to be retained, shared, or published after the workload.

## Initial API shape

Keep the existing `workspace` contract and add an optional sibling to `POST /workloads`:

```json
{
  "name": "bugstone",
  "replicas": 3,
  "workspace": {
    "size": "5Gi",
    "accessMode": "ReadWriteMany",
    "storageClass": "ibm-scale-csi"
  },
  "artifacts": {
    "type": "filesystem",
    "size": "10Gi",
    "accessMode": "ReadWriteMany",
    "storageClass": "ibm-scale-cache-s3",
    "retain": true
  }
}
```

Both resources must be declared before sandboxes are created because Kubernetes cannot attach a
new PVC to a running Pod. A filesystem artifact store is mounted at `/artifacts`; the existing
workspace remains mounted at `/workspace`.

## Two object-storage paths

Artifact storage should not depend on IBM Storage Scale. The service should eventually support two
ways to reach the same durable object store.

### Direct object access

```text
Agent --> S3 API --> object storage
```

The workload uses an S3-compatible SDK or CLI. Context Service supplies connection metadata and a
reference to Kubernetes credentials; it does not return secret values. The artifact declaration
identifies a bucket and workload-specific prefix. Whether Context Service creates buckets or only
uses existing ones remains an open design decision.

Direct access is the portable baseline. It works without a filesystem or a vendor-specific CSI
feature, but the application must understand object APIs and their semantics.

### Cached filesystem access

```text
Agent --> /artifacts --> Scale cache volume --> S3-compatible object storage
```

An IBM Storage Scale CSI cache volume presents object-backed storage as a mounted filesystem.
Agents use ordinary file operations while Storage Scale manages data movement and caching. This can
improve repeated access and aggregate throughput while preserving the same durable object-storage
destination.

The CSI-specific `volumeType: cache` remains inside the StorageClass. The Context Service API
selects a storage class or a future friendly storage profile; it does not expose raw CSI driver
parameters.

## Intended story

```text
Portable baseline:  workload --> direct S3 access
Optimized option:   workload --> Scale cache volume --> S3
```

This makes the value of Scale measurable rather than mandatory: run the same artifact workload
with direct S3, then use a cache volume to demonstrate transparent filesystem access and improved
performance.

## Proposed sequence

1. Prove direct S3 upload and retrieval manually in a sandbox.
2. Prove an IBM Storage Scale cache-volume PVC against the same object store.
3. Compare behavior and performance using the same artifact workload.
4. Implement only the smallest Context Service contract supported by those experiments.

The first implementation should keep `artifacts` optional and separate from `workspace`. A generic
multi-volume context model can wait until additional use cases demonstrate that it is needed.
