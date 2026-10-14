# Context portability

Context portability moves captured agent state between local filesystems and PVC-backed Context
Service storage.

## Portable bundles

`contextctl ctx export NAME` creates a compressed `.context` bundle containing:

- the context manifest;
- native harness state under `harnesses/`, derived `memory/`, `knowledge/`, and `artifacts/`
  content, or plain files under `workspace/`; and
- a SHA-256 inventory in `checksums.json`.

Machine-specific attachments and paths are not used to attach the imported context. Import verifies
the inventory, rejects unsafe archive entries, and installs into a new context without overwriting an
existing one. Revision history and derivation provenance remain part of the portable manifest.
Bundles use consumer-readable permissions so non-root agents can read them from a read-only mount:
directories are `0755`, a file whose owner-execute bit is set is `0755`, and every other file is
`0644`. Import applies the same rule, so an executable script stays executable. Setuid, setgid,
sticky, and group or world write bits are never stored or restored. Local imports remain private:
`0700` directories, `0700` executable files, and `0600` other files.

```text
~/.contexts/demo                 demo.context                 another machine
├── manifest.json      export   ├── manifest.json   import   ~/.contexts/demo
└── harnesses/         ───────▶ ├── checksums.json ───────▶ ├── manifest.json
                               └── harnesses/                └── harnesses/
```

## Bundle a directory

`contextctl ctx pack DIR -o FILE` builds a `workspace` bundle from an ordinary directory, such as a
small example repository, so that Moca can upload it without first capturing a local context. Files
are stored under `workspace/`, and the context name defaults to the directory's base name.

```sh
contextctl ctx pack ./demo-repo -o demo-repo.context
contextctl ctx pack ./demo-repo -o demo-repo.context --name kubecon-demo --json
```

- `.git` directories and files are skipped unless `--include-git` is set.
- Symbolic links and special files are rejected, as they are by export.
- The upload limits are checked while packing, so a bundle that packs is not rejected for size at
  upload: 10,000 tar entries, 256 MiB per file, 1 GiB of expanded content, and 256 MiB compressed.
  A small PVC can still lower the expanded limit to 90 percent of its request.
- `--type` accepts only `workspace`. Use `ctx export` for captured state and derived contexts.
- `--json` prints `path`, `name`, `type`, `files`, `bytes`, and `revision`. The revision is the
  one Context Service reports after the upload.

A bundle that contains `workspace/` is rejected by a Context Service built before the root existed.
Upgrade the server before clients upload packed bundles.

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

Bundle a plain directory as a workspace context:

```sh
contextctl ctx pack ./demo-repo -o demo-repo.context
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
