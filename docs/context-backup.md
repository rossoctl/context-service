# Automatic context backup

Automatic backup continuously copies one local filesystem context to one remote target. It is a
one-way safety copy: remote changes are not pulled into the active local context.

```text
harness hook ──capture──▶ ~/.contexts/demo ──backup──▶ S3 or Context Service PVC
                                │                        └── current verified revision
                                └── periodic scan
```

## Start and inspect a backup

Use an S3-compatible target:

```sh
contextctl ctx backup start demo --to s3://contexts/demo
contextctl ctx backup status demo
contextctl ctx backup stop demo
```

Or use a PVC-backed Context Service context:

```sh
contextctl ctx create cloud-demo --type state
contextctl ctx backup start demo --to pvc://serverless-harness/cloud-demo
```

The background worker runs immediately, after harness capture events, and at the configured
interval. Rapid events are debounced and transfers for one context never overlap. If the portable
content is unchanged, no object is uploaded. Failures preserve the last valid remote revision and
retry with bounded exponential backoff.

Backup configuration and status are stored under `~/.contexts/NAME/.backup/`. Status reports the
last attempt, last successful source revision, file and byte counts, and the latest error. Logs are
written to the path displayed by `contextctl ctx backup status`.

Credentials are inherited by the background worker. S3 uses the standard AWS credential chain and
the `CS_S3_ENDPOINT` and `CS_S3_REGION` settings described in [Context portability](context-portability.md).
PVC backup requires the same Context Service and Kubernetes access as manual PVC sync.

`contextctl ctx backup run NAME` runs the worker in the foreground for a container, service manager,
or troubleshooting. Restarting a worker reuses its persisted successful source revision.
