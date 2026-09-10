# Automatic context backup

Capture a real Claude session and keep a verified backup in local S3-compatible storage.

## Start local object storage

From the repository root:

```sh
docker compose -f demos/local/context-portability/minio.compose.yaml up -d minio
docker compose -f demos/local/context-portability/minio.compose.yaml run --rm create-bucket

export AWS_ACCESS_KEY_ID=context-service
export AWS_SECRET_ACCESS_KEY=context-service-demo
export CS_S3_ENDPOINT=http://127.0.0.1:9000
export CS_S3_REGION=us-east-1
```

## Capture and back up Claude

```sh
make build
export PATH="$PWD/bin:$PATH"
mkdir -p /tmp/context-backup-demo
cd /tmp/context-backup-demo

contextctl ctx create demo --type state --backend filesystem
contextctl ctx attach demo --harness claude
contextctl ctx backup start demo --to s3://contexts/demo --interval 1m
claude
```

Ask Claude to create a short project plan, then exit. The harness hook captures the session and
signals the backup worker.

```sh
contextctl ctx backup status demo
```

Open `http://127.0.0.1:9001` with the demo credentials to see `.context-service/current` and the
checksummed `.context` object. Continue the Claude session and check status again to see the next
revision.

## Stop

```sh
contextctl ctx backup stop demo
contextctl ctx detach demo --harness claude
cd -
docker compose -f demos/local/context-portability/minio.compose.yaml down -v
```

See [Automatic context backup](../../../docs/context-backup.md) for behavior, status, retries, and
the PVC target form.
