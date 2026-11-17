---
title: Repositories
description: "`slopcop enable` adds a repository to the workspace. Listing, enabling, and disabling repositories, the active-repository limit, and the confirmation an agent cannot give."
sidebar:
  order: 3
---

```console
slopcop status                     # this checkout's repository, and whether the workspace checks it
slopcop enable                     # add this checkout's repository to the workspace, at a terminal
slopcop repo list                  # the workspace's repositories: active, disabled, or over the limit
slopcop repo disable acme/widgets  # stop checking and charging for one (owner only)
slopcop repo enable acme/widgets   # turn it back on, within the workspace's active limit
```

`slopcop status` shows the checkout's repository (the one `origin` names),
whether it is added to the workspace, and its state, beside the account and
workspace that bill checks. `--root` names another checkout, `-w`/`--workspace`
another workspace, and `--format human|json` the output.

`repo list` takes `--format human|json`, as do `repo enable` and
`repo disable` for their answer. All three take `-w`/`--workspace` for another
workspace of the account.

## `slopcop enable` adds a repository

Run `slopcop enable` in a checkout to add the repository its `origin` remote
names to the workspace that checks and bills it. It names the workspace and
what adding the repository costs, asks you to type the repository's
`owner/name`, and refuses when stdin is not a terminal. It then runs the
repository's first check and exits with that check's result. A public
repository works at once; a private one needs the workspace's
[GitHub App, GitLab, or Bitbucket connection](/account/private-repositories/).

A repository that is already added but disabled is enabled again. One that is
already active is left alone. `slopcop status` shows which of these a checkout
is in.

You do not have to run `slopcop enable` first. A check of a repository the
workspace has never added adds it to the workspace the check runs in (the
login's, or the one `--workspace` names), which then pays for its checks, and
says `added owner/name to "Workspace"` on stderr. An agent's checks and the Stop
hook add a repository the same way, so an agent is not stopped to ask.

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

Only the workspace's owner enables or disables, and `repo enable` and
`repo disable` ask you to type the repository's `owner/name` and refuse when
stdin is not a terminal. An agent or CI job runs with your login, and the
service cannot tell it from you, so changing what the workspace pays stays a
step you take yourself.

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
