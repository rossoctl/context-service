# Harness attachments

A filesystem-backed `state` context stores a harness's native session files outside the project.
Attaching a context adds a project-local harness integration that captures state after completed
responses and when the session ends.

```sh
contextctl ctx create demo --type state --backend filesystem
contextctl ctx attach demo --harness claude
claude
```

Run these commands from the project whose state should be captured. `--project PATH` can select a
different project explicitly.

## Supported harnesses

| Harness | Project integration | Capture event |
|---|---|---|
| Claude Code | `.claude/settings.local.json` command hooks | `Stop`, `SessionEnd` |
| Codex | `.codex/hooks.json` command hooks | `Stop`, `SessionEnd` |
| OpenCode | `.opencode/plugins/context-service.ts` plugin | `session.idle` |
| Pi | `.pi/extensions/context-service.ts` extension | `agent_settled`, `session_shutdown` |

The integration invokes an internal `contextctl hook` command. Users do not run that command
directly. Context Service records captured files under:

```text
~/.contexts/<context>/harnesses/<harness>/
```

Existing Claude and Codex settings are preserved. Detach removes only the integration managed by
Context Service:

```sh
contextctl ctx detach demo --harness claude
```

Inspect the attachment and latest capture with:

```sh
contextctl ctx get demo --backend filesystem
```

Manual `capture` and `restore` currently support Claude Code project state:

```sh
contextctl ctx capture demo --harness claude
contextctl ctx restore demo --harness claude --project /path/to/project
```

Restore refuses to overwrite existing Claude state for the destination project.
