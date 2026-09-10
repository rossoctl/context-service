# Searchable knowledge contexts

A `knowledge` context converts selected state and memory into small attributed records and a local
search index. It is optimized for retrieval rather than replaying a complete harness session.

```text
state ─┐
       ├── independent agent ──▶ records.jsonl ──▶ index.json ──▶ contextctl ctx search
memory ┘                         source attribution
```

## Generate and search

```sh
contextctl ctx derive knowledge \
  --name project-knowledge \
  --from project-state \
  --from project-memory \
  --agent codex

contextctl ctx search project-knowledge "release schedule"
```

Claude and Codex are supported generators. Each source is processed independently. Repeating the
command reuses records whose source revision has not changed and regenerates only changed sources.

## On-disk contract

```text
~/.contexts/project-knowledge/
├── manifest.json
└── knowledge/
    ├── sources/
    │   ├── project-state.txt
    │   └── project-memory.txt
    ├── records.jsonl
    └── index.json
```

`sources/` contains the exact text supplied to the generator with context revision headers.
`records.jsonl` contains normalized facts with source context, revision, and file attribution.
`index.json` is a deterministic lexical index used by the local search command. These files are
ordinary JSON, JSONL, and text; no vector database is required.

The manifest records every source revision and generator. Export, import, PVC sync, S3 sync, and
automatic backup include the complete knowledge directory. A query result always reports its source
context revision and file so conflicting or stale inputs remain visible.

## Context boundaries

- `state`: native harness sessions and metadata; useful for resuming execution.
- `memory`: curated durable facts and decisions; useful as compact agent context.
- `knowledge`: attributed records plus an index; useful for retrieving only relevant context.
