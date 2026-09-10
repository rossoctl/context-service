# Build and search agent knowledge

This demo continues the [memory generation demo](../memory-generation/). It turns the captured
`demo-state` and derived `demo-memory` contexts into inspectable, searchable knowledge.

## 1. Build knowledge

```sh
contextctl ctx derive knowledge \
  --name demo-knowledge \
  --from demo-state \
  --from demo-memory \
  --agent codex
```

Inspect both the logical context and its ordinary files:

```sh
contextctl ctx get demo-knowledge --backend filesystem
cat "$HOME/.contexts/demo-knowledge/knowledge/records.jsonl"
cat "$HOME/.contexts/demo-knowledge/knowledge/index.json"
```

## 2. Search with attribution

```sh
contextctl ctx search demo-knowledge "Juniper release"
contextctl ctx search demo-knowledge "status preference"
```

Each result identifies the source context revision and source file.

## 3. Give only retrieved knowledge to a fresh agent

```sh
contextctl ctx search demo-knowledge "Juniper release" --json > /tmp/context-results.json

codex exec --ephemeral --skip-git-repo-check --sandbox read-only \
  "Use only the retrieved knowledge supplied on stdin. Summarize the release facts in one sentence." \
  < /tmp/context-results.json
```

The fresh agent receives selected records, not the producer's original session history.

## 4. See incremental generation

Continue the producer session and add a new durable fact, then exit so its state is captured. Run the
same knowledge command again. The output reports records reused from unchanged sources; only the
changed source is sent to the generator.

See [Searchable knowledge contexts](../../../docs/derived-knowledge.md) for the file format,
incremental behavior, and context boundaries.
