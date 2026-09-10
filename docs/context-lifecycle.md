# Context lifecycle

Snapshots preserve an immutable context revision. A snapshot can become a new writable context, or
restore an existing local context. Restoring never silently discards newer local content: Context
Service first creates a protected safety snapshot.

```sh
contextctl ctx snapshot create demo baseline
contextctl ctx snapshot clone demo@baseline experiment
contextctl ctx snapshot restore demo baseline
```

The default `auto` backend uses a filesystem context when one exists locally; otherwise it uses the
PVC context with the same name. Use `--backend filesystem` or `--backend pvc` to choose explicitly.

## Retention

Retention keeps snapshots that match either rule. Protected snapshots and revisions referenced by
clones or restores are always kept.

```sh
contextctl ctx snapshot retention demo --keep-last 5 --max-age 168h
contextctl ctx snapshot gc demo --dry-run
contextctl ctx snapshot gc demo
```

Local snapshots are checksummed `.context` bundles stored below the context's `.snapshots`
directory. They work without Kubernetes or a particular storage driver.

PVC snapshots use the Kubernetes `VolumeSnapshot` API. Context Service selects a
`VolumeSnapshotClass` matching the PVC's CSI driver, or accepts `--snapshot-class`. Check a PVC
context before using the feature:

```sh
contextctl ctx snapshot capabilities demo
```

CSI snapshots can be cloned into new writable contexts. Generic CSI does not provide safe,
in-place replacement of an existing PVC; PVC restore therefore returns a clear unsupported error
and directs the user to clone instead.

The HTTP endpoints are listed in [the API reference](api.md). A runnable filesystem walkthrough is
in [the lifecycle demo](../demos/local/context-lifecycle/).
