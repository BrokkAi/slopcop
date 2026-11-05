---
title: The Stop hook
description: "The automatic check hook slopcop init installs for Claude Code and Codex. When it runs, what it hands back to the agent, how it stays quiet, and how to remove it."
sidebar:
  order: 7
---

Besides the skill, `slopcop init` adds a `Stop` hook for each harness installed
on the machine, and leaves everything else in the file as it was:

| File | Harness |
| --- | --- |
| `settings.json` under `CLAUDE_CONFIG_DIR`, or `~/.claude` | Claude Code |
| `hooks.json` under `CODEX_HOME`, or `~/.codex` | Codex |

Codex runs a new or changed hook only once you trust it: open Codex and review
it with `/hooks`. Init never writes that trust itself, and reminds you each time
it runs, since Codex says nothing of a hook it skips. The hook stays the same
from one release to the next, so an update does not ask for it again.

The hook is a backstop, not a replacement. The skill keeps telling the agent to
check as it works; the hook catches a session that stopped without doing so.

## When it checks

When the agent stops, the hook checks the working tree through the same hosted
API, but only when all of these hold:

- the session wrote files in the repository with its edit tools (a Codex
  session's subagents included), or the working tree has a changed dependency
  manifest or lockfile (below);
- slopcop is signed in, and the workspace a check would use
  (`SLOPCOP_WORKSPACE`, or the login's) has the repository active, so nothing is
  sent from other repositories and the hook never adds one;
- the tree differs from the last check in the session, including a check the
  agent ran by hand. A check whose result the session already has is not sent
  again.

The hook is therefore dormant in a repository until someone runs `slopcop
enable` there. It stays quiet otherwise; it does not ask to enable the
repository.

### Dependency changes

An agent often adds a dependency through its shell, with `npm install`,
`pnpm add`, `cargo add`, or `uv add`, which no edit tool records. So the hook
also checks when git shows one of these files changed, added, or untracked in
any directory of the repository: `package.json`, `package-lock.json`,
`npm-shrinkwrap.json`, `pnpm-lock.yaml`, `yarn.lock`, `Cargo.toml`,
`Cargo.lock`, `pyproject.toml`, `uv.lock`, `poetry.lock`, `requirements*.txt`,
`go.mod`, and `go.sum`. Files git ignores and those under `node_modules` do not
count.

The hook recognises these files by name only: it never reads them, runs a
package manager, or asks an advisory database. The hosted check reads them like
any other file in the change. A changed dependency file counts as one the
session wrote, so its new findings are handed to the agent like those in files
it edited, even if the file was already changed when the session began.

## What it hands back

New findings in files the session wrote are handed to the agent to fix, once
for a given tree and at most twice in a row. Then the agent is let go and you
are told what remains. A finding ignored with `slopcop ignore` is not handed
back. New findings in other files on the branch are only mentioned.

## How it stays quiet

The hook never keeps the agent going on its own failures. A refusal the service
calls final, such as an expired login, is reported once and the hook stays
quiet for the rest of the session, until a check by hand completes. A brief
outage is only reported.

The hook runs as `slopcop hook claude || true` (and `slopcop hook codex ||
true`, or `|| exit /b 0` under Windows' `cmd`), so even a `slopcop` that cannot
run it, such as an older one first on `PATH`, lets the agent stop. Init warns
when that is the case, because no check would run.

Hook runs never print the [upgrade notice](/get-started/install/#upgrade-notices).

## Turn it off

```console
slopcop init --no-hooks       # install only the skill
slopcop init --remove-hooks   # remove SlopCop's hooks and nothing else
```

Ordinary checks do not reinstall a removed hook. The skill remains the way to
check from other harnesses.
