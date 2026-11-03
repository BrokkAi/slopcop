---
title: What leaves your machine
description: "Exactly what a slopcop check sends, what the service does with it, what it never does, and what an agent session adds."
sidebar:
  order: 7
---

## What a check sends

- **The repository's URL**, canonicalized from the `origin` remote.
- **The base commit's hash.**
- **The patch**: one git patch from the base commit to the working tree. It
  can hold unpublished source and binary files, so review what your working
  tree holds before an agent checks it. Git-ignored files stay out, except
  selected `.slopcop/policies` rules.
- **The policy selection** and the `--fail-on` level.
- **The bearer token** of the login.

`slopcop --verbose` prints the resolved request to stderr before it is sent.

## What an agent session adds

Run by Claude Code or Codex, a check also sends which models wrote the change:
the harness and its version, the session or thread id as an opaque identifier,
and each model and effort with the repository-relative files it wrote. The CLI
reads only those fields from the session's own record under `~/.claude` or
`~/.codex`, never the conversation, a tool's input, or a file's contents. Run
outside an agent, a check sends none of it. It is analytics only, asserted by
the client, and never decides a check or what it costs.

## What the service does with it

The service applies the patch to the base commit in its own clone of the
repository and runs Bifrost over the files the change writes. The result is
kept so that an identical request within an hour attaches to the same analysis,
and so the workspace's Checks page can show what was found and ignored.

## What it never does

- **Never runs your code.** The analysis reads source as written. Nothing is
  built or executed.
- **Never sends your code to a language model.** A check is static analysis
  only.
- **Never writes to your repository.** The GitHub App asks for read-only
  contents and metadata; the optional issues permission is for the web app's
  issue filing, not checks.
- **Never puts source in a finding.** A finding carries the rule, severity,
  certainty, file, line, and message. The workspace's Checks page shows a
  private repository's examples only to members whose own connection can read
  it.
- **Never reads your session's conversation.** Only the model, effort, and
  file of each edit.

## What stays on your machine

- The credential, in `slopcop/config.json` under the user config directory,
  written owner-only.
- The time of the last upgrade notice.
- Backups of a skill with local edits, under `skill-backups`.
- The Stop hook's record of the last check in a session, so it does not resend
  a check whose result the session already has.

[Sign in](/account/sign-in/) names the config directory on each platform.
