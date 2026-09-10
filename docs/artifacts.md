# Artifact contexts

An `artifacts` context contains immutable outputs such as reports, patches, datasets, logs, and
build products. A workspace is mutable while an agent works; publishing copies selected results
into content-addressed artifact storage with provenance.

```sh
contextctl ctx artifact publish release-output dist/ \
  --from project-workspace \
  --producer agent:builder

contextctl ctx artifact list release-output
contextctl ctx artifact get release-output report.pdf
```

Publishing a file records its media type, size, SHA-256 checksum, producer, and exact source context
revision. Publishing the same bytes again is idempotent. Publishing new bytes under the same name
creates another immutable version; retrieve one explicitly with `NAME@VERSION`.

## Layout

```text
~/.contexts/release-output/
├── manifest.json
└── artifacts/
    ├── index.json
    └── objects/
        └── SHA256
```

Files and directories are accepted. Symlinks and special files are rejected. Retrieval verifies
the checksum and refuses to overwrite an existing destination.

Artifact contexts use the existing `.context` export/import and sync paths. Large artifact bundles
can therefore be transferred to S3 with `contextctl ctx sync push NAME --to s3://bucket/prefix`;
artifact bytes are never embedded in Context Service API objects.

See the [producer and consumer demo](../demos/local/artifact-publishing/).
