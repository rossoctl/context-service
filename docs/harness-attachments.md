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

## Manual capture and restore

Manual `capture` supports Claude Code project state. Codex, OpenCode, and Pi are captured by
their attachments. `restore` supports all four harnesses and writes each one back into the same
harness:

```sh
contextctl ctx capture demo --harness claude
contextctl ctx restore demo --harness claude --project /path/to/project
contextctl ctx restore demo --harness codex --project /path/to/project
contextctl ctx restore demo --harness opencode --project /path/to/project
contextctl ctx restore demo --harness pi --project /path/to/project
```

| Harness | Captured as | Restored to | Existing local state |
|---|---|---|---|
| Claude Code | `harnesses/claude/project/` (whole project directory) | `$CLAUDE_CONFIG_DIR/projects/<project key>/` | Refuses if the directory is not empty |
| Codex | `harnesses/codex/sessions/<thread id>.jsonl` (rollout) | `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<time>-<thread id>.jsonl` | Skips identical rollouts; refuses if a live or archived rollout for the thread differs |
| OpenCode | `harnesses/opencode/sessions/<session id>.json` (`opencode export`) | `opencode import`, run in the destination project | Refuses if the session already exists |
| Pi | `harnesses/pi/sessions/<time>_<id>.jsonl` | `$PI_CODING_AGENT_DIR/sessions/--<project>--/` or `$PI_CODING_AGENT_SESSION_DIR` | Skips identical sessions; refuses if the file differs |

`CODEX_HOME` defaults to `~/.codex` and `PI_CODING_AGENT_DIR` to `~/.pi/agent`. Codex and Pi
restore check every session before writing any, including content conflicts and directory links
in each destination path, so a refusal leaves local state untouched. OpenCode restore checks that
no session exists before importing any, but imports are not atomic across sessions: if a later
import fails, earlier imports remain. Restore writes only below the harness's session directory
and rejects captured symlinks, session IDs that are not plain names, and directory links that
would lead outside the session directory.

Caveats:

- Restore copies session files byte for byte. A Codex or Pi session keeps the working directory
  it was captured in, so it resumes most faithfully at the same project path. `codex resume <id>`
  finds a restored rollout by thread ID. The resume picker, which filters by working directory,
  may not list it. `--project` does not change where a Codex session is restored.
- Codex captures one rollout per thread, under its thread ID. Restore rebuilds Codex's file name
  from the rollout's `session_meta` record, using its UTC start time; Codex names new files with
  local time. Restore refuses a thread that has a compressed (`.jsonl.zst`) local copy, since it
  does not compare compressed rollouts.
- OpenCode stores sessions in its own database, so restore runs `opencode import` instead of
  writing files. OpenCode must be on `PATH`. Import binds each session to the destination project.
  If an import fails partway, the sessions imported before the failure remain.
- Pi restore follows `PI_CODING_AGENT_SESSION_DIR`, but not a `sessionDir` set in Pi's
  `settings.json`.

Restore does not translate state between harnesses. A Claude Code capture restores into Claude
Code only, a Codex capture into Codex only, and so on.
