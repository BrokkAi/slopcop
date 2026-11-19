---
title: GitHub Copilot CLI
description: "Running the local review through GitHub Copilot CLI, what a check run inside Copilot CLI sends about the models that wrote the change, and what is not supported yet."
sidebar:
  order: 4
---

> **What a check reads from a Copilot session is not yet verified against a
> live session.** Copilot CLI is not open source. That part is built from the
> session event schema GitHub publishes in the
> [Copilot SDK](https://github.com/github/copilot-sdk/blob/99d90edf504f824c77dcdf0faa978f4b35848a87/nodejs/src/generated/session-events.ts),
> the CLI's [changelog](https://github.com/github/copilot-cli/blob/main/changelog.md)
> and its [hooks reference](https://docs.github.com/en/copilot/reference/hooks-configuration),
> not from a captured session.

## Checking a change

The first check in a repository adds it to your workspace (`slopcop enable`
does that up front, if you prefer). Ask Copilot to check its change, or add the usual line to your
project's instructions:

```text
Run `slopcop check --if-enabled` before you finish and fix what it reports.
```

There is no Stop hook for Copilot CLI yet; `slopcop init` installs none there.

## Copilot as a reviewer

Copilot CLI can also run the local review, on your Copilot subscription,
whichever agent wrote the change: Copilot itself, Pi on its Copilot provider,
or VS Code. Set it with:

```console
slopcop config set review.provider copilot
slopcop config unset review.reviewers
slopcop init
```

`slopcop init` also picks Copilot by itself when no reviewer of its own works
and Copilot may be signed in (a `GH_TOKEN`, `GITHUB_TOKEN` or
`COPILOT_GITHUB_TOKEN`, a Copilot CLI login, or a GitHub CLI login). Each
probe it sends uses one Copilot request.

Copilot runs `terra`, `sol` and `haiku`; `flash` keeps dsh. The CLI starts
Copilot (`@github/copilot@1.0.114`, through npx, unless
`review.adapters.copilot.command` names another) once to list the models your
account may use, which sends no prompt, and then once per reviewer, with:

- **The newest model of the family** your account has enabled, through
  `--model`. A model Copilot lists but has not enabled fails the reviewer and
  says so, rather than running another model.
- **Reasoning effort `high`**, through `--reasoning-effort`. If Copilot will
  not start with it, which it does for a model without effort settings, the
  reviewer starts again at the model's default effort and the report says
  why.
- **Only read tools**, through `--available-tools view grep glob`, with
  `allow_all` off and the session out of autopilot. The reviewer cannot ask
  to write or run anything, and the CLI refuses any request that is not a
  read.

A reply that is Copilot's own error, such as `Error: Execution failed: ...`,
fails the reviewer rather than counting as a review.

The review path is checked against Copilot CLI 1.0.114 on Copilot Free; the
paid models have not yet been run from a live account.

### Copilot Free

Copilot Free offers only Auto, the model Copilot picks itself, and no
reasoning effort on it. One reviewer then runs on Auto, and the report warns
that it did; other families do not run, because a second Auto reviewer could
get the same model. With one reviewer, a clean review needs only that one.
Copilot Free also has a small monthly request allowance that every check
draws on. Copilot Pro unlocks the `terra`, `sol` and `haiku` models.

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
