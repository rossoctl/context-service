# Context access

Start the Kind environment, then create a private context as its owner:

```sh
make kind-up
export CS_STORAGE_CLASS=local-path
export CS_SUBJECT=user:owner
contextctl ctx create private --type state
```

Give one agent read-only access and another read-write access:

```sh
contextctl ctx grant private --subject agent:reader --permissions read,attach
contextctl ctx grant private --subject agent:writer --permissions read,write,attach

CS_SUBJECT=agent:reader contextctl ctx access private
CS_SUBJECT=agent:writer contextctl ctx access private
CS_SUBJECT=agent:outsider contextctl ctx list --backend pvc
```

Create a real Pod that mounts the context read-only:

```sh
kubectl apply -f demos/kubernetes/context-access/reader-pod.yaml
kubectl -n serverless-harness wait --for=condition=Ready pod/context-reader --timeout=90s
contextctl ctx consumers private
```

Deletion is blocked while the Pod is using the context:

```sh
contextctl ctx delete private
kubectl -n serverless-harness delete pod/context-reader
contextctl ctx delete private
```

The first delete should return `409 Conflict`; the second succeeds.
