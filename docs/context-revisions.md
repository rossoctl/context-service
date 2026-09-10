# Context revisions and provenance

Context Service assigns a SHA-256 revision ID to each distinct set of portable context content.
Repeated capture of unchanged files keeps the same revision; changed content appends a revision to
the context history.

Each revision records:

- when and how it was produced;
- the producing harness or generator;
- file and byte counts;
- generation parameters; and
- exact source context revisions for derived memory and knowledge.

Source references are immutable provenance. If a source context is later removed, the derived
context remains valid and reports that source as missing.

```sh
contextctl ctx revisions demo
contextctl ctx lineage demo-memory
```

Use `--json` for machine-readable output. Add `--backend pvc` and `--namespace NAME` to inspect
revision history published to a Context Service PVC.

Portable `.context` bundles include the manifest's complete revision history. A successful PVC
sync publishes the same current revision through the Context Service API.

The service exposes revision history at:

```text
GET  /v1/namespaces/{namespace}/contexts/{name}/revisions
POST /v1/namespaces/{namespace}/contexts/{name}/revisions
```

The POST endpoint records a verified revision after content has been published to the backing
store. Revision IDs identify portable content, not the compressed bundle used to transport it.
