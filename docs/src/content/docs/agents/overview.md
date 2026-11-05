---
title: SlopCop in your coding agent
description: "How a coding agent checks its own change with SlopCop, reads what it found, and fixes it before handing the work back. The skill, the Stop hook, and the exit codes an agent acts on."
sidebar:
  order: 1
---

Your agent checks its own change with SlopCop, reads what it found, and fixes
it before it hands the work back. Two things make that work without a human in
the loop:

- **The skill.** A set of instructions, bundled in the `slopcop` binary, that
  tells an agent how to run a check, how to read the report, which exit codes
  mean what, and what to do about each. `slopcop init` installs it where your
  agents look for skills. See [The agent skill](/agents/skill/).
- **The Stop hook.** For Claude Code and Codex, a hook that runs a check when
  the agent stops after writing files, and hands new findings in those files
  back to the agent to fix. See [The Stop hook](/agents/stop-hook/).

The CLI itself is built for this: it never prompts, every check it sends is
one request, and it answers with a closed exit-code contract. Any agent that
can run a shell command can run it. `--if-enabled` skips a repository that is
not enabled, and says so, instead of sending a check.

## Set up

Once per machine:

```console
slopcop init     # skill into ~/.agents/skills and each harness here, plus the hook
slopcop login    # approve the code in your browser
```

Once per repository, at a terminal:

```console
slopcop enable   # add this checkout to the workspace and run its first check
```

After that the agent can check the repository by itself. A check of one that is
not enabled sends nothing and exits `4` with `repository_not_enabled`; an
automatic check can use `--if-enabled` to skip it quietly instead. `slopcop
status` shows where a checkout stands.

`slopcop init` names what it did. A skill that is already there is left alone,
so running it again is safe. See the [Quickstart](/get-started/quickstart/).

## How a check works

1. Your agent runs `slopcop`, because the skill tells it to, because you asked,
   or because the Stop hook fired. The hook sends nothing for a repository the
   workspace does not have active.
2. The CLI builds one git patch from the merge base with your upstream branch
   to the working tree: unpushed commits, unstaged edits, and untracked files.
   Git-ignored files stay out.
3. The service applies the patch at that base commit in its own clone and runs
   Bifrost over the files the change writes. Your code is read, never run, and
   never sent to a language model.
4. The report comes back. New findings, on lines the change added or touched,
   fail the check. Findings already in the code are counted, never failed.
5. The agent fixes the new findings and checks again, until none are left. A
   false positive it ignores with `slopcop ignore <id>`, and later checks leave
   it out.

## The exit codes an agent acts on

| Exit | Meaning | What the agent does |
| --- | --- | --- |
| `0` | Clean, or `--if-enabled` skipped a repository that is not active and sent nothing. | Carries on; a skip is not a pass. |
| `1` | Findings. The change added at least one. | Fixes them and checks again. |
| `2` | Incomplete, or the CLI or service failed. No new finding crossed the threshold, but some rule could not finish. | Reports which rules did not complete. Never reads it as all clear. |
| `3` | Still running when `--timeout` ran out. | Runs the same check again; it picks up the same analysis. |
| `4` | The check was refused and only a person can clear it: sign in, credits, access, or enabling the repository. | Stops and tells you the code and remedy. |

[Exit codes](/checks/exit-codes/) has the full table, including interrupts.

## Which models wrote what

Run by Claude Code or Codex, a check also sends which models wrote each changed
file, read from the session's own record, so a finding can be traced to the
model that wrote its line. The CLI reads only the model, effort, and file of
each edit, never the conversation, a tool's input, or a file's contents. Run
outside an agent, a check sends none of it. `slopcop --verbose` prints what it
would send. See [What leaves your machine](/account/privacy/).

## Per agent

- [Claude Code](/agents/claude-code/): personal skills, `/slopcop`, the Stop
  hook in `settings.json`, and the line for `CLAUDE.md`.
- [Codex](/agents/codex/): `~/.agents/skills` and `$CODEX_HOME/skills`,
  `$slopcop`, trusting the hook with `/hooks`, and the line for `AGENTS.md`.
- [Other agents](/agents/other-agents/): opencode, and anything with a shell.
- [Lovable, Bolt, and MCP clients](/agents/app-builders/): agents with no
  shell, which check over SlopCop's hosted MCP server.
