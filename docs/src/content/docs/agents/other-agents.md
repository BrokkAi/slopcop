---
title: Other agents
description: "opencode reads both skills folders, and any agent that can run a shell command can run slopcop. What to tell an agent that has no skill support."
sidebar:
  order: 4
---

## opencode

opencode reads both `~/.agents/skills` and `~/.claude/skills`, so
`slopcop init` covers it too. Ask it to check its change, or add the usual line
to your project's `AGENTS.md`:

```text
Run slopcop before you finish and fix what it reports.
```

There is no Stop hook for opencode; the skill is how it knows to check.

## Any agent with a shell

Any agent that can run a shell command can run `slopcop`: it never prompts,
sends one request, and answers with an exit code. Run outside Claude Code or
Codex, a check sends nothing about the session, only the repository URL, the
base commit, and the patch.

If the agent cannot load skills, give it the essentials in its instructions
file:

```text
Before you finish, run `slopcop --format json` from the repository root and
read the report.
- Exit 0: clean.
- Exit 1: the change added findings. Fix each one and run the check again.
  Read a finding whose certainty is "possible" or "heuristic" before changing
  code; if it is a false positive, run `slopcop ignore <id>` with its id.
  Undo a mistaken ignore with `slopcop unignore <id>`.
- Exit 2: some rule could not finish, or the check failed. Say which; never
  treat it as clean.
- Exit 3: still running. Run the same command again.
- Exit 4: refused; only a person can clear it. Stop and report the code and
  remedy from the output.
When checking committed work, pass `--base <branch the change targets>`.
```

The full instructions are in the skill itself, which you can read at
[`skills/slopcop/SKILL.md`](https://github.com/BrokkAi/slopcop/blob/master/skills/slopcop/SKILL.md)
and adapt.

## Signing in for an agent

An agent runs with your login. On a machine where you cannot open a browser,
store a token instead of running the device flow:

```console
slopcop login --token "$SLOPCOP_TOKEN"
```

or set `SLOPCOP_TOKEN` in the agent's environment, which needs no login at all.
See [Sign in](/account/sign-in/). Give agents that share a login their own
`--workspace` rather than switching the login's workspace; see
[Workspaces](/account/workspaces/).
