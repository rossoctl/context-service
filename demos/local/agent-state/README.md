# Local agent state

Capture a harness's native sessions, memory files, and metadata in a local context.

> Agent state may contain prompts, files, command output, and secrets. Protect `~/.contexts`.

## Try it

Build the CLI:

```sh
make build
export PATH="$PWD/bin:$PATH"
```

From your project:

```sh
contextctl ctx create demo --type state --backend filesystem
contextctl ctx attach demo --harness claude
claude
```

Ask Claude:

```text
Remember that the project code word is violet telescope.
```

Wait for its response, then exit Claude. Context Service captures the native state automatically.

Use `codex`, `opencode`, or `pi` instead of `claude` to attach another harness.

Inspect the captured state:

```sh
contextctl ctx get demo --backend filesystem
find ~/.contexts/demo -type f
```

To stop automatic capture:

```sh
contextctl ctx detach demo --harness claude
```

## Restore Claude state

```sh
mkdir -p /tmp/claude-state-restored
contextctl ctx restore demo --harness claude --project /tmp/claude-state-restored
cd /tmp/claude-state-restored
claude --resume
```

Select the captured session and ask: `What is the project code word?`
