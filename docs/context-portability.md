# Context portability

Context portability moves captured agent state between local filesystems and PVC-backed Context
Service storage.

## Portable bundles

`contextctl ctx export NAME` creates a compressed `.context` bundle containing:

- the context manifest;
- native harness state under `harnesses/` or derived `memory/`, `knowledge/`, and `artifacts/` content; and
- a SHA-256 inventory in `checksums.json`.

Machine-specific attachments and paths are not used to attach the imported context. Import verifies
the inventory, rejects unsafe archive entries, and installs into a new context without overwriting an
existing one. Revision history and derivation provenance remain part of the portable manifest.

```text
~/.contexts/demo                 demo.context                 another machine
├── manifest.json      export   ├── manifest.json   import   ~/.contexts/demo
└── harnesses/         ───────▶ ├── checksums.json ───────▶ ├── manifest.json
                               └── harnesses/                └── harnesses/
```

## PVC sync

`contextctl ctx sync push` resolves the destination PVC through the Context Service API and mounts
it in a short-lived helper Pod. The bundle is streamed through the authenticated Kubernetes Pod
proxy, then its checksum is verified before publication.

```text
local context → .context bundle → Kubernetes Pod proxy → temporary helper Pod → Context Service PVC
                                                        └── .context-service/
                                                            ├── current
                                                            └── objects/<sha256>.context
```

Objects are named by content hash. `current` is replaced atomically only after upload verification.
Concurrent uploads use separate temporary paths. Pull reads the current revision, verifies it
locally, and imports it without replacing an existing local context.

After a verified PVC push, `contextctl` also publishes the portable content revision to Context
Service. The context API can therefore report revision history without mounting the PVC.

The transport requires `kubectl` access to the cluster hosting the PVC, including permission to
create and delete helper Pods and use Pod exec and proxy endpoints. It moves captured state; it does
not unpack or attach that state inside a remote agent.

## S3 sync

S3 sync uses the same content-addressed layout under an explicit `s3://bucket/prefix` location:

```text
s3://contexts/team/demo/.context-service/
├── current
└── objects/<sha256>.context
```

Push uploads and reads back the immutable object to verify its size and SHA-256 before replacing
`current`. Pull verifies the current object before importing it. Standard AWS credentials are used;
credentials are never stored in context manifests or bundles. Set `CS_S3_ENDPOINT` to use MinIO or
another S3-compatible service. Custom endpoints use path-style bucket addressing.

## Commands

Export or import a local filesystem context:

```sh
contextctl ctx export demo
contextctl ctx import demo.context --name demo-copy
```

Push a local filesystem context to a PVC context, or pull its current revision:

```sh
contextctl ctx create cloud-demo --type state
contextctl ctx sync push demo --remote-name cloud-demo
contextctl ctx sync pull cloud-demo --name downloaded-demo
```

Push or pull through S3-compatible object storage:

```sh
contextctl ctx sync push demo --to s3://contexts/team/demo
contextctl ctx sync pull s3://contexts/team/demo --name downloaded-demo
```

Push and pull are explicit operations. Sync does not run continuously in the background.
Both commands display transferred bytes, total size, percentage, rate, and elapsed time.
