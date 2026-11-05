---
title: The agent skill
description: "The bundled SlopCop skill, its repository install, updates to existing copies, and local edit backups."
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

## Local review

By default, `slopcop check` runs the hosted static check and local review
concurrently. `--mode static` runs only the hosted check; `--mode ai` runs only
review without a SlopCop login, enabled repository, or supported origin.
`--static` and `--ai` are shortcuts. The review uses Node.js with `npx` and
existing harness logins. Reviewers can read files and run commands in the
repository. Slopcop refuses their requests to edit, delete, or move files and
runs Codex in its read-only sandbox. Commands can still write, so this
is not a security boundary. A review finding is a model's judgment, while a static finding traces to a rule.

## What it tells an agent

For hosted static checks, the skill uses repositories the workspace has enabled.
Local review in `ai` mode does not need enablement. Before an automatic check it
uses `slopcop check --if-enabled`, which skips only the static half when the
repository is not active. Local review still runs in default `both` mode. It
never adds a repository and never asks the user to add one as a side effect of a
check; `slopcop enable` is the step a person takes at a terminal, and the skill
only points to it when the user explicitly asks for a check of a repository that
is not enabled.

## Where it goes

Run `slopcop init` inside each repository. It installs the skill in these
repository-local directories and names what it did:

| Location | Harness |
| --- | --- |
| `.agents/skills/slopcop/` | Codex and other agents |
| `.claude/skills/slopcop/` | Claude Code and other agents |

Init adds these paths to `.git/info/exclude`, leaves tracked targets alone,
and removes old user-level SlopCop skill copies. An existing skill is left
alone when init runs again.
`slopcop init --no-skill` skips the install and any update of it.

## How it stays current

A check updates skill copies already present in the repository, rewriting
one that differs from the skill bundled into the binary. It creates no new
copy, and `slopcop login` does not install the skill. This keeps existing
copies aligned with the version of slopcop that runs. Each install records
the version that wrote it, and what each file held, in `.slopcop-skill.json` in
the skill folder.

An update names the version it replaces, the new one, and the files that
changed:

```text
slopcop: updated the slopcop skill at .claude/skills/slopcop from v0.0.9 to v0.0.10 (changed: SKILL.md). Reread it if you loaded the old one.
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
