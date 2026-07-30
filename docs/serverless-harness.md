# Serverless Harness integration

Serverless Harness is the workload-facing control plane. Context Service is the allocation layer.
Agent workflows call SH; SH calls Context Service when workload-scoped allocation is enabled.

```text
Agent workload --> SH /workloads --> Context Service /v1/sandbox-pools
Agent workload --> SH /runs      --> sandbox lease and execution
```

## Identity

Context Service requires an allocation `name`. SH uses the requested workload name, or generates
one when the caller omits it.

```text
SH workloadId  <-->  CS sandbox-pool name  <-->  Kubernetes workload label
shared-review       shared-review              context.rossoctl.io/pool=shared-review
```

SH returns the CS allocation name to its caller as `workloadId`.

## Redis workload record

SH stores the workload record returned by Context Service. For `workloadId: "shared-review"`, the
entry is conceptually:

```text
Redis key
sh:workload:shared-review

Redis value
{
  "workloadId": "shared-review",
  "status": "ready",
  "replicas": 3,
  "readyReplicas": 3,
  "sandboxSelector": "context.rossoctl.io/pool=shared-review",
  "workspace": {
    "size": "5Gi",
    "accessMode": "ReadWriteMany",
    "storageClass": "ibm-scale-csi"
  }
}
```

The Redis key identifies the workload. Its JSON value contains the Kubernetes selector and the
latest allocation status returned by Context Service.

## Run routing

A run request supplies the workload identity:

```json
{
  "workloadId": "shared-review",
  "sessionId": "bugstone/leaf-1"
}
```

SH then:

1. Reads `sh:workload:shared-review` from Redis.
2. Obtains `context.rossoctl.io/pool=shared-review` from `sandboxSelector`.
3. Passes the selector to its existing Redis leasing and sandbox-routing code.
4. Executes the run in an eligible Sandbox Pod.

The agent workflow sees `workloadId`; it does not need to know the Kubernetes label.

## Persistence boundaries

Context Service does not maintain a workload database. It reconstructs allocation status from
labeled Kubernetes resources after a restart.

SH persists its workload record in Redis so later `/runs` requests can resolve `workloadId` to the
selector. Redis therefore needs durable storage if workload routing must survive loss of the Redis
Pod.

## Optional integration

When Context Service is not configured, existing SH `/runs` requests continue to use the static
`KAGENTI_SANDBOX_POOL_SELECTOR`. Workload lifecycle endpoints are available only when the optional
Context Service integration is enabled.
