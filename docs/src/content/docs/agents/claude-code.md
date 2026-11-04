---
title: Claude Code
description: "Where the SlopCop skill lives for Claude Code, how to invoke it, the Stop hook that checks what a session wrote, and the one line to add to CLAUDE.md."
sidebar:
  order: 2
---

## The skill

`slopcop init` puts the skill in `~/.claude/skills/slopcop/`, Claude Code's
personal skills, so it is there in every project. Claude uses it when you ask it
to check its change, and you can run it yourself with `/slopcop`.

```console
slopcop init
```

`slopcop login` and every check also install the skill, and replace one that
differs from the skill bundled in the binary you run, so Claude follows the
instructions of the slopcop version it has. [The agent skill](/agents/skill/)
explains the update and backup rules.

## Check every change

First add the repository to the workspace from its checkout, at a terminal:

```console
slopcop enable
```

Then, to have Claude check every change without being asked, add a line to your
project's `CLAUDE.md`:

```text
Run `slopcop check --if-enabled` before you finish and fix what it reports.
```

Claude runs the check, reads the report, fixes the new findings, and runs it
again until none are left. The skill tells it to pass `--base` with the branch
the change targets when it checks committed work, to read a `possible` finding
before changing code, and to run `slopcop ignore <id>` for a false positive.

## The Stop hook

`slopcop init` also adds a `Stop` hook to Claude Code's `settings.json`, under
`CLAUDE_CONFIG_DIR` or `~/.claude`, and leaves everything else in the file as
it was. When Claude stops, the hook checks the working tree through the same
hosted API, but only when the session wrote files in the repository (or a
dependency manifest or lockfile changed), slopcop is signed in with the
repository active, and the tree differs from the last check in the session.

New findings in files the session wrote are handed back to Claude to fix, once
for a given tree and at most twice in a row. Then Claude is let go and you are
told what remains. The hook runs as `slopcop hook claude || true`, so a slopcop
that cannot run it still lets the agent stop.

[The Stop hook](/agents/stop-hook/) has the full rules, and
`slopcop init --no-hooks` or `--remove-hooks` turn it off.

## What the CLI reads from the session

Claude Code names its session in the commands it runs
(`CLAUDE_CODE_SESSION_ID`). From that session's transcript under
`~/.claude/projects` (or `$CLAUDE_CONFIG_DIR/projects`), subagents included,
the CLI reads the model and effort behind each file Claude wrote: each
successful Write, Edit, MultiEdit, or NotebookEdit gives a model, an effort, and
a file. It reads only those fields, never the conversation, a tool's input, or
a file's contents, and sends the harness, its version, the session id as an
opaque identifier, and each model and effort with the repository-relative files
it wrote. This is analytics only and never decides a check.
`slopcop --verbose` prints it.

## Dogfooding tip

Agents typically run `slopcop` after committing, often after pushing. On a
branch whose upstream already has every commit, the default base falls back to
the merge base with `origin/HEAD`, so the check still covers the branch. On the
default branch itself, with nothing unpushed, the patch is empty: the report
says `emptyChange: true` and stderr suggests `--base`. Tell Claude which branch
the change targets, for example `slopcop --base origin/main`, when it checks
committed work.
