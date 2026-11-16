---
title: Codex
description: "Where the SlopCop skill lives for Codex, how to invoke it, trusting the Stop hook with /hooks, and the one line to add to AGENTS.md."
sidebar:
  order: 3
---

## Local review

By default, `slopcop check` runs the hosted static check and local review
concurrently, once reviewers are configured (`slopcop init` picks them). With
none configured, or in a CI job, it runs only the static check. `--mode static` runs only the hosted check; `--mode ai` runs only
review without a SlopCop login, enabled repository, or supported origin.
`--static` and `--ai` are shortcuts. The review uses Node.js with `npx` and
existing harness logins. The prompt carries the captured diff. Reviewers read repository content,
which is untrusted. Claude and DeepSeek reviewers may only read and search
files; Slopcop refuses their requests to run commands or change files. Codex
reviewers run in Codex's read-only sandbox. A review finding is a model's judgment, while a static finding traces to a rule.

## The skill

`slopcop init` puts the skill in `~/.agents/skills/slopcop/` once for every
project. Codex also discovers the other shared agents that read that directory.
Codex lists it as
**SlopCop**. Ask it to check its change, or mention `$slopcop` in a prompt:

```text
Use $slopcop to check this change and fix what it reports.
```

A check updates an existing skill copy when it differs from the bundled
skill, but creates none. Login does not install the skill. [The agent
skill](/agents/skill/) explains the update and backup rules.

## Check every change

First add the repository to the workspace from its checkout, at a terminal:

```console
slopcop enable
```

Then, to have Codex check every change without being asked, add a line to your
project's `AGENTS.md`:

```text
Run `slopcop check --if-enabled` before you finish and fix what it reports.
```

## Running the review

The default check includes local review and can take minutes. Run it in the
foreground and poll the shell session until it exits. Do not detach with
`nohup` or `&`. The prompt carries the captured diff. Reviewers read repository content,
which is untrusted. Claude and DeepSeek reviewers may only read and search
files; Slopcop refuses their requests to run commands or change files. Codex
reviewers run in Codex's read-only sandbox.

## The push gate

`slopcop init` also adds a user-level `PreToolUse` hook on `Bash` to
`$CODEX_HOME/hooks.json` (or `~/.codex/hooks.json`) that runs
`slopcop gate --hook codex`. It blocks
`git push` and `gh pr create` until HEAD's tree has a passing check or valid
override, and names the reason, which Codex hands to the agent. Codex sends
every shell command to the hook as `Bash` and blocks one when the hook exits
`2` with a reason; this follows Codex 0.167's source and has not yet been run
in a live session. Like the Stop hook, it runs only once you trust it with
`/hooks`.

## The Stop hook

`slopcop init` adds a user-level `Stop` hook to Codex's `hooks.json`, and
leaves everything else in the file as it was.

Codex runs a new or changed user-level hook only once you trust it: open Codex
and review it with `/hooks`. Init never writes that trust itself. The hook
stays the same from one release to the next, so an update does not ask for it
again.

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
its turn. A move counts as writing its destination. For the static half,
the CLI sends only these attribution fields to SlopCop, never the session
conversation, tool input, or file contents. The review half also reads the
user's messages from the rollout for intent. The reviewer prompts carry
the diff and the user messages to model providers through your own
logins. Review data does not go to SlopCop. Use `--mode static` to avoid
local model review. `slopcop --verbose` prints it.
