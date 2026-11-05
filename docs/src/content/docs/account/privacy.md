---
title: What leaves your machine
description: "Exactly what a slopcop check sends, what the service does with it, what it never does, and what an agent session adds."
sidebar:
  order: 7
---

## What the static half sends

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
and each model and effort with the repository-relative files it wrote. For the
static half, the CLI reads only those attribution fields from the session record
under `~/.claude` or `~/.codex`, not the conversation, tool input, or file
contents from the session. Run outside an agent, a check sends none of it. It is
analytics only, asserted by the client, and never decides a check or what it
costs.

## What local review sends

In default `both` mode or `ai` mode, reviewers read the captured diff and
receive a prompt with the diff command and the user's intent. Their models see
the diff when reviewers fetch it with their tools. The CLI obtains intent from
the current Claude Code transcript or Codex rollout when available, then from
`--intent`, else labels it `not available`. Reviewer adapters use your existing
logins to send these prompts to the chosen model providers: OpenAI through Codex
for GPT Terra, DeepSeek through dsh for DeepSeek Flash, and Anthropic through
Claude for Claude Haiku. Review results and logs stay under
`<git-dir>/slopcop/`; nothing from the local review goes to the SlopCop service.
Use `--mode static` to avoid the local review and these provider requests.
Reviewers can read files and run commands in the repository root. Slopcop
refuses their requests to edit, delete, or move files and runs Codex in its
read-only sandbox. Commands can still write, so this is not a security boundary.

## What the service does with it

The service applies the patch to the base commit in its own clone of the
repository and runs Bifrost over the files the change writes. The result is
kept so that an identical request within an hour attaches to the same analysis,
and so the workspace's Checks page can show what was found and ignored.

## What the SlopCop service never does

- **Never runs your code.** The analysis reads source as written. Nothing is
  built or executed.
- **Never sends your code to a language model through the SlopCop service.**
  The hosted half is static analysis. Local reviewers send the diff to
  model providers under your own harness logins.
- **Never writes to your repository.** The GitHub App asks for read-only
  contents and metadata; the optional issues permission is for the web app's
  issue filing, not checks.
- **Never puts source in a finding.** A finding carries the rule, severity,
  certainty, file, line, and message. The workspace's Checks page shows a
  private repository's examples only to members whose own connection can read
  it.
- **Never reads conversation for static attribution.** The local review
  reads the user's own messages from a Claude Code transcript or Codex
  rollout, when found, to provide intent. It does not read assistant
  messages for this purpose.

## What stays on your machine

- The credential, in `slopcop/config.json` under the user config directory,
  written owner-only.
- The time of the last upgrade notice.
- Backups of a skill with local edits, under `skill-backups`.
- The Stop hook's record of the last check in a session, so it does not resend
  a check whose result the session already has.

[Sign in](/account/sign-in/) names the config directory on each platform.
