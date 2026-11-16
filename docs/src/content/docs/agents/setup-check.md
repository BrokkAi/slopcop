---
title: Checking your setup
description: "slopcop doctor reports whether each agent on this machine has SlopCop's skill, Stop hook and push gate, what to run when something is missing, and which leftovers from per-repository releases a checkout still holds."
sidebar:
  order: 7
---

`slopcop init` installs SlopCop once per user: the skill, the Stop hook and
the push gate for every agent harness it finds. Most of the time that is
enough. The setup can still have gaps: an agent installed after init ran, a
hook removed by hand, Codex hooks not yet trusted, or an old checkout that
still holds files from the per-repository releases. `slopcop doctor` shows
which of these apply.

```console
slopcop doctor                 # anywhere; in a checkout it also looks for leftovers
slopcop doctor --format json   # for an agent or a script
```

Doctor reads local files only. It sends nothing, probes no reviewer, and
changes nothing, so an agent can run it at the start of a session without
asking.

## What it reports

For Claude Code, Codex and Grok, when the harness is installed here, doctor
reports whether the `slopcop` skill, the Stop hook and the push gate are each
present. It then gives one of three states:

| State | Meaning |
| --- | --- |
| `ready` | every installed harness has all three |
| `not_installed` | nothing is installed; init has not run for this user |
| `incomplete` | something is missing, such as a harness installed after init or a hook removed by hand |

When something is missing, doctor names it and suggests `slopcop init`. Init
only adds SlopCop's own entries and leaves everything else in each file alone.
`--no-skill` or `--no-hooks` installs just one half, and `--no-reviewers` skips
the reviewer probe, which sends each reviewer a short prompt. Doctor mentions
the probe when no reviewers are configured yet.

With no harness installed, doctor checks only the shared `~/.agents/skills`
skill that opencode, Cursor and other agents read.

## Codex trust

Codex runs a new or changed user-level hook only after you trust it with
`/hooks`. That trust is not visible in any file doctor can read. So whenever
the Codex hooks are installed, doctor reminds you to trust them, rather than
calling them working.

## Leftovers from per-repository releases

Earlier releases wrote hooks into `.claude/settings.local.json` and
`.codex/hooks.json`, and skills into `.agents/skills` and `.claude/skills`, in
each checkout. Run inside a checkout, doctor lists any that remain under
`legacy`. `slopcop init` in that checkout removes them, as does the checkout's
old hook the next time it runs the upgraded binary. Tracked files are never
listed or removed.

## Coverage

| Harness | What doctor checks |
| --- | --- |
| Claude Code | skill in `~/.claude/skills`, Stop hook and push gate in `settings.json` |
| Codex | skill in `~/.agents/skills` or `CODEX_HOME/skills`, Stop hook and push gate in `hooks.json`; trust is not visible |
| Grok | skill in `~/.agents/skills`, Stop hook and push gate in `hooks/slopcop.json` |
| Kimi Code, opencode, Cursor, Copilot CLI, others | the shared skill only; hooks for these are set up by hand |

Hooks deployed through managed settings (see [Managed rollout](/agents/managed-rollout/))
live outside these files, so doctor reports them as missing.
