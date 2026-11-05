---
title: GitHub Copilot CLI
description: "What a check run inside GitHub Copilot CLI sends about the models that wrote the change, read from the session's events.jsonl, and what is not supported yet."
sidebar:
  order: 4
---

> **Not yet verified against a live session.** Copilot CLI is not open source.
> What follows is built from the session event schema GitHub publishes in the
> [Copilot SDK](https://github.com/github/copilot-sdk/blob/99d90edf504f824c77dcdf0faa978f4b35848a87/nodejs/src/generated/session-events.ts),
> the CLI's [changelog](https://github.com/github/copilot-cli/blob/main/changelog.md)
> and its [hooks reference](https://docs.github.com/en/copilot/reference/hooks-configuration),
> not from a captured session.

## Checking a change

Add the repository to the workspace from its checkout with `slopcop enable` at
a terminal, then ask Copilot to check its change, or add the usual line to your
project's instructions:

```text
Run `slopcop check --if-enabled` before you finish and fix what it reports.
```

There is no Stop hook for Copilot CLI yet; `slopcop init` installs none there.

## What the CLI reads from the session

Copilot CLI names its session in the commands the agent runs
(`COPILOT_AGENT_SESSION_ID`, since Copilot CLI 1.0.29). From that session's
`events.jsonl` under `~/.copilot/session-state/<session>/` (or
`$COPILOT_HOME/session-state/<session>/`) the CLI reads:

- **Files:** the `fileEdits` a built-in edit tool committed, as
  `tool.execution_complete` records them. An edit that failed committed
  nothing; one that failed part way counts for the files it did write. A delete
  writes nothing. `fileEdits` arrived in Copilot CLI 1.0.92 and is marked
  experimental, so an older session sends no files.
- **Model:** the model the tool call names, on `tool.execution_complete` or on
  its `tool.execution_start`.
- **Effort:** the session's reasoning effort as `session.start`,
  `session.resume` and `session.model_change` last set it, or for a subagent
  the effort `subagent.configured` resolved. An effort bound to another model
  than the call's is left out, as is one the session never set.
- **Version:** `copilotVersion` from `session.start`.

It reads only those fields, never the conversation, a tool's input, or a file's
contents, and sends the harness (`copilot-cli`), its version, the session id as
an opaque identifier, and each model and effort with the repository-relative
files it wrote. This is analytics only and never decides a check.
`slopcop --verbose` prints it.

A live session still has to confirm that the variable is set in the agent's
shell, that `events.jsonl` holds the schema's events in that shape and is
written before the agent runs the check, and where effort shows up.
