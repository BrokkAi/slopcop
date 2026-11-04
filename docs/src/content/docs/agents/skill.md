---
title: The agent skill
description: "The SlopCop skill bundled in the binary, where slopcop init installs it, how login and every check keep it current, and what happens to local edits."
sidebar:
  order: 5
---

The `slopcop` binary carries the agent skill that teaches a coding agent how to
run slopcop: when to check, which flags matter, how to read the report, what
each exit code means, and what to do about every service error code. It is the
file at
[`skills/slopcop/SKILL.md`](https://github.com/BrokkAi/slopcop/blob/master/skills/slopcop/SKILL.md)
in the repository, with a small `agents/openai.yaml` beside it that gives Codex
the skill's display name and default prompt.

## Where it goes

`slopcop init` installs the skill into each well-known skills directory whose
harness is present, and names what it did:

| Location | Harness |
| --- | --- |
| `~/.agents/skills/slopcop/` | Codex, opencode, Cursor, and other agents |
| `~/.claude/skills/slopcop/` | Claude Code, opencode |
| `$CODEX_HOME/skills/slopcop/`, or `~/.codex/skills/slopcop/` | Codex home |

When none of those harness directories is present, init writes the
vendor-neutral `~/.agents/skills/slopcop/` instead, which Codex, opencode,
Cursor and other agents read. A fresh machine therefore has the skill before
any harness has created its configuration directory. A skill that is already
there is left alone by init, so running it again is safe.
`slopcop init --no-skill` skips the install and any update of it.

## How it stays current

`slopcop login` and every check also install the skill in those directories,
rewriting one that differs from the skill bundled into the binary, so an agent
follows the instructions of the slopcop version it runs. Each install records
the version that wrote it, and what each file held, in `.slopcop-skill.json` in
the skill folder.

An update names the version it replaces, the new one, and the files that
changed:

```text
slopcop: updated the slopcop skill at ~/.claude/skills/slopcop from v0.0.9 to v0.0.10 (changed: SKILL.md). Reread it if you loaded the old one.
```

It removes the files the new skill no longer has. A check's `json` and `sarif`
reports carry the same update under `skillUpdated`, so an agent that ran the
check knows its loaded copy of the instructions is out of date and rereads
`SKILL.md` before acting on the report.

## Local edits are kept

A skill with local edits, one that matches neither its record nor any release,
is copied first to `skill-backups/<harness>-<time>` next to the slopcop
configuration, outside every skills directory so no harness loads it as a
second skill, and the notice says where. If you want to keep your own version
of the instructions, put it in a skill with another name; the `slopcop` skill
directory is managed by the CLI.
