---
title: OpenCode and Kilo Code
description: "How opencode finds the SlopCop skill, what a check run inside an OpenCode session reads from its database, and why Kilo Code CLI sends no session yet."
sidebar:
  order: 4
---

## The skill

opencode reads both `~/.agents/skills` and `~/.claude/skills`, so
`slopcop init` covers it too. Add the repository to the workspace from its
checkout with `slopcop enable` at a terminal, then ask it to check its change,
or add the usual line to your project's `AGENTS.md`:

```text
Run `slopcop check --if-enabled` before you finish and fix what it reports.
```

There is no Stop hook for opencode: it runs hooks only as JavaScript plugins,
not shell commands, so the skill is how it knows to check.

## What the CLI reads from the session

OpenCode 2 names its session in the commands its agent runs
(`OPENCODE_SESSION_ID`). A check run there opens OpenCode's database read-only,
`opencode.db` under `$XDG_DATA_HOME/opencode` or `~/.local/share/opencode` (or
the one `OPENCODE_DB` names), and reads, for the session and its subagents'
sessions:

- the OpenCode version that created the session;
- each `edit`, `write` and `patch` tool call that completed, and the files it
  wrote. A failed call is not counted, a patch's move counts as writing its
  destination, and a delete writes nothing;
- the model of the step that made the call, and its variant as the effort.
  The `default` variant names no effort, so none is sent.

It reads only those fields, never the conversation, a tool's other input, or a
file's contents, and sends the harness, its version, the session id as an
opaque identifier, and each model and effort with the repository-relative files
it wrote. This is analytics only and never decides a check. `slopcop --verbose`
prints it.

:::note
This was built from OpenCode's source, and read back from an OpenCode 2.1.2
database, but not yet checked from inside a live session. If a check from
OpenCode sends no models, `slopcop --verbose` shows what it found.
:::

## Kilo Code CLI

Kilo Code CLI, a fork of opencode, reads skills from `~/.agents/skills` too,
so the skill and the `AGENTS.md` line above work there. A check run in it sends nothing
about the session yet: Kilo does not name its session in the commands its agent
runs, so the CLI cannot tell which session in its database is the one running
the check.
