---
title: Repositories
description: "A repository's first check adds it to the workspace, and `slopcop enable` can add it up front. Listing, enabling, and disabling repositories, the active-repository limit, and the steps an agent cannot take."
sidebar:
  order: 3
---

```console
slopcop status                     # this checkout's repository, and whether the workspace checks it
slopcop enable                     # optional: add this checkout's repository now, and say which workspace
slopcop repo list                  # the workspace's repositories: active, disabled, or over the limit
slopcop repo disable acme/widgets  # stop checking one (owner only)
slopcop repo enable acme/widgets   # turn it back on, within the workspace's active limit
```

`slopcop status` shows the checkout's repository (the one `origin` names),
whether it is added to the workspace, and its state, beside the account and
workspace that bill checks. `--root` names another checkout, `-w`/`--workspace`
another workspace, and `--format human|json` the output.

`repo list` takes `--format human|json`, as do `repo enable` and
`repo disable` for their answer. All three take `-w`/`--workspace` for another
workspace of the account.

## The first check adds a repository

Open your agent in a repository and let it check its change: a check of a
repository the workspace has never added adds it to the workspace the check
runs in (the one the checkout is mapped to, the login's, or the one
`--workspace` names), which then pays for its checks, and says
`added owner/name to "Workspace"` on stderr. An agent's checks and the Stop
hook add a repository the same way, so an agent is not stopped to ask.
Repositories are free; a check uses one of the workspace's free checks for the
month, then credits. A public repository works at once; a private one needs the
workspace's
[GitHub App, GitLab, or Bitbucket connection](/account/private-repositories/).

## `slopcop enable` (optional)

`slopcop enable` in a checkout does the same up front: it adds the repository
its `origin` remote names, runs the repository's first check, exits with that
check's result, and maps this machine's checkouts of it to the workspace, so
later checks run there whatever workspace the login is bound to. Use it to add
a repository before its first change, to pick its workspace with
`--workspace`, or to turn a disabled one back on.

It does not ask. It says which workspace it added the repository to and how to
undo that:

```console
$ slopcop enable
slopcop: added acme/widgets to "Acme team"; your agent's changes here are checked there from now on.
slopcop: wrong workspace? Run `slopcop enable --workspace <name>` here to add it to another (`slopcop workspace list` names them), then `slopcop repo disable acme/widgets --workspace "Acme team"` to remove it from "Acme team".
```

An account with one workspace gets the first line alone, ending with the
`slopcop repo disable owner/name` that undoes it. When another of your
workspaces already has the repository, `enable` says so first, since both
would then check and bill it.

A repository that is already active is left alone. One that is added but
disabled is enabled again, but only at a terminal: someone chose to disable
it, and an agent running with your login does not undo that. `slopcop status`
shows which of these a checkout is in.

A check does not turn a repository back on. One the workspace has disabled
exits `4` with `repository_disabled`, and one past the active limit with
`active_repository_limit_reached`; neither sends anything or starts the local
reviewers. An automatic check can pass `--if-enabled` to exit `0` and send
nothing instead. The agent skill uses it, and the Stop hook makes the same
check before it sends anything. When a check reports `repository_disabled`, the
workspace has the repository switched off, by a person or because it was added
past the active limit, so an agent following the skill stops and tells you:
only a person turns it back on.

`--add-repository` is no longer needed; a check still accepts it.

## Active and disabled

Every repository a workspace has is **active** or **disabled**. Repositories
are free ([Credits](/account/credits/)); a disabled one is not checked, and
keeps its reports, findings, and settings. A check of a disabled repository
answers `repository_disabled`; `slopcop whoami` names the workspace that
disabled it.

Only the workspace's owner enables or disables. An agent or CI job runs with
your login, and the service cannot tell it from you, so both stay steps you
take yourself: `repo enable` refuses when stdin is not a terminal, and
`repo disable`, which stops an agent's own checks, also asks you to type the
repository's `owner/name`.

Names match without regard to case; when two providers have a repository of
the same name, pass the id the listing prints.

## The active-repository limit

A workspace that has never paid (never topped up, had a coupon applied, or been
unlocked by an operator) has no limit for its first 45 days, then **one**
active repository until it unlocks. A workspace that has paid once has no limit,
for good. `repo list` marks a repository that is enabled but over the limit, and
the web app's Repositories page is where the owner chooses which stay active.
Ten days before the limit applies, the workspace is told by email and in the
app. A check of a repository over the limit answers
`active_repository_limit_reached`.

## What stays in the web app

Deleting a repository and its records, archiving a workspace, members, credits,
and turning [pull request checks](/checks/pull-requests/) on or off stay in the
web app. See [The web app](/account/web-app/). A repository's own rules live in
the repository itself; see [Repository rules](/checks/repository-rules/).
