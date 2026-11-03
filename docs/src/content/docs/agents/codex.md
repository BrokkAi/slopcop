---
title: Codex
description: "Where the SlopCop skill lives for Codex, how to invoke it, trusting the Stop hook with /hooks, and the one line to add to AGENTS.md."
sidebar:
  order: 3
---

## The skill

`slopcop init` puts the skill in `~/.agents/skills/slopcop/`, where Codex reads
your skills, and in `~/.codex/skills/slopcop/` (`$CODEX_HOME/skills/slopcop/`
when `CODEX_HOME` is set). Codex lists it as **SlopCop**. Ask it to check its
change, or mention `$slopcop` in a prompt:

```text
Use $slopcop to check this change and fix what it reports.
```

`slopcop login` and every check also install the skill, and replace one that
differs from the skill bundled in the binary you run. [The agent
skill](/agents/skill/) explains the update and backup rules.

## Check every change

To have Codex check every change without being asked, add a line to your
project's `AGENTS.md`:

```text
Run slopcop before you finish and fix what it reports.
```

## The Stop hook

`slopcop init` adds a `Stop` hook to Codex's `hooks.json`, under `CODEX_HOME`
or `~/.codex`, and leaves everything else in the file as it was.

Codex runs a new or changed hook only once you trust it: open Codex and review
it with `/hooks`. Init never writes that trust itself, and reminds you each time
it runs, since Codex says nothing of a hook it skips. The hook stays the same
from one release to the next, so an update does not ask for it again.

When Codex stops, the hook checks the working tree through the same hosted API,
but only when the session wrote files in the repository (its subagents
included, or a dependency manifest or lockfile changed), slopcop is signed in
with the repository active, and the tree differs from the last check in the
session. New findings in files the session wrote are handed back to Codex to
fix, once for a given tree and at most twice in a row. The hook runs as
`slopcop hook codex || true` (`|| exit /b 0` under Windows' `cmd`), so a
slopcop that cannot run it still lets the agent stop.

[The Stop hook](/agents/stop-hook/) has the full rules, and
`slopcop init --no-hooks` or `--remove-hooks` turn it off.

## What the CLI reads from the thread

Codex names its thread in the commands it runs (`CODEX_THREAD_ID`). From that
thread's rollout under `~/.codex/sessions` (or `$CODEX_HOME/sessions`) the CLI
reads the files each completed file change wrote, and the model and effort of
its turn. A move counts as writing its destination. It reads only those fields,
never the conversation, a tool's input, or a file's contents, and sends the
harness, its version, the thread id as an opaque identifier, and each model and
effort with the repository-relative files it wrote. This is analytics only and
never decides a check. `slopcop --verbose` prints it.
