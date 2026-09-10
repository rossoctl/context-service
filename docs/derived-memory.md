# Derived long-term memory

A `state` context preserves a harness's native session files. A `memory` context is a smaller,
harness-independent set of durable facts and decisions derived from that state.

```text
state context                         memory context
native transcripts  ── Claude ──▶    memory/MEMORY.md
tool and session data                 provenance in manifest.json
```

Generate memory without changing the source state:

```sh
contextctl ctx derive memory project-state --name project-memory --agent codex
contextctl ctx get project-memory --backend filesystem
```

The Claude or Codex generator runs as an independent, non-persistent agent process. It receives the readable files
captured under the source context's `harnesses/` directory and is instructed to retain stable facts,
preferences, decisions, workflows, and unresolved work while removing transient logs and secrets.
Captured content is treated as untrusted data, not as generator instructions.

## On-disk contract

```text
~/.contexts/project-memory/
├── manifest.json
└── memory/
    └── MEMORY.md
```

The manifest records the source context name and type, exact source revision, generation time, and
generator identity. `MEMORY.md` is ordinary Markdown so a person or agent can inspect and use it
without Context Service. Export, S3 sync, PVC sync, and automatic backup include derived memory.

If the named memory context already derives from the same source revision, the command exits without
calling the generator again. A changed source requires a new memory context name; automatic memory
promotion and replacement policies are intentionally not implied.
