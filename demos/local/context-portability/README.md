# Context portability

Export captured agent state as a portable file or synchronize it with a PVC-backed context.

## Before you begin

Build the CLI and create a captured filesystem context named `demo`. The
[local agent state capture demo](../harness-capture/) shows the complete flow.

```sh
make build
export PATH="$PWD/bin:$PATH"
contextctl ctx get demo --backend filesystem
```

## Move a context as a file

Export a checksummed `.context` bundle:

```sh
contextctl ctx export demo
```

Copy `demo.context` to another machine, then import it:

```sh
contextctl ctx import demo.context --name demo-copy
contextctl ctx get demo-copy --backend filesystem
```

## Synchronize with Context Service

The current `kubectl` context and `CS_URL` must refer to the same cluster.

```sh
contextctl ctx create cloud-demo --type state
contextctl ctx sync push demo --remote-name cloud-demo
contextctl ctx sync pull cloud-demo --name downloaded-demo
contextctl ctx get downloaded-demo --backend filesystem
```

Push publishes a checksummed revision on the PVC. Pull verifies and imports the current revision;
it does not overwrite an existing local context.

## Synchronize with S3

Start the included MinIO environment and create its demo bucket:

```sh
docker compose -f demos/local/context-portability/minio.compose.yaml up -d minio
docker compose -f demos/local/context-portability/minio.compose.yaml run --rm create-bucket
```

Configure standard AWS credentials and the local S3-compatible endpoint:

```sh
export AWS_ACCESS_KEY_ID=context-service
export AWS_SECRET_ACCESS_KEY=context-service-demo
export CS_S3_ENDPOINT=http://127.0.0.1:9000
export CS_S3_REGION=us-east-1
```

Push the captured context, then pull it under a new local name:

```sh
contextctl ctx sync push demo --to s3://contexts/demo
contextctl ctx sync pull s3://contexts/demo --name s3-demo
contextctl ctx get s3-demo --backend filesystem
```

Open `http://127.0.0.1:9001` to inspect the stored objects, using the same demo credentials.

Stop MinIO and remove its demo data:

```sh
docker compose -f demos/local/context-portability/minio.compose.yaml down -v
```

See [Context portability](../../../docs/context-portability.md) for the bundle format and transport
design.
