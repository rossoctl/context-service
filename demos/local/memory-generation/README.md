# Generate long-term memory

Use one Claude session to produce state, an independent Codex process to distill memory, and a fresh
agent process to consume only that memory. Kubernetes is not required.

## 1. Produce state

From the repository root:

```sh
make build
export PATH="$PWD/bin:$PATH"
mkdir -p /tmp/context-memory-producer
cd /tmp/context-memory-producer

contextctl ctx create demo-state --type state --backend filesystem
contextctl ctx attach demo-state --harness claude
claude
```

Tell Claude:

```text
Remember that this project's codename is Juniper, its release is Friday,
and I prefer short status updates. Draft a three-step release plan.
```

Exit Claude, then confirm the captured state:

```sh
contextctl ctx get demo-state --backend filesystem
```

## 2. Generate memory

```sh
contextctl ctx derive memory demo-state --name demo-memory --agent codex
contextctl ctx get demo-memory --backend filesystem
cat "$HOME/.contexts/demo-memory/memory/MEMORY.md"
```

The source remains native harness state. The new context contains plain Markdown plus provenance for
the exact source revision.

## 3. Use memory in a fresh session

```sh
mkdir -p /tmp/context-memory-consumer
cd /tmp/context-memory-consumer
claude --print --no-session-persistence \
  --add-dir "$HOME/.contexts/demo-memory/memory" \
  "Read MEMORY.md from the added directory. What is the codename, release day, and status preference?"
```

This final process has the generated memory but not the producer's original session state.

The same check with a fresh Codex process reads only the generated Markdown from standard input:

```sh
codex exec --ephemeral --skip-git-repo-check --sandbox read-only \
  "Use the MEMORY.md content supplied on stdin. What is the codename, release day, and status preference?" \
  < "$HOME/.contexts/demo-memory/memory/MEMORY.md"
```

See [Derived long-term memory](../../../docs/derived-memory.md) for the file contract and provenance.
