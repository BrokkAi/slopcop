---
title: Claude Code
description: "Where the SlopCop skill lives for Claude Code, how to invoke it, the Stop hook that checks what a session wrote, and the one line to add to CLAUDE.md."
sidebar:
  order: 2
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

`slopcop init` puts the skill in `.claude/skills/slopcop/` in the current
repository, so it is available for that project. Claude uses it when you ask it
to check its change, and you can run it yourself with `/slopcop`.

```console
slopcop init
```

A check updates an existing copy when it differs from the bundled skill, but
creates none. Login does not install it. Existing copies follow the instructions
of the slopcop version that ran the check. [The agent skill](/agents/skill/)
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

## Running the review

The default check includes local review and can take minutes. Run Bash with
`run_in_background: true`, then poll TaskOutput until it finishes. The prompt carries the captured diff. Reviewers read repository content,
which is untrusted. Claude and DeepSeek reviewers may only read and search
files; Slopcop refuses their requests to run commands or change files. Codex
reviewers run in Codex's read-only sandbox. The push gate
installed by init is Claude Code only: it blocks `git push` and `gh pr
create` until HEAD's tree has a passing check or valid override. After
checking a dirty tree, commit exactly that state before pushing.

The gate is a guard for the agent's workflow, not a security boundary. It
checks HEAD in the directory Claude Code runs the command from: it does not
follow `cd` or `git -C` in the command, read the pushed refspecs, or expand
shell aliases. It matches commands by text, so it can also block a command
that only mentions `git push`. Pushes from your own terminal are not affected.
`slopcop init` lists its files in the repository's `.git/info/exclude`, which
linked worktrees of the same repository share.

## The Stop hook

`slopcop init` also adds a `Stop` hook to Claude Code's
`.claude/settings.local.json` in the repository, and leaves everything else in
the file as it was. When Claude stops, the hook checks the working tree through
the same hosted API, but only when the session wrote files in the repository (or
a dependency manifest or lockfile changed), slopcop is signed in with the
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
a file. The static half sends only these attribution fields to SlopCop,
never the conversation, tool input, or file contents from the session. The
review half also reads the user's messages from the transcript for intent
and includes them and the diff in prompts sent to the chosen reviewer model
providers under your own logins. Review data does not go to SlopCop. Use
`--mode static` to avoid local model review.
`slopcop --verbose` prints it.

## Dogfooding tip

Agents typically run `slopcop` after committing, often after pushing. On a
branch whose upstream already has every commit, the default base falls back to
the merge base with `origin/HEAD`, so the check still covers the branch. On the
default branch itself, with nothing unpushed, the patch is empty: the report
says `emptyChange: true` and stderr suggests `--base`. Tell Claude which branch
the change targets, for example `slopcop --base origin/main`, when it checks
committed work.
