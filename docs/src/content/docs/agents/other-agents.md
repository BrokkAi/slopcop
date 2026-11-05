---
title: Other agents
description: "opencode reads both skills folders, and any agent that can run a shell command can run slopcop. What to tell an agent that has no skill support."
sidebar:
  order: 4
---

## Local review

By default, `slopcop check` runs the hosted static check and local review
concurrently. `--mode static` runs only the hosted check; `--mode ai` runs only
review without a SlopCop login, enabled repository, or supported origin.
`--static` and `--ai` are shortcuts. The review uses Node.js with `npx` and
existing harness logins. Reviewers can read files and run commands in the
repository. Slopcop refuses their requests to edit, delete, or move files and
runs Codex in its read-only sandbox. Commands can still write, so this
is not a security boundary. A review finding is a model's judgment, while a static finding traces to a rule.

## opencode

opencode reads `.agents/skills` and `.claude/skills` in the repository, so
`slopcop init` covers it too when run there. Add the repository to the workspace
from its checkout with `slopcop enable` at a terminal, then ask it to check its
change, or add the usual line to your project's `AGENTS.md`:

```text
Run `slopcop check --if-enabled` before you finish and fix what it reports.
```

There is no Stop hook for opencode; the skill is how it knows to check.

## Any agent with a shell

Any agent that can run a shell command can run `slopcop`: it never prompts, the
static half sends one request, and it answers with an exit code. Run outside
Claude Code or Codex, a check sends nothing about the session, the static half
sends only the repository URL, base commit, patch, and selected policies. The
review half sends the diff and supplied intent to reviewer model providers
through your harness logins. Use `--mode static` to avoid that review.

If the agent cannot load skills, give it the essentials in its instructions
file:

```text
Before you finish, run `slopcop check --if-enabled --format json` from the
repository root and read the report. It sends nothing when this repository is
not enabled in the workspace.
- Exit 0: clean, or skipped because the repository is not enabled. A skip is
  not a pass; do not ask the user to add the repository unless they asked you
  to check it. They enable it with `slopcop enable` at a terminal.
- Exit 1: the change added findings. Fix each one and run the check again.
  Read a finding whose certainty is "possible" or "heuristic" before changing
  code; if it is a false positive, run `slopcop ignore <id>` with its id.
  Undo a mistaken ignore with `slopcop unignore <id>`; `slopcop ignored`
  lists the repository's ignored findings and their ids.
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
