# Context lineage

Start with the captured state from the [harness capture demo](../harness-capture/), then generate
memory:

```sh
contextctl ctx derive memory demo --name demo-memory --agent claude
```

Inspect the immutable revision history and its source:

```sh
contextctl ctx revisions demo
contextctl ctx revisions demo-memory
contextctl ctx lineage demo-memory
```

See the complete state → memory → knowledge relationship, attached harnesses, and storage copies:

```sh
contextctl ctx graph
```

Run another agent response in the attached project, then generate memory under a new name. The new
memory points to the new state revision while the earlier provenance remains unchanged.

For raw metadata:

```sh
contextctl ctx lineage demo-memory --json
```

See [Context revisions and provenance](../../../docs/context-revisions.md) for the data model and
service API.
