# API examples

These examples call Context Service directly. Agent workloads normally use a runtime integration
such as [Moca](moca.md).

```sh
export CS_URL=https://example.test/context-service
export CS_TOKEN=replace-with-gateway-token
```

The gateway authenticates the token and supplies the verified `X-Context-Subject` identity used by
Context Service grants. The curl examples omit that deployment-specific identity injection. See
[Context access and consumers](context-access.md).

## Health

```sh
curl --fail --silent --show-error \
  -H "X-SH-Auth: $CS_TOKEN" \
  "$CS_URL/healthz"
```

## Create a context

```sh
curl --fail --silent --show-error \
  -H "X-SH-Auth: $CS_TOKEN" \
  -H "Content-Type: application/json" \
  -X POST "$CS_URL/v1/contexts" \
  -d '{
    "name": "shared-review",
    "namespace": "team1",
    "type": "workspace",
    "storage": {
      "backend": "pvc",
      "size": "5Gi",
      "accessMode": "ReadWriteMany",
      "storageClass": "ibm-scale-csi"
    }
  }'
```

The response's `attachment.claimName` is the PVC that the runtime mounts in its own execution
environment. Use `ReadWriteMany` when several Pods must mount the same context.

## List contexts

```sh
curl --fail --silent --show-error \
  -H "X-SH-Auth: $CS_TOKEN" \
  "$CS_URL/v1/namespaces/team1/contexts"
```

## Read status

```sh
curl --fail --silent --show-error \
  -H "X-SH-Auth: $CS_TOKEN" \
  "$CS_URL/v1/namespaces/team1/contexts/shared-review"
```

## Delete

```sh
curl --fail --silent --show-error \
  -H "X-SH-Auth: $CS_TOKEN" \
  -X DELETE "$CS_URL/v1/namespaces/team1/contexts/shared-review"
```

Deletion is rejected with `409` while the context has consumers. Sandbox-pool examples were removed
with the API; see [Removed: sandbox pools](api.md#removed-sandbox-pools).
