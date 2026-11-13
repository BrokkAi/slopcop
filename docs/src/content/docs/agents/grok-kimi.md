---
title: Grok and Kimi Code
description: "The Stop hook and model attribution for Grok Build and Kimi Code CLI, what the CLI reads from their sessions, and what is not yet checked against a live session."
sidebar:
  order: 4
---

**Not yet verified against a live session.** Support for Grok and Kimi Code is
built from each harness's published source,
[xai-org/grok-build](https://github.com/xai-org/grok-build) at `2bdd1d6` and
[MoonshotAI/kimi-code](https://github.com/MoonshotAI/kimi-code) at `131c21a`
(Kimi Code 2.5.1), not from a captured session. If a hook does not fire or a
check names no model, please open an issue.

Both read the skill from the repository's `.agents/skills`, where
`slopcop init` puts it. Grok reads it, with `.grok/skills` and
`.claude/skills`, in the working directory and each parent up to the Git
root, and only in a folder you trust. Kimi Code reads it, with
`.kimi-code/skills`, at the root of the Git repository. The `AGENTS.md` line
from [Other agents](/agents/other-agents/) works as it does elsewhere.

## Grok

`slopcop init` writes a `Stop` hook to `hooks/slopcop.json` under `GROK_HOME`,
or `~/.grok`. It stays at user level while the Claude Code and Codex hooks
move into the repository, because Grok runs a project's hooks only in a folder
you trust, and a user-level hook needs no trust. Grok loads every `*.json` file in that directory, so the hook
lives in a file of its own and your other hook files are left alone. Grok asks
you to trust hooks that come with a project, not these.
`slopcop init --remove-hooks` takes it out again.

Grok also runs the hooks in `~/.claude/settings.json` and, in a folder you
trust, the repository's `.claude/settings.json` and
`.claude/settings.local.json`, unless its Claude Code compatibility is off.
SlopCop's Claude Code Stop hook sees that it is running under Grok and does
nothing, so a stop is checked once. The push gate `slopcop init` puts in
`.claude/settings.local.json` does run under Grok: it blocks Grok's
`run_terminal_command` when the command pushes or names `refs/slopcop/`, as it
does Claude Code's `Bash`.

### What the CLI reads from the session

Grok names its session in the commands its agent runs (`GROK_SESSION_ID`), so
a check run by the agent is attributed as well as one run by the hook. The CLI
reads the session under `~/.grok/sessions` (or `$GROK_HOME/sessions`):

- `updates.jsonl`: each edit tool call, the files it names, and whether it
  completed. A failed edit is not counted.
- `chat_history.jsonl`: the model and reasoning effort of the response that
  made each call. After a compaction rewrites it, an earlier edit gets the
  model its turn started with, and no effort.
- `summary.json`: the working directory a relative path is resolved against.

Edits made by Grok's subagents, which run in sessions of their own, are not
read yet. Grok's version is not available at run time, so none is sent.

## Kimi Code

Kimi Code keeps hooks only in `config.toml` under `KIMI_CODE_HOME`, or
`~/.kimi-code`, and fails to load that file if a hook entry holds a field it
does not know. `slopcop init` does not edit it. Add the hook yourself:

```toml
[[hooks]]
event = "Stop"
command = "slopcop hook kimi || true"
timeout = 600
```

Kimi Code allows a hook at most 600 seconds and lets the agent stop if the hook
takes longer. It lets a Stop hook hold the agent back once per turn.

### What the CLI reads from the session

Kimi Code puts nothing in its agent's commands that names the session, so a
check the agent runs by hand sends no attribution; only the Stop hook does,
using the session id in its payload. The hook reads every agent's
`wire.jsonl` in the session's directory under `sessions/`, the main agent's and
each subagent's:

- the model alias, such as `kimi-code/k3`, and the thinking effort (a level, or
  `on` or `off`) each agent ran with;
- each `Edit` and `Write` call, its path, and whether it succeeded;
- the session's working directory from `session_index.jsonl`, for relative
  paths.

A session migrated from the older Kimi CLI is not read. Kimi Code's version is
not available to the hook, so none is sent.

Like the other harnesses, the CLI reads only these fields, never the
conversation or a file's contents, and sends the harness, the session id, and
each model and effort with the repository-relative files it wrote. This is
analytics only and never decides a check.
