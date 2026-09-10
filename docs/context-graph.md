# Context relationship graph

`contextctl ctx graph` shows how context moves and changes without requiring users to inspect local
manifests, PVC annotations, or backup state.

```text
project-state  state · filesystem · Ready · 6ad3e194a0c1
├── location  /Users/me/.contexts/project-state
├── derives  project-memory@9c01b7a22e61 · memory · filesystem · ready
├── consumer  harness/Claude · /work/project · attached
└── copy  pvc/context-project-state · namespace team1 · 6ad3e194a0c1 · ready

project-memory  memory · filesystem · Ready · 9c01b7a22e61
├── location  /Users/me/.contexts/project-memory
├── source  project-state@6ad3e194a0c1 · state · filesystem · ready
└── derives  project-knowledge@ebec31906820 · knowledge · filesystem · ready
```

The default view combines filesystem and PVC contexts. Use `--backend filesystem` for local context
only, `--backend pvc` for Context Service storage only, or `--json` for automation.

Each context appears once. `source` points to the exact input revision; `derives` points to context
built from it. `consumer` identifies attached harnesses or Pods. `copy` identifies configured S3
backups and matching PVC context. Relationship state is:

- `ready`: the referenced revision is current.
- `stale`: a newer local revision exists or the remote copy is behind.
- `syncing`: the backup worker is transferring a newer revision.
- `missing`: the source revision or configured destination is unavailable.
- `failed`: the latest copy attempt failed; backup status contains the error.

PVC consumer discovery is scoped to the selected namespace and the caller's Context Service access.
If the service is unavailable in the default `all` view, local relationships are still shown with a
warning.
